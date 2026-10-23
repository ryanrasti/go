// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package wasm

// cgo on linux/wasm (linux-workers: docs/ports/cgo.md). The C side of a
// program (cgo's wrappers, the packages' C, runtime/cgo's) is linked by
// the C toolchain into one relocatable object, with libc and what the
// packages' LDFLAGS name (wasm-ld -r), and that object is linked into the
// module here, as internal linking loads ELF host objects elsewhere:
//
//   - C's data segments are symbols of Go's data layout;
//   - C's functions follow Go's, each in the table at Go's offset
//     (funcValueOffset + its index), which is a C function pointer's
//     value (Go's are that <<16);
//   - C's imports (linux.syscall, ...) join Go's;
//   - C calls Go's wasmexports (runtime/cgo's crosscall2), and takes
//     Go functions' addresses (cgo's _cgoexp_ functions, for crosscall2);
//   - C's stack pointer is a global of its own, its stack in Go's bss.
//
// Relocations are applied in place: in an object each relocated field is
// at its widest (a 5-byte LEB, say). The runtime calls C through the
// table as int (*)(void *), type 1 (asmcgocall).

import (
	"bytes"
	"cmd/internal/obj"
	"cmd/internal/objabi"
	"cmd/internal/wasmobj"
	"cmd/link/internal/ld"
	"cmd/link/internal/loader"
	"cmd/link/internal/sym"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// cgoStackSize is C's stack, as the C toolchain's programs have it
// (-z stack-size).
const cgoStackSize = 1 << 20

// cgoType is asmcgocall's call_indirect type, int (*)(void *).
var cgoType = &wasmFuncType{Params: []byte{I32}, Results: []byte{I32}}

type cgoLink struct {
	f *wasmobj.File

	segs []loader.Sym // each data segment's symbol
	// funcVals are value symbols of C functions (their table indices),
	// for Go's references and C data's: by C function
	funcVals map[uint32]loader.Sym
	stack    loader.Sym
	// linker-defined data symbols (__heap_end, ...): value symbols too
	defined map[string]loader.Sym

	res []cgoRes // by C symbol index

	imports []cgoImport // C's host imports
	// importIdx is each C function import's: an index into imports, or -1
	importIdx []int

	extra []*cgoExtra // functions the linker writes
	tags  []uint32    // the module's tags (C's), by type
	// globals: C's defined globals (after the C stack pointer)
	globals []wasmobj.Global

	numGoFuncs int // set by asmb: the C functions' table indices follow

	alias      []cgoAlias
	goVals     []cgoGoVal
	codeRelocs map[uint32][]wasmobj.Reloc // by C function
}

type cgoImport struct {
	module, name string
	typ          wasmobj.FuncType
}

// cgoExtra is a function the linker writes: __wasm_call_ctors, or a stub
// for a weak function that's undefined (it traps).
type cgoExtra struct {
	name  string
	typ   wasmobj.FuncType
	ctors []uint32 // __wasm_call_ctors: C symbols, in order
}

const (
	resNone   = iota
	resCFunc  // k: a C function
	resGo     // s: a Go function (a wasmexport, called; or any, its address)
	resImport // k: C's imports' index (imports)
	resExtra  // k: extra's index
	resData   // s + off: a data symbol's address
	resGlobal // k: the module's global
	resTag    // k: the module's tag
	resTable  // the table
	resZero   // weak and undefined: 0
)

type cgoRes struct {
	kind int
	k    uint32
	s    loader.Sym
	off  int64
}

var cgo *cgoLink

// cgoGlobalSP is C's stack pointer: after Go's globals (writeGlobalSec).
// Then thread-local storage's, which musl's clone and pthread_create use
// (posix_spawn's vfork child): there's none (load refuses its segments),
// so __tls_base 0, __tls_size 0 and __tls_align 1. Then C's own globals.
const (
	cgoGlobalSP       = 8
	cgoGlobalTLS      = 9
	cgoGlobalTLSSize  = 10
	cgoGlobalTLSAlign = 11
	cgoGlobalC        = 12
)

func linkWasmHostObjs(ctxt *ld.Link, ldr *loader.Loader, objs []ld.WasmHostObj, ldflags []string) {
	b, err := cgoPrelink(ctxt, objs, ldflags)
	if err != nil {
		ld.Exitf("cgo: %v", err)
	}
	f, err := wasmobj.Read(b)
	if err != nil {
		ld.Exitf("cgo: %v", err)
	}
	c := &cgoLink{f: f, funcVals: map[uint32]loader.Sym{}, defined: map[string]loader.Sym{}}
	if err := c.load(ctxt, ldr); err != nil {
		ld.Exitf("cgo: %v", err)
	}
	cgo = c
}

// cgoPrelink links the host objects, with libc and the packages' LDFLAGS,
// into one relocatable object: the C compiler's -r, which links an archive
// member for each symbol undefined (as a program's link would; but no
// startup files, libraries or optimizer: -nostdlib, -O0). libc and the
// compiler's builtins go to the linker in a response file (-Wl,@file),
// which a wrapper of the linker that drops them for -r doesn't see.
func cgoPrelink(ctxt *ld.Link, objs []ld.WasmHostObj, ldflags []string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "go-link-cgo-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	extld := ctxt.Extld()
	query := func(arg string) (string, error) {
		out, err := exec.Command(extld[0], append(extld[1:len(extld):len(extld)], arg)...).Output()
		if err != nil {
			return "", fmt.Errorf("%s %s: %v", extld[0], arg, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	libc, err := query("-print-file-name=libc.a")
	if err != nil {
		return nil, err
	}
	builtins, err := query("-print-libgcc-file-name")
	if err != nil {
		return nil, err
	}
	rsp := filepath.Join(dir, "libs.rsp")
	if err := os.WriteFile(rsp, []byte(libc+"\n"+builtins+"\n"), 0o666); err != nil {
		return nil, err
	}
	out := filepath.Join(dir, "cgo.o")
	args := append(extld[1:len(extld):len(extld)], "-O0", "-r", "-nostdlib", "-o", out)
	for i, o := range objs {
		name := filepath.Join(dir, fmt.Sprintf("%06d.o", i))
		if err := os.WriteFile(name, o.Data, 0o666); err != nil {
			return nil, err
		}
		args = append(args, name)
	}
	args = append(args, ldflags...)
	args = append(args, ld.ExtldFlags()...)
	args = append(args, "-Wl,@"+rsp)
	if ctxt.Debugvlog != 0 {
		ctxt.Logf("cgo: %s %v\n", extld[0], args)
	}
	cmd := exec.Command(extld[0], args...)
	if msg, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%s -r: %v\n%s", extld[0], err, msg)
	}
	return os.ReadFile(out)
}

// load makes symbols of C's data, and resolves C's symbols.
func (c *cgoLink) load(ctxt *ld.Link, ldr *loader.Loader) error {
	f := c.f
	roots := &ctxt.WasmHostRoots

	// data segments
	for i := range f.Segments {
		seg := &f.Segments[i]
		if seg.Flags&wasmobj.SegTLS != 0 {
			return fmt.Errorf("%s: thread-local storage (_Thread_local, __thread) isn't supported", seg.Name)
		}
		s := ldr.CreateStaticSym(fmt.Sprintf("cgo(%s)", seg.Name))
		sb := ldr.MakeSymbolUpdater(s)
		switch {
		case strings.HasPrefix(seg.Name, ".bss"), strings.HasPrefix(seg.Name, ".tbss"):
			sb.SetType(sym.SNOPTRBSS)
		case strings.HasPrefix(seg.Name, ".rodata"):
			sb.SetType(sym.SRODATA)
		default:
			sb.SetType(sym.SNOPTRDATA)
		}
		if sb.Type() != sym.SNOPTRBSS {
			sb.SetData(append([]byte(nil), seg.Data...))
		}
		sb.SetSize(int64(len(seg.Data)))
		sb.SetAlign(int32(1) << seg.Align)
		c.segs = append(c.segs, s)
		*roots = append(*roots, s)
	}

	// C's stack
	c.stack = ldr.CreateStaticSym("cgo.stack")
	sb := ldr.MakeSymbolUpdater(c.stack)
	sb.SetType(sym.SNOPTRBSS)
	sb.SetSize(cgoStackSize)
	sb.SetAlign(16)
	*roots = append(*roots, c.stack)

	// imports: C's host imports (a module other than env), and the rest,
	// which are Go's or the linker's to define (by their symbols)
	c.importIdx = make([]int, f.NumFuncImports)
	{
		k := 0
		for i := range f.Imports {
			im := &f.Imports[i]
			if im.Kind != wasmobj.KindFunc {
				continue
			}
			c.importIdx[k] = -1
			if im.Module != "env" {
				// (one import for each, though C and Go may both import it)
				c.importIdx[k] = -1
				for j, ci := range c.imports {
					if ci.module == im.Module && ci.name == im.Name && ci.typ.Equal(f.Types[im.Type]) {
						c.importIdx[k] = j
					}
				}
				if c.importIdx[k] < 0 {
					c.importIdx[k] = len(c.imports)
					c.imports = append(c.imports, cgoImport{im.Module, im.Name, f.Types[im.Type]})
				}
			}
			k++
		}
	}

	// symbols
	c.res = make([]cgoRes, len(f.Symbols))
	stubs := map[string]uint32{} // weak functions' stubs, by type
	var ctors *cgoExtra
	for i := range f.Symbols {
		s := &f.Symbols[i]
		r := &c.res[i]
		switch s.Kind {
		case wasmobj.SymFunction:
			if !s.Undefined() {
				r.kind, r.k = resCFunc, s.Index-uint32(f.NumFuncImports)
				break
			}
			if s.Index >= uint32(f.NumFuncImports) {
				return fmt.Errorf("%s: undefined function with no import", s.Name)
			}
			if j := c.importIdx[s.Index]; j >= 0 {
				r.kind, r.k = resImport, uint32(j)
				break
			}
			t := f.Types[f.FuncImport(s.Index).Type]
			if s.Name == "__wasm_init_tls" {
				// (no thread-local storage to initialize)
				r.kind, r.k = resExtra, uint32(len(c.extra))
				c.extra = append(c.extra, &cgoExtra{name: s.Name, typ: t})
				break
			}
			if s.Name == "__wasm_call_ctors" {
				if ctors == nil {
					ctors = &cgoExtra{name: s.Name, typ: t}
					c.extra = append(c.extra, ctors)
				}
				r.kind, r.k = resExtra, uint32(len(c.extra)-1)
				for j, e := range c.extra {
					if e == ctors {
						r.k = uint32(j)
					}
				}
				break
			}
			if g := goFunc(ldr, s.Name); g != 0 {
				r.kind, r.s = resGo, g
				*roots = append(*roots, g)
				break
			}
			if s.Weak() {
				key := fmt.Sprint(t.Params, t.Results)
				j, ok := stubs[key]
				if !ok {
					j = uint32(len(c.extra))
					stubs[key] = j
					c.extra = append(c.extra, &cgoExtra{name: "cgo.undefined_weak", typ: t})
				}
				r.kind, r.k = resExtra, j
				break
			}
			return fmt.Errorf("undefined: %s (a C function, referenced from C: %s)", s.Name, c.referrers(uint32(i)))
		case wasmobj.SymData:
			if !s.Undefined() {
				if int(s.Segment) >= len(c.segs) {
					return fmt.Errorf("%s: in segment %d of %d", s.Name, s.Segment, len(c.segs))
				}
				r.kind, r.s, r.off = resData, c.segs[s.Segment], int64(s.Offset)
				break
			}
			switch s.Name {
			case "__heap_end", "__heap_base", "__data_end", "__dso_handle", "__global_base", "__stack_high", "__stack_low":
				r.kind, r.s = resData, c.definedSym(ldr, s.Name)
				*roots = append(*roots, r.s)
			default:
				if s.Weak() {
					r.kind = resZero
					break
				}
				return fmt.Errorf("undefined: %s (C data, referenced from C: %s)", s.Name, c.referrers(uint32(i)))
			}
		case wasmobj.SymGlobal:
			if s.Undefined() {
				switch s.Name {
				case "__stack_pointer":
					r.kind, r.k = resGlobal, cgoGlobalSP
				case "__tls_base":
					r.kind, r.k = resGlobal, cgoGlobalTLS
				case "__tls_size":
					r.kind, r.k = resGlobal, cgoGlobalTLSSize
				case "__tls_align":
					r.kind, r.k = resGlobal, cgoGlobalTLSAlign
				default:
					return fmt.Errorf("undefined: %s (a global, referenced from C: %s)", s.Name, c.referrers(uint32(i)))
				}
				break
			}
			r.kind, r.k = resGlobal, cgoGlobalC+s.Index-uint32(f.NumGlobalImports)
		case wasmobj.SymTag:
			if s.Undefined() {
				// (setjmp's __c_longjmp, C++'s __cpp_exception: the
				// linker's, one each, as wasm-ld's)
				var t uint32
				n := uint32(0)
				for _, im := range f.Imports {
					if im.Kind == wasmobj.KindTag {
						if n == s.Index {
							t = im.Type
						}
						n++
					}
				}
				r.kind, r.k = resTag, uint32(len(c.tags))
				c.tags = append(c.tags, t)
				break
			}
			r.kind, r.k = resTag, uint32(len(c.tags))
			c.tags = append(c.tags, f.Tags[s.Index-uint32(f.NumTagImports)])
		case wasmobj.SymTable:
			r.kind = resTable
		}
	}
	if ctors != nil {
		inits := append([]wasmobj.InitFunc(nil), f.Init...)
		sort.SliceStable(inits, func(i, j int) bool { return inits[i].Priority < inits[j].Priority })
		for _, in := range inits {
			ctors.ctors = append(ctors.ctors, in.Symbol)
		}
	}
	c.globals = f.Globals

	// Go's references to C's functions (cgo's wrappers, runtime/cgo's
	// x_cgo_init): value symbols, the functions' table indices
	for i := range f.Symbols {
		s := &f.Symbols[i]
		if s.Kind != wasmobj.SymFunction || s.Undefined() || s.Local() || s.Name == "" {
			continue
		}
		g := ldr.Lookup(s.Name, 0)
		if g == 0 {
			continue
		}
		if t := ldr.SymType(g); t != 0 && t != sym.SXREF && t != sym.SHOSTOBJ {
			continue // (Go's own)
		}
		c.funcVal(ldr, c.res[i].k, g)
	}

	// relocations of data: Go's, to the segments and value symbols
	for _, rel := range f.DataRelocs() {
		seg := -1
		for j := range f.Segments {
			sg := &f.Segments[j]
			if rel.Offset >= sg.Off && rel.Offset < sg.Off+uint32(len(sg.Data)) {
				seg = j
				break
			}
		}
		if seg < 0 {
			return fmt.Errorf("a data relocation at 0x%x in no segment", rel.Offset)
		}
		off := int32(rel.Offset - f.Segments[seg].Off)
		r := &c.res[rel.Index]
		var target loader.Sym
		var add int64
		switch rel.Type {
		case wasmobj.R_MEMORY_ADDR_I32:
			switch r.kind {
			case resData:
				target, add = r.s, r.off+rel.Addend
			case resZero:
				continue
			default:
				return fmt.Errorf("data relocation to %s: not data", f.Symbols[rel.Index].Name)
			}
		case wasmobj.R_TABLE_INDEX_I32:
			switch r.kind {
			case resCFunc:
				target = c.funcVal(ldr, r.k, 0)
			case resGo:
				target, add = c.goFuncVal(ldr, r.s), 0
			case resZero:
				continue
			default:
				return fmt.Errorf("data relocation to %s: a function pointer to an import, or a function the linker writes", f.Symbols[rel.Index].Name)
			}
		case wasmobj.R_FUNCTION_INDEX_I32:
			return fmt.Errorf("unsupported data relocation (function index) to %s", f.Symbols[rel.Index].Name)
		default:
			return fmt.Errorf("unsupported data relocation type %d to %s", rel.Type, f.Symbols[rel.Index].Name)
		}
		sb := ldr.MakeSymbolUpdater(c.segs[seg])
		rr, _ := sb.AddRel(objabi.R_ADDR)
		rr.SetOff(off)
		rr.SetSiz(4)
		rr.SetSym(target)
		rr.SetAdd(add)
	}
	for _, s := range c.segs {
		ldr.MakeSymbolUpdater(s).SortRelocs()
	}

	// relocations of code, by function
	c.codeRelocs = map[uint32][]wasmobj.Reloc{}
	for _, rel := range f.CodeRelocs() {
		k := sort.Search(len(f.Funcs), func(k int) bool {
			return f.Funcs[k].Off+uint32(len(f.Funcs[k].Body)) > rel.Offset
		})
		if k == len(f.Funcs) || rel.Offset < f.Funcs[k].Off {
			return fmt.Errorf("a code relocation at 0x%x in no function", rel.Offset)
		}
		c.codeRelocs[uint32(k)] = append(c.codeRelocs[uint32(k)], rel)
	}
	return nil
}

// goFunc is the Go function C refers to by name: a wasmexport's wrapper
// (crosscall2), or a cgo export (_cgoexp_...).
func goFunc(ldr *loader.Loader, name string) loader.Sym {
	if s, ok := ldr.CgoExports[name]; ok {
		return s
	}
	for _, v := range []int{0, sym.SymVerABIInternal} {
		if s := ldr.Lookup(name, v); s != 0 && ldr.SymType(s).IsText() {
			return s
		}
	}
	return 0
}

// funcVal is C function k's value symbol (named g, if Go refers to it).
func (c *cgoLink) funcVal(ldr *loader.Loader, k uint32, g loader.Sym) loader.Sym {
	if s, ok := c.funcVals[k]; ok && g == 0 {
		return s
	}
	if g == 0 {
		g = ldr.CreateStaticSym(fmt.Sprintf("cgo.func.%d", k))
	}
	sb := ldr.MakeSymbolUpdater(g)
	sb.SetType(sym.SHOSTOBJ)
	sb.SetSize(0)
	if _, ok := c.funcVals[k]; !ok {
		c.funcVals[k] = g
	} else {
		// (two names, one function: both valued alike, by asmb)
		c.alias = append(c.alias, cgoAlias{k, g})
	}
	return g
}

type cgoAlias struct {
	k uint32
	s loader.Sym
}

// goFuncVal is a Go function's value symbol for C: its table index.
func (c *cgoLink) goFuncVal(ldr *loader.Loader, g loader.Sym) loader.Sym {
	for _, v := range c.goVals {
		if v.g == g {
			return v.s
		}
	}
	s := ldr.CreateStaticSym("cgo.gofunc." + ldr.SymName(g))
	sb := ldr.MakeSymbolUpdater(s)
	sb.SetType(sym.SHOSTOBJ)
	c.goVals = append(c.goVals, cgoGoVal{g, s})
	return s
}

type cgoGoVal struct {
	g, s loader.Sym
}

// definedSym is a data symbol the linker defines (valued by asmb).
func (c *cgoLink) definedSym(ldr *loader.Loader, name string) loader.Sym {
	if s, ok := c.defined[name]; ok {
		return s
	}
	s := ldr.CreateStaticSym("cgo." + name)
	sb := ldr.MakeSymbolUpdater(s)
	sb.SetType(sym.SHOSTOBJ)
	c.defined[name] = s
	return s
}

// tableIndex is C function k's table index.
func (c *cgoLink) tableIndex(k uint32) int64 {
	return int64(funcValueOffset + c.numGoFuncs + int(k))
}

// setValues values the value symbols, once Go's functions and data are
// laid out (asmb, before data is relocated).
func (c *cgoLink) setValues(ctxt *ld.Link, ldr *loader.Loader) {
	c.numGoFuncs = len(ctxt.Textp)
	for k, s := range c.funcVals {
		ldr.SetSymValue(s, c.tableIndex(k))
	}
	for _, a := range c.alias {
		ldr.SetSymValue(a.s, c.tableIndex(a.k))
	}
	for _, v := range c.goVals {
		ldr.SetSymValue(v.s, ldr.SymValue(v.g)>>16)
	}
	heapEnd := int64(initialMemoryPages(ldr) * wasmPageSize)
	dataEnd := ldr.SymValue(ldr.Lookup("runtime.end", 0))
	for name, s := range c.defined {
		var v int64
		switch name {
		case "__heap_end":
			// the memory's initial end: libc's break starts there
			// (runtime/cgo's x_cgo_init)
			v = heapEnd
		case "__heap_base", "__data_end":
			v = dataEnd
		case "__stack_low":
			v = ldr.SymValue(c.stack)
		case "__stack_high":
			v = ldr.SymValue(c.stack) + cgoStackSize
		case "__dso_handle", "__global_base":
			v = 0
		}
		ldr.SetSymValue(s, v)
	}
}

// funcIndexer gives the module's function indices: of C's function k,
// C's import j, the linker's function j, a Go function.
type funcIndexer struct {
	cfunc  func(k uint32) uint32
	cimp   func(j uint32) uint32
	extra  func(j uint32) uint32
	gofunc func(s loader.Sym) uint32
}

// relocate applies C's code relocations to function k's body (a copy).
func (c *cgoLink) relocate(ldr *loader.Loader, k uint32, idx funcIndexer, typeIdx func(wasmobj.FuncType) uint32) ([]byte, error) {
	f := c.f
	fn := &f.Funcs[k]
	body := append([]byte(nil), fn.Body...)
	for _, rel := range c.codeRelocs[k] {
		off := int(rel.Offset - fn.Off)
		var v int64
		r := &cgoRes{}
		if rel.Type != wasmobj.R_TYPE_INDEX_LEB {
			r = &c.res[rel.Index]
		}
		name := func() string { return f.Symbols[rel.Index].Name }
		switch rel.Type {
		case wasmobj.R_FUNCTION_INDEX_LEB, wasmobj.R_FUNCTION_INDEX_I32:
			switch r.kind {
			case resCFunc:
				v = int64(idx.cfunc(r.k))
			case resImport:
				v = int64(idx.cimp(r.k))
			case resExtra:
				v = int64(idx.extra(r.k))
			case resGo:
				want := f.Types[f.FuncImport(f.Symbols[rel.Index].Index).Type]
				if err := c.checkGoCall(ldr, r.s, k, want); err != nil {
					return nil, err
				}
				v = int64(idx.gofunc(r.s))
			default:
				return nil, fmt.Errorf("call to %s: not a function", name())
			}
		case wasmobj.R_TABLE_INDEX_SLEB, wasmobj.R_TABLE_INDEX_I32, wasmobj.R_TABLE_INDEX_SLEB64, wasmobj.R_TABLE_INDEX_I64:
			switch r.kind {
			case resCFunc:
				v = c.tableIndex(r.k)
			case resExtra:
				v = int64(funcValueOffset + c.numGoFuncs + len(f.Funcs) + int(r.k))
			case resGo:
				v = ldr.SymValue(r.s) >> 16
			case resZero:
				v = 0
			default:
				return nil, fmt.Errorf("%s's address: an import (not in the table)", name())
			}
		case wasmobj.R_MEMORY_ADDR_LEB, wasmobj.R_MEMORY_ADDR_SLEB, wasmobj.R_MEMORY_ADDR_I32,
			wasmobj.R_MEMORY_ADDR_LEB64, wasmobj.R_MEMORY_ADDR_SLEB64, wasmobj.R_MEMORY_ADDR_I64:
			switch r.kind {
			case resData:
				v = ldr.SymValue(r.s) + r.off + rel.Addend
			case resZero:
				v = rel.Addend
			default:
				return nil, fmt.Errorf("%s's address: not data", name())
			}
		case wasmobj.R_TYPE_INDEX_LEB:
			v = int64(typeIdx(f.Types[rel.Index]))
		case wasmobj.R_GLOBAL_INDEX_LEB, wasmobj.R_GLOBAL_INDEX_I32:
			if r.kind != resGlobal {
				return nil, fmt.Errorf("%s: not a global", name())
			}
			v = int64(r.k)
		case wasmobj.R_TAG_INDEX_LEB:
			if r.kind != resTag {
				return nil, fmt.Errorf("%s: not a tag", name())
			}
			v = int64(r.k)
		case wasmobj.R_TABLE_NUMBER_LEB:
			v = 0
		default:
			return nil, fmt.Errorf("unsupported relocation type %d (to %s) in %s", rel.Type, name(), c.funcName(k))
		}
		if err := patch(body, off, rel.Type, v); err != nil {
			return nil, fmt.Errorf("%s: %v", c.funcName(k), err)
		}
	}
	return body, nil
}

// checkGoCall checks a C call of a Go function: a wasmexport's wrapper,
// whose signature must be what C declared.
func (c *cgoLink) checkGoCall(ldr *loader.Loader, g loader.Sym, k uint32, want wasmobj.FuncType) error {
	ws := ldr.WasmTypeSym(g)
	if ws == 0 {
		return fmt.Errorf("%s calls Go's %s, which isn't a //go:wasmexport", c.funcName(k), ldr.SymName(g))
	}
	var o obj.WasmFuncType
	o.Read(ldr.Data(ws))
	have := wasmobj.FuncType{Params: fieldsToTypes(o.Params), Results: fieldsToTypes(o.Results)}
	if !have.Equal(want) {
		return fmt.Errorf("%s calls Go's %s as %v -> %v, which is %v -> %v", c.funcName(k), ldr.SymName(g), want.Params, want.Results, have.Params, have.Results)
	}
	return nil
}

// referrers names the C functions whose code refers to symbol i (for
// errors).
func (c *cgoLink) referrers(i uint32) string {
	f := c.f
	var names []string
	seen := map[int]bool{}
	for _, rel := range f.CodeRelocs() {
		if rel.Index != i || rel.Type == wasmobj.R_TYPE_INDEX_LEB {
			continue
		}
		k := sort.Search(len(f.Funcs), func(k int) bool {
			return f.Funcs[k].Off+uint32(len(f.Funcs[k].Body)) > rel.Offset
		})
		if k < len(f.Funcs) && !seen[k] {
			seen[k] = true
			names = append(names, c.funcName(uint32(k)))
		}
	}
	if len(names) > 5 {
		names = append(names[:5], "...")
	}
	return strings.Join(names, ", ")
}

func (c *cgoLink) funcName(k uint32) string {
	for i := range c.f.Symbols {
		s := &c.f.Symbols[i]
		if s.Kind == wasmobj.SymFunction && !s.Undefined() && s.Index-uint32(c.f.NumFuncImports) == k {
			return s.Name
		}
	}
	return fmt.Sprintf("C function %d", k)
}

// patch writes v into the field at off, at the field's width.
func patch(b []byte, off int, typ uint8, v int64) error {
	put := func(n int) error {
		if off < 0 || off+n > len(b) {
			return fmt.Errorf("relocation at %d past the function's end", off)
		}
		return nil
	}
	switch typ {
	case wasmobj.R_FUNCTION_INDEX_LEB, wasmobj.R_MEMORY_ADDR_LEB, wasmobj.R_TYPE_INDEX_LEB,
		wasmobj.R_GLOBAL_INDEX_LEB, wasmobj.R_TAG_INDEX_LEB, wasmobj.R_TABLE_NUMBER_LEB:
		if err := put(5); err != nil {
			return err
		}
		if v < 0 || v > 0xffffffff {
			return fmt.Errorf("relocated value %d out of range", v)
		}
		uv := uint64(v)
		for i := 0; i < 5; i++ {
			c := byte(uv & 0x7f)
			uv >>= 7
			if i < 4 {
				c |= 0x80
			}
			b[off+i] = c
		}
	case wasmobj.R_TABLE_INDEX_SLEB, wasmobj.R_MEMORY_ADDR_SLEB:
		if err := put(5); err != nil {
			return err
		}
		if int64(int32(v)) != v && int64(uint32(v)) != v {
			return fmt.Errorf("relocated value %d out of range", v)
		}
		sv := int64(int32(uint32(v)))
		for i := 0; i < 5; i++ {
			c := byte(sv & 0x7f)
			sv >>= 7
			if i < 4 {
				c |= 0x80
			}
			b[off+i] = c
		}
	case wasmobj.R_MEMORY_ADDR_LEB64:
		if err := put(10); err != nil {
			return err
		}
		uv := uint64(v)
		for i := 0; i < 10; i++ {
			c := byte(uv & 0x7f)
			uv >>= 7
			if i < 9 {
				c |= 0x80
			}
			b[off+i] = c
		}
	case wasmobj.R_TABLE_INDEX_SLEB64, wasmobj.R_MEMORY_ADDR_SLEB64:
		if err := put(10); err != nil {
			return err
		}
		sv := v
		for i := 0; i < 10; i++ {
			c := byte(sv & 0x7f)
			sv >>= 7
			if i < 9 {
				c |= 0x80
			}
			b[off+i] = c
		}
	case wasmobj.R_TABLE_INDEX_I32, wasmobj.R_MEMORY_ADDR_I32, wasmobj.R_GLOBAL_INDEX_I32, wasmobj.R_FUNCTION_INDEX_I32:
		if err := put(4); err != nil {
			return err
		}
		binary.LittleEndian.PutUint32(b[off:], uint32(v))
	case wasmobj.R_TABLE_INDEX_I64, wasmobj.R_MEMORY_ADDR_I64:
		if err := put(8); err != nil {
			return err
		}
		binary.LittleEndian.PutUint64(b[off:], uint64(v))
	default:
		return fmt.Errorf("unsupported relocation type %d", typ)
	}
	return nil
}

// extraBody is the body of a function the linker writes.
func (c *cgoLink) extraBody(e *cgoExtra, idx funcIndexer) []byte {
	b := new(bytes.Buffer)
	writeUleb128(b, 0) // no locals
	if e.name == "__wasm_call_ctors" {
		for _, s := range e.ctors {
			r := &c.res[s]
			b.WriteByte(0x10) // call
			switch r.kind {
			case resCFunc:
				writeUleb128(b, uint64(idx.cfunc(r.k)))
			default:
				ld.Exitf("cgo: constructor %s isn't a C function", c.f.Symbols[s].Name)
			}
		}
	} else if e.name != "__wasm_init_tls" {
		b.WriteByte(0x00) // unreachable
	}
	b.WriteByte(0x0b) // end
	return b.Bytes()
}
