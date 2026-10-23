// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package wasm

import (
	"bytes"
	"cmd/internal/obj"
	"cmd/internal/obj/wasm"
	"cmd/internal/objabi"
	"cmd/internal/wasmobj"
	"cmd/link/internal/ld"
	"cmd/link/internal/loader"
	"cmd/link/internal/sym"
	"fmt"
	"internal/abi"
	"internal/buildcfg"
	"io"
	"regexp"
)

const (
	I32 = 0x7F
	I64 = 0x7E
	F32 = 0x7D
	F64 = 0x7C
)

const (
	sectionCustom    = 0
	sectionType      = 1
	sectionImport    = 2
	sectionFunction  = 3
	sectionTable     = 4
	sectionMemory    = 5
	sectionGlobal    = 6
	sectionExport    = 7
	sectionStart     = 8
	sectionElement   = 9
	sectionCode      = 10
	sectionData      = 11
	sectionDataCount = 12
	sectionTag       = 13
)

const wasmPageSize = 64 << 10 // 64KB

// funcValueOffset is the offset between the PC_F value of a function and the index of the function in WebAssembly
const funcValueOffset = 0x1000 // TODO(neelance): make function addresses play nice with heap addresses

func gentext(ctxt *ld.Link, ldr *loader.Loader) {
}

type wasmFunc struct {
	Module string
	Name   string
	Type   uint32
	Code   []byte
}

type wasmFuncType struct {
	Params  []byte
	Results []byte
}

func readWasmImport(ldr *loader.Loader, s loader.Sym) obj.WasmImport {
	var wi obj.WasmImport
	wi.Read(ldr.Data(s))
	return wi
}

var wasmFuncTypes = map[string]*wasmFuncType{
	"_rt0_wasm_js":            {Params: []byte{}},                                         //
	"_rt0_wasm_wasip1":        {Params: []byte{}},                                         //
	"_rt0_wasm_wasip1_lib":    {Params: []byte{}},                                         //
	"_rt0_wasm_linux":         {Params: []byte{}},                                         //
	"wasm_vfork_child":        {Params: []byte{I32}, Results: []byte{I32}},                // the parent's g and SP
	"wasm_export__start":      {},                                                         //
	"wasm_export_run":         {Params: []byte{I32, I32}},                                 // argc, argv
	"wasm_export_resume":      {Params: []byte{}},                                         //
	"wasm_export_getsp":       {Results: []byte{I32}},                                     // sp
	"wasm_pc_f_loop":          {Params: []byte{}},                                         //
	"wasm_pc_f_loop_export":   {Params: []byte{I32}},                                      // pc_f
	"runtime.wasmDiv":         {Params: []byte{I64, I64}, Results: []byte{I64}},           // x, y -> x/y
	"runtime.wasmTruncS":      {Params: []byte{F64}, Results: []byte{I64}},                // x -> int(x)
	"runtime.wasmTruncU":      {Params: []byte{F64}, Results: []byte{I64}},                // x -> uint(x)
	"gcWriteBarrier":          {Params: []byte{I64}, Results: []byte{I64}},                // #bytes -> bufptr
	"runtime.gcWriteBarrier1": {Results: []byte{I64}},                                     // -> bufptr
	"runtime.gcWriteBarrier2": {Results: []byte{I64}},                                     // -> bufptr
	"runtime.gcWriteBarrier3": {Results: []byte{I64}},                                     // -> bufptr
	"runtime.gcWriteBarrier4": {Results: []byte{I64}},                                     // -> bufptr
	"runtime.gcWriteBarrier5": {Results: []byte{I64}},                                     // -> bufptr
	"runtime.gcWriteBarrier6": {Results: []byte{I64}},                                     // -> bufptr
	"runtime.gcWriteBarrier7": {Results: []byte{I64}},                                     // -> bufptr
	"runtime.gcWriteBarrier8": {Results: []byte{I64}},                                     // -> bufptr
	"runtime.notInitialized":  {},                                                         //
	"cmpbody":                 {Params: []byte{I64, I64, I64, I64}, Results: []byte{I64}}, // a, alen, b, blen -> -1/0/1
	"memeqbody":               {Params: []byte{I64, I64, I64}, Results: []byte{I64}},      // a, b, len -> 0/1
	"memcmp":                  {Params: []byte{I32, I32, I32}, Results: []byte{I32}},      // a, b, len -> <0/0/>0
	"memchr":                  {Params: []byte{I32, I32, I32}, Results: []byte{I32}},      // s, c, len -> index
}

func assignAddress(ldr *loader.Loader, sect *sym.Section, n int, s loader.Sym, va uint64, isTramp bool) (*sym.Section, int, uint64) {
	// WebAssembly functions do not live in the same address space as the linear memory.
	// Instead, WebAssembly automatically assigns indices. Imported functions (section "import")
	// have indices 0 to n. They are followed by native functions (sections "function" and "code")
	// with indices n+1 and following.
	//
	// The following rules describe how wasm handles function indices and addresses:
	//   PC_F = funcValueOffset + WebAssembly function index (not including the imports)
	//   s.Value = PC = PC_F<<16 + PC_B
	//
	// The funcValueOffset is necessary to avoid conflicts with expectations
	// that the Go runtime has about function addresses.
	// The field "s.Value" corresponds to the concept of PC at runtime.
	// However, there is no PC register, only PC_F and PC_B. PC_F denotes the function,
	// PC_B the resume point inside of that function. The entry of the function has PC_B = 0.
	ldr.SetSymSect(s, sect)
	ldr.SetSymValue(s, int64(funcValueOffset+va/abi.MINFUNC)<<16) // va starts at zero
	va += uint64(abi.MINFUNC)
	return sect, n, va
}

type wasmDataSect struct {
	sect *sym.Section
	data []byte
}

var dataSects []wasmDataSect

func asmb(ctxt *ld.Link, ldr *loader.Loader) {
	sections := []*sym.Section{
		ldr.SymSect(ldr.Lookup("runtime.rodata", 0)),
		ldr.SymSect(ldr.Lookup("runtime.typelink", 0)),
		ldr.SymSect(ldr.Lookup("runtime.itablink", 0)),
		ldr.SymSect(ldr.Lookup("runtime.firstmoduledata", 0)),
		ldr.SymSect(ldr.Lookup("runtime.pclntab", 0)),
		ldr.SymSect(ldr.Lookup("runtime.noptrdata", 0)),
		ldr.SymSect(ldr.Lookup("runtime.data", 0)),
	}

	if cgo != nil {
		cgo.setValues(ctxt, ldr)
	}

	dataSects = make([]wasmDataSect, len(sections))
	for i, sect := range sections {
		data := ld.DatblkBytes(ctxt, int64(sect.Vaddr), int64(sect.Length))
		dataSects[i] = wasmDataSect{sect, data}
	}
}

// asmb writes the final WebAssembly module binary.
// Spec: https://webassembly.github.io/spec/core/binary/modules.html
func asmb2(ctxt *ld.Link, ldr *loader.Loader) {
	types := []*wasmFuncType{
		// For normal Go functions, the single parameter is PC_B,
		// the return value is
		// 0 if the function returned normally or
		// 1 if the stack needs to be unwound.
		{Params: []byte{I32}, Results: []byte{I32}},
		// For the runtime's calls into C (linux/wasm's cgo: asmcgocall's
		// CallIndirect $1), int (*)(void *); the same, as it happens.
		cgoType,
	}

	// collect host imports (functions that get imported from the WebAssembly host, usually JavaScript)
	// we store the import index of each imported function, so the R_WASMIMPORT relocation
	// can write the correct index after a "call" instruction
	// these are added as import statements to the top of the WebAssembly binary
	var hostImports []*wasmFunc
	hostImportMap := make(map[loader.Sym]int64)
	for _, fn := range ctxt.Textp {
		relocs := ldr.Relocs(fn)
		for ri := 0; ri < relocs.Count(); ri++ {
			r := relocs.At(ri)
			if r.Type() == objabi.R_WASMIMPORT {
				if wsym := ldr.WasmImportSym(fn); wsym != 0 {
					wi := readWasmImport(ldr, wsym)
					hostImportMap[fn] = int64(len(hostImports))
					hostImports = append(hostImports, &wasmFunc{
						Module: wi.Module,
						Name:   wi.Name,
						Type: lookupType(&wasmFuncType{
							Params:  fieldsToTypes(wi.Params),
							Results: fieldsToTypes(wi.Results),
						}, &types),
					})
				} else {
					panic(fmt.Sprintf("missing wasm symbol for %s", ldr.SymName(r.Sym())))
				}
			}
		}
	}

	// C's imports (cgo.go), after Go's: one for each, as Go may import the
	// same (linux.syscall)
	var cgoImportIdx []uint32
	if cgo != nil {
		for _, ci := range cgo.imports {
			typ := lookupType(&wasmFuncType{Params: ci.typ.Params, Results: ci.typ.Results}, &types)
			idx := -1
			for i, h := range hostImports {
				if h.Module == ci.module && h.Name == ci.name && h.Type == typ {
					idx = i
				}
			}
			if idx < 0 {
				idx = len(hostImports)
				hostImports = append(hostImports, &wasmFunc{Module: ci.module, Name: ci.name, Type: typ})
			}
			cgoImportIdx = append(cgoImportIdx, uint32(idx))
		}
	}

	// collect functions with WebAssembly body
	var buildid []byte
	fns := make([]*wasmFunc, len(ctxt.Textp))
	for i, fn := range ctxt.Textp {
		wfn := new(bytes.Buffer)
		if ldr.SymName(fn) == "go:buildid" {
			writeUleb128(wfn, 0) // number of sets of locals
			writeI32Const(wfn, 0)
			wfn.WriteByte(0x0b) // end
			buildid = ldr.Data(fn)
		} else {
			// Relocations have variable length, handle them here.
			relocs := ldr.Relocs(fn)
			P := ldr.Data(fn)
			off := int32(0)
			for ri := 0; ri < relocs.Count(); ri++ {
				r := relocs.At(ri)
				if r.Siz() == 0 {
					continue // skip marker relocations
				}
				wfn.Write(P[off:r.Off()])
				off = r.Off()
				rs := r.Sym()
				switch r.Type() {
				case objabi.R_ADDR:
					writeSleb128(wfn, ldr.SymValue(rs)+r.Add())
				case objabi.R_CALL:
					writeSleb128(wfn, int64(len(hostImports))+ldr.SymValue(rs)>>16-funcValueOffset)
				case objabi.R_WASMIMPORT:
					writeSleb128(wfn, hostImportMap[rs])
				default:
					ldr.Errorf(fn, "bad reloc type %d (%s)", r.Type(), sym.RelocName(ctxt.Arch, r.Type()))
					continue
				}
			}
			wfn.Write(P[off:])
		}

		typ := uint32(0)
		if sig, ok := wasmFuncTypes[ldr.SymName(fn)]; ok {
			typ = lookupType(sig, &types)
		}
		if s := ldr.WasmTypeSym(fn); s != 0 {
			var o obj.WasmFuncType
			o.Read(ldr.Data(s))
			t := &wasmFuncType{
				Params:  fieldsToTypes(o.Params),
				Results: fieldsToTypes(o.Results),
			}
			typ = lookupType(t, &types)
		}

		name := nameRegexp.ReplaceAllString(ldr.SymName(fn), "_")
		fns[i] = &wasmFunc{Name: name, Type: typ, Code: wfn.Bytes()}
	}

	// C's functions (cgo.go), then the linker's for C
	if cgo != nil {
		nGo, nC := uint32(len(fns)), uint32(len(cgo.f.Funcs))
		base := uint32(len(hostImports))
		idx := funcIndexer{
			cfunc:  func(k uint32) uint32 { return base + nGo + k },
			cimp:   func(j uint32) uint32 { return cgoImportIdx[j] },
			extra:  func(j uint32) uint32 { return base + nGo + nC + j },
			gofunc: func(s loader.Sym) uint32 { return base + uint32(ldr.SymValue(s)>>16) - funcValueOffset },
		}
		typeIdx := func(t wasmobj.FuncType) uint32 {
			return lookupType(&wasmFuncType{Params: t.Params, Results: t.Results}, &types)
		}
		for k := range cgo.f.Funcs {
			body, err := cgo.relocate(ldr, uint32(k), idx, typeIdx)
			if err != nil {
				ld.Exitf("cgo: %v", err)
			}
			fns = append(fns, &wasmFunc{Name: nameRegexp.ReplaceAllString(cgo.funcName(uint32(k)), "_"), Type: typeIdx(cgo.f.Types[cgo.f.Funcs[k].Type]), Code: body})
		}
		for _, e := range cgo.extra {
			fns = append(fns, &wasmFunc{Name: e.name, Type: typeIdx(e.typ), Code: cgo.extraBody(e, idx)})
		}
	}

	segments := dataSegments()
	startIdx := -1 // linux: _start's function index
	if buildcfg.GOOS == "linux" {
		startIdx = len(hostImports) + len(fns)
		fns = append(fns, linuxStartFunc(ldr, len(hostImports), segments, lookupType(&wasmFuncType{}, &types)))
	}

	ctxt.Out.Write([]byte{0x00, 0x61, 0x73, 0x6d}) // magic
	ctxt.Out.Write([]byte{0x01, 0x00, 0x00, 0x00}) // version

	// Add any buildid early in the binary:
	if len(buildid) != 0 {
		writeBuildID(ctxt, buildid)
	}

	writeTypeSec(ctxt, types)
	writeImportSec(ctxt, ldr, hostImports)
	writeFunctionSec(ctxt, fns)
	writeTableSec(ctxt, fns)
	if buildcfg.GOOS != "linux" { // (linux: imported)
		writeMemorySec(ctxt, ldr)
	}
	if cgo != nil && len(cgo.tags) > 0 {
		writeTagSec(ctxt, cgo.tags, func(t uint32) uint32 {
			ft := cgo.f.Types[t]
			return lookupType(&wasmFuncType{Params: ft.Params, Results: ft.Results}, &types)
		})
	}
	writeGlobalSec(ctxt, ldr)
	writeExportSec(ctxt, ldr, len(hostImports), startIdx)
	writeElementSec(ctxt, uint64(len(hostImports)), uint64(len(fns)))
	if buildcfg.GOOS == "linux" {
		writeDataCountSec(ctxt, len(segments))
	}
	writeCodeSec(ctxt, fns)
	writeDataSec(ctxt, segments)
	writeProducerSec(ctxt)
	if buildcfg.GOOS == "linux" {
		writeLibcSec(ctxt)
	}
	if !*ld.FlagS {
		writeNameSec(ctxt, len(hostImports), fns)
	}
}

func lookupType(sig *wasmFuncType, types *[]*wasmFuncType) uint32 {
	for i, t := range *types {
		if bytes.Equal(sig.Params, t.Params) && bytes.Equal(sig.Results, t.Results) {
			return uint32(i)
		}
	}
	*types = append(*types, sig)
	return uint32(len(*types) - 1)
}

func writeSecHeader(ctxt *ld.Link, id uint8) int64 {
	ctxt.Out.WriteByte(id)
	sizeOffset := ctxt.Out.Offset()
	ctxt.Out.Write(make([]byte, 5)) // placeholder for length
	return sizeOffset
}

func writeSecSize(ctxt *ld.Link, sizeOffset int64) {
	endOffset := ctxt.Out.Offset()
	ctxt.Out.SeekSet(sizeOffset)
	writeUleb128FixedLength(ctxt.Out, uint64(endOffset-sizeOffset-5), 5)
	ctxt.Out.SeekSet(endOffset)
}

func writeBuildID(ctxt *ld.Link, buildid []byte) {
	sizeOffset := writeSecHeader(ctxt, sectionCustom)
	writeName(ctxt.Out, "go:buildid")
	ctxt.Out.Write(buildid)
	writeSecSize(ctxt, sizeOffset)
}

// writeTypeSec writes the section that declares all function types
// so they can be referenced by index.
func writeTypeSec(ctxt *ld.Link, types []*wasmFuncType) {
	sizeOffset := writeSecHeader(ctxt, sectionType)

	writeUleb128(ctxt.Out, uint64(len(types)))

	for _, t := range types {
		ctxt.Out.WriteByte(0x60) // functype
		writeUleb128(ctxt.Out, uint64(len(t.Params)))
		for _, v := range t.Params {
			ctxt.Out.WriteByte(v)
		}
		writeUleb128(ctxt.Out, uint64(len(t.Results)))
		for _, v := range t.Results {
			ctxt.Out.WriteByte(v)
		}
	}

	writeSecSize(ctxt, sizeOffset)
}

// writeImportSec writes the section that lists the functions that get
// imported from the WebAssembly host, usually JavaScript.
func writeImportSec(ctxt *ld.Link, ldr *loader.Loader, hostImports []*wasmFunc) {
	sizeOffset := writeSecHeader(ctxt, sectionImport)

	numImports := len(hostImports)
	if buildcfg.GOOS == "linux" {
		numImports++ // env.memory
	}
	writeUleb128(ctxt.Out, uint64(numImports)) // number of imports
	for _, fn := range hostImports {
		if fn.Module != "" {
			writeName(ctxt.Out, fn.Module)
		} else {
			writeName(ctxt.Out, wasm.GojsModule) // provided by the import object in wasm_exec.js
		}
		writeName(ctxt.Out, fn.Name)
		ctxt.Out.WriteByte(0x00) // func import
		writeUleb128(ctxt.Out, uint64(fn.Type))
	}
	if buildcfg.GOOS == "linux" {
		// linux-workers: the process's memory is the host's (as musl's
		// programs import it): shared, so that its threads can share it,
		// and with a maximum, which the host reserves (all of wasm32's).
		writeName(ctxt.Out, "env")
		writeName(ctxt.Out, "memory")
		ctxt.Out.WriteByte(0x02)                        // mem import
		ctxt.Out.WriteByte(0x03)                        // shared, with a maximum
		writeUleb128(ctxt.Out, initialMemoryPages(ldr)) // minimum (initial) memory size
		writeUleb128(ctxt.Out, 1<<16)                   // maximum: 4 GiB
	}

	writeSecSize(ctxt, sizeOffset)
}

// writeFunctionSec writes the section that declares the types of functions.
// The bodies of these functions will later be provided in the "code" section.
func writeFunctionSec(ctxt *ld.Link, fns []*wasmFunc) {
	sizeOffset := writeSecHeader(ctxt, sectionFunction)

	writeUleb128(ctxt.Out, uint64(len(fns)))
	for _, fn := range fns {
		writeUleb128(ctxt.Out, uint64(fn.Type))
	}

	writeSecSize(ctxt, sizeOffset)
}

// writeTableSec writes the section that declares tables. Currently there is only a single table
// that is used by the CallIndirect operation to dynamically call any function.
// The contents of the table get initialized by the "element" section.
func writeTableSec(ctxt *ld.Link, fns []*wasmFunc) {
	sizeOffset := writeSecHeader(ctxt, sectionTable)

	numElements := uint64(funcValueOffset + len(fns))
	writeUleb128(ctxt.Out, 1)           // number of tables
	ctxt.Out.WriteByte(0x70)            // type: anyfunc
	ctxt.Out.WriteByte(0x00)            // no max
	writeUleb128(ctxt.Out, numElements) // min

	writeSecSize(ctxt, sizeOffset)
}

// writeMemorySec writes the section that declares linear memories. Currently one linear memory is being used.
// Linear memory always starts at address zero. More memory can be requested with the GrowMemory instruction.
func writeMemorySec(ctxt *ld.Link, ldr *loader.Loader) {
	sizeOffset := writeSecHeader(ctxt, sectionMemory)

	writeUleb128(ctxt.Out, 1)                       // number of memories
	ctxt.Out.WriteByte(0x00)                        // no maximum memory size
	writeUleb128(ctxt.Out, initialMemoryPages(ldr)) // minimum (initial) memory size

	writeSecSize(ctxt, sizeOffset)
}

// initialMemoryPages is the memory's initial size, in wasm pages.
func initialMemoryPages(ldr *loader.Loader) uint64 {
	dataEnd := uint64(ldr.SymValue(ldr.Lookup("runtime.end", 0)))
	var initialSize = dataEnd + 1<<20 // 1 MB, for runtime init allocating a few pages

	return initialSize / wasmPageSize
}

// writeTagSec writes the section that declares exception tags: C's
// (setjmp/longjmp's), of their types.
func writeTagSec(ctxt *ld.Link, tags []uint32, typeIdx func(uint32) uint32) {
	sizeOffset := writeSecHeader(ctxt, sectionTag)
	writeUleb128(ctxt.Out, uint64(len(tags)))
	for _, t := range tags {
		ctxt.Out.WriteByte(0x00) // attribute: exception
		writeUleb128(ctxt.Out, uint64(typeIdx(t)))
	}
	writeSecSize(ctxt, sizeOffset)
}

// writeGlobalSec writes the section that declares global variables.
func writeGlobalSec(ctxt *ld.Link, ldr *loader.Loader) {
	sizeOffset := writeSecHeader(ctxt, sectionGlobal)

	globalRegs := []byte{
		I32, // 0: SP
		I64, // 1: CTXT
		I64, // 2: g
		I64, // 3: RET0
		I64, // 4: RET1
		I64, // 5: RET2
		I64, // 6: RET3
		I32, // 7: PAUSE
	}

	n := len(globalRegs)
	if cgo != nil {
		n += cgoGlobalC - len(globalRegs) + len(cgo.globals) // C's stack pointer (cgoGlobalSP), TLS's, C's
	}
	writeUleb128(ctxt.Out, uint64(n)) // number of globals

	for _, typ := range globalRegs {
		ctxt.Out.WriteByte(typ)
		ctxt.Out.WriteByte(0x01) // var
		switch typ {
		case I32:
			writeI32Const(ctxt.Out, 0)
		case I64:
			writeI64Const(ctxt.Out, 0)
		}
		ctxt.Out.WriteByte(0x0b) // end
	}
	if cgo != nil {
		// C's stack pointer: its stack's top
		ctxt.Out.WriteByte(I32)
		ctxt.Out.WriteByte(0x01) // var
		writeI32Const(ctxt.Out, int32(ldr.SymValue(cgo.stack)+cgoStackSize))
		ctxt.Out.WriteByte(0x0b) // end
		for _, g := range []struct {
			mut byte
			v   int32
		}{{0x01, 0}, {0x00, 0}, {0x00, 1}} { // __tls_base, __tls_size, __tls_align
			ctxt.Out.WriteByte(I32)
			ctxt.Out.WriteByte(g.mut)
			writeI32Const(ctxt.Out, g.v)
			ctxt.Out.WriteByte(0x0b) // end
		}
		for _, g := range cgo.globals {
			ctxt.Out.WriteByte(g.ValType)
			if g.Mutable {
				ctxt.Out.WriteByte(0x01)
			} else {
				ctxt.Out.WriteByte(0x00)
			}
			ctxt.Out.Write(g.Init)
		}
	}

	writeSecSize(ctxt, sizeOffset)
}

// writeExportSec writes the section that declares exports.
// Exports can be accessed by the WebAssembly host, usually JavaScript.
// The wasm_export_* functions and the linear memory get exported.
func writeExportSec(ctxt *ld.Link, ldr *loader.Loader, lenHostImports int, startIdx int) {
	sizeOffset := writeSecHeader(ctxt, sectionExport)

	switch buildcfg.GOOS {
	case "wasip1":
		writeUleb128(ctxt.Out, uint64(2+len(ldr.WasmExports))) // number of exports
		var entry, entryExpName string
		switch ctxt.BuildMode {
		case ld.BuildModeExe:
			entry = "_rt0_wasm_wasip1"
			entryExpName = "_start"
		case ld.BuildModeCShared:
			entry = "_rt0_wasm_wasip1_lib"
			entryExpName = "_initialize"
		}
		s := ldr.Lookup(entry, 0)
		if s == 0 {
			ld.Errorf("export symbol %s not defined", entry)
		}
		idx := uint32(lenHostImports) + uint32(ldr.SymValue(s)>>16) - funcValueOffset
		writeName(ctxt.Out, entryExpName)   // the wasi entrypoint
		ctxt.Out.WriteByte(0x00)            // func export
		writeUleb128(ctxt.Out, uint64(idx)) // funcidx
		for _, s := range ldr.WasmExports {
			idx := uint32(lenHostImports) + uint32(ldr.SymValue(s)>>16) - funcValueOffset
			writeName(ctxt.Out, ldr.SymName(s))
			ctxt.Out.WriteByte(0x00)            // func export
			writeUleb128(ctxt.Out, uint64(idx)) // funcidx
		}
		writeName(ctxt.Out, "memory") // memory in wasi
		ctxt.Out.WriteByte(0x02)      // mem export
		writeUleb128(ctxt.Out, 0)     // memidx
	case "linux":
		// linux-workers: a process of tombl's arch/wasm, as musl's: _start
		// (binfmt_wasm's entry), and the table the kernel calls functions
		// of the program's through (signal handlers, a thread's start)
		writeUleb128(ctxt.Out, uint64(2+len(ldr.WasmExports))) // number of exports
		writeName(ctxt.Out, "_start")
		ctxt.Out.WriteByte(0x00)                 // func export
		writeUleb128(ctxt.Out, uint64(startIdx)) // funcidx: linuxStartFunc
		for _, s := range ldr.WasmExports {
			idx := uint32(lenHostImports) + uint32(ldr.SymValue(s)>>16) - funcValueOffset
			writeName(ctxt.Out, ldr.SymName(s))
			ctxt.Out.WriteByte(0x00)            // func export
			writeUleb128(ctxt.Out, uint64(idx)) // funcidx
		}
		writeName(ctxt.Out, "__indirect_function_table")
		ctxt.Out.WriteByte(0x01)  // table export
		writeUleb128(ctxt.Out, 0) // tableidx
	case "js":
		writeUleb128(ctxt.Out, uint64(4+len(ldr.WasmExports))) // number of exports
		for _, name := range []string{"run", "resume", "getsp"} {
			s := ldr.Lookup("wasm_export_"+name, 0)
			if s == 0 {
				ld.Errorf("export symbol %s not defined", "wasm_export_"+name)
			}
			idx := uint32(lenHostImports) + uint32(ldr.SymValue(s)>>16) - funcValueOffset
			writeName(ctxt.Out, name)           // inst.exports.run/resume/getsp in wasm_exec.js
			ctxt.Out.WriteByte(0x00)            // func export
			writeUleb128(ctxt.Out, uint64(idx)) // funcidx
		}
		for _, s := range ldr.WasmExports {
			idx := uint32(lenHostImports) + uint32(ldr.SymValue(s)>>16) - funcValueOffset
			writeName(ctxt.Out, ldr.SymName(s))
			ctxt.Out.WriteByte(0x00)            // func export
			writeUleb128(ctxt.Out, uint64(idx)) // funcidx
		}
		writeName(ctxt.Out, "mem") // inst.exports.mem in wasm_exec.js
		ctxt.Out.WriteByte(0x02)   // mem export
		writeUleb128(ctxt.Out, 0)  // memidx
	default:
		ld.Exitf("internal error: writeExportSec: unrecognized GOOS %s", buildcfg.GOOS)
	}

	writeSecSize(ctxt, sizeOffset)
}

// writeElementSec writes the section that initializes the tables declared by the "table" section.
// The table for CallIndirect gets initialized in a very simple way so that each table index (PC_F value)
// maps linearly to the function index (numImports + PC_F).
func writeElementSec(ctxt *ld.Link, numImports, numFns uint64) {
	sizeOffset := writeSecHeader(ctxt, sectionElement)

	writeUleb128(ctxt.Out, 1) // number of element segments

	writeUleb128(ctxt.Out, 0) // tableidx
	writeI32Const(ctxt.Out, funcValueOffset)
	ctxt.Out.WriteByte(0x0b) // end

	writeUleb128(ctxt.Out, numFns) // number of entries
	for i := uint64(0); i < numFns; i++ {
		writeUleb128(ctxt.Out, numImports+i)
	}

	writeSecSize(ctxt, sizeOffset)
}

// writeCodeSec writes the section that provides the function bodies for the functions
// declared by the "func" section.
func writeCodeSec(ctxt *ld.Link, fns []*wasmFunc) {
	sizeOffset := writeSecHeader(ctxt, sectionCode)

	writeUleb128(ctxt.Out, uint64(len(fns))) // number of code entries
	for _, fn := range fns {
		writeUleb128(ctxt.Out, uint64(len(fn.Code)))
		ctxt.Out.Write(fn.Code)
	}

	writeSecSize(ctxt, sizeOffset)
}

type dataSegment struct {
	offset int32
	data   []byte
}

// dataSegments returns the data segments that initialize the linear memory.
func dataSegments() []*dataSegment {

	// Omit blocks of zeroes and instead emit data segments with offsets skipping the zeroes.
	// This reduces the size of the WebAssembly binary. We use 8 bytes as an estimate for the
	// overhead of adding a new segment (same as wasm-opt's memory-packing optimization uses).
	segmentOverhead := int32(8)
	if buildcfg.GOOS == "linux" {
		// (passive: and _start's memory.init and data.drop, about as much again)
		segmentOverhead = 24
	}

	// Generate at most this many segments. A higher number of segments gets rejected by some WebAssembly runtimes.
	const maxNumSegments = 100000

	var segments []*dataSegment
	for secIndex, ds := range dataSects {
		data := ds.data
		offset := int32(ds.sect.Vaddr)

		// skip leading zeroes
		for len(data) > 0 && data[0] == 0 {
			data = data[1:]
			offset++
		}

		for len(data) > 0 {
			dataLen := int32(len(data))
			var segmentEnd, zeroEnd int32
			if len(segments)+(len(dataSects)-secIndex) == maxNumSegments {
				segmentEnd = dataLen
				zeroEnd = dataLen
			} else {
				for {
					// look for beginning of zeroes
					for segmentEnd < dataLen && data[segmentEnd] != 0 {
						segmentEnd++
					}
					// look for end of zeroes
					zeroEnd = segmentEnd
					for zeroEnd < dataLen && data[zeroEnd] == 0 {
						zeroEnd++
					}
					// emit segment if omitting zeroes reduces the output size
					if zeroEnd-segmentEnd >= segmentOverhead || zeroEnd == dataLen {
						break
					}
					segmentEnd = zeroEnd
				}
			}

			segments = append(segments, &dataSegment{
				offset: offset,
				data:   data[:segmentEnd],
			})
			data = data[zeroEnd:]
			offset += zeroEnd
		}
	}

	return segments
}

// writeDataSec writes the section that provides data that will be used to initialize the linear memory.
func writeDataSec(ctxt *ld.Link, segments []*dataSegment) {
	sizeOffset := writeSecHeader(ctxt, sectionData)

	writeUleb128(ctxt.Out, uint64(len(segments))) // number of data entries
	for _, seg := range segments {
		if buildcfg.GOOS == "linux" {
			// passive: copied in by _start (linuxStartFunc), not by each
			// instance (a thread's, a vfork child's) of the shared memory
			writeUleb128(ctxt.Out, 1)
		} else {
			writeUleb128(ctxt.Out, 0) // memidx
			writeI32Const(ctxt.Out, seg.offset)
			ctxt.Out.WriteByte(0x0b) // end
		}
		writeUleb128(ctxt.Out, uint64(len(seg.data)))
		ctxt.Out.Write(seg.data)
	}

	writeSecSize(ctxt, sizeOffset)
}

// writeDataCountSec writes the number of data segments, which code that
// refers to them (memory.init, data.drop) needs declared before it.
func writeDataCountSec(ctxt *ld.Link, n int) {
	sizeOffset := writeSecHeader(ctxt, sectionDataCount)
	writeUleb128(ctxt.Out, uint64(n))
	writeSecSize(ctxt, sizeOffset)
}

// linuxStartFunc is linux/wasm's _start: the data segments copied into the
// memory (which is shared: other instances of the program, a thread's or a
// vfork child's, leave it as it is), then the runtime's entry.
func linuxStartFunc(ldr *loader.Loader, lenHostImports int, segments []*dataSegment, typ uint32) *wasmFunc {
	b := new(bytes.Buffer)
	writeUleb128(b, 0) // no locals
	for i, seg := range segments {
		writeI32Const(b, seg.offset)
		writeI32Const(b, 0)
		writeI32Const(b, int32(len(seg.data)))
		b.Write([]byte{0xfc, 0x08}) // memory.init
		writeUleb128(b, uint64(i))
		b.WriteByte(0x00)           // memidx
		b.Write([]byte{0xfc, 0x09}) // data.drop
		writeUleb128(b, uint64(i))
	}
	s := ldr.Lookup("_rt0_wasm_linux", 0)
	if s == 0 {
		ld.Errorf("export symbol _rt0_wasm_linux not defined")
	}
	b.WriteByte(0x10) // call
	writeUleb128(b, uint64(uint32(lenHostImports)+uint32(ldr.SymValue(s)>>16)-funcValueOffset))
	b.WriteByte(0x0b) // end
	return &wasmFunc{Name: "_start", Type: typ, Code: b.Bytes()}
}

// writeProducerSec writes an optional section that reports the source language and compiler version.
func writeProducerSec(ctxt *ld.Link) {
	sizeOffset := writeSecHeader(ctxt, sectionCustom)
	writeName(ctxt.Out, "producers")

	writeUleb128(ctxt.Out, 2) // number of fields

	writeName(ctxt.Out, "language")       // field name
	writeUleb128(ctxt.Out, 1)             // number of values
	writeName(ctxt.Out, "Go")             // value: name
	writeName(ctxt.Out, buildcfg.Version) // value: version

	writeName(ctxt.Out, "processed-by")   // field name
	writeUleb128(ctxt.Out, 1)             // number of values
	writeName(ctxt.Out, "Go cmd/compile") // value: name
	writeName(ctxt.Out, buildcfg.Version) // value: version

	writeSecSize(ctxt, sizeOffset)
}

// writeLibcSec writes the version of the contract between linux/wasm
// programs and the host (its imports, exports and their behavior), which
// the host checks at exec: musl's crt1.c writes the same.
func writeLibcSec(ctxt *ld.Link) {
	sizeOffset := writeSecHeader(ctxt, sectionCustom)
	writeName(ctxt.Out, "linux.libc")
	ctxt.Out.Write([]byte("1"))
	writeSecSize(ctxt, sizeOffset)
}

var nameRegexp = regexp.MustCompile(`[^\w.]`)

// writeNameSec writes an optional section that assigns names to the functions declared by the "func" section.
// The names are only used by WebAssembly stack traces, debuggers and decompilers.
// TODO(neelance): add symbol table of DATA symbols
func writeNameSec(ctxt *ld.Link, firstFnIndex int, fns []*wasmFunc) {
	sizeOffset := writeSecHeader(ctxt, sectionCustom)
	writeName(ctxt.Out, "name")

	sizeOffset2 := writeSecHeader(ctxt, 0x01) // function names
	writeUleb128(ctxt.Out, uint64(len(fns)))
	for i, fn := range fns {
		writeUleb128(ctxt.Out, uint64(firstFnIndex+i))
		writeName(ctxt.Out, fn.Name)
	}
	writeSecSize(ctxt, sizeOffset2)

	writeSecSize(ctxt, sizeOffset)
}

type nameWriter interface {
	io.ByteWriter
	io.Writer
}

func writeI32Const(w io.ByteWriter, v int32) {
	w.WriteByte(0x41) // i32.const
	writeSleb128(w, int64(v))
}

func writeI64Const(w io.ByteWriter, v int64) {
	w.WriteByte(0x42) // i64.const
	writeSleb128(w, v)
}

func writeName(w nameWriter, name string) {
	writeUleb128(w, uint64(len(name)))
	w.Write([]byte(name))
}

func writeUleb128(w io.ByteWriter, v uint64) {
	if v < 128 {
		w.WriteByte(uint8(v))
		return
	}
	more := true
	for more {
		c := uint8(v & 0x7f)
		v >>= 7
		more = v != 0
		if more {
			c |= 0x80
		}
		w.WriteByte(c)
	}
}

func writeUleb128FixedLength(w io.ByteWriter, v uint64, length int) {
	for i := 0; i < length; i++ {
		c := uint8(v & 0x7f)
		v >>= 7
		if i < length-1 {
			c |= 0x80
		}
		w.WriteByte(c)
	}
	if v != 0 {
		panic("writeUleb128FixedLength: length too small")
	}
}

func writeSleb128(w io.ByteWriter, v int64) {
	more := true
	for more {
		c := uint8(v & 0x7f)
		s := uint8(v & 0x40)
		v >>= 7
		more = !((v == 0 && s == 0) || (v == -1 && s != 0))
		if more {
			c |= 0x80
		}
		w.WriteByte(c)
	}
}

func fieldsToTypes(fields []obj.WasmField) []byte {
	b := make([]byte, len(fields))
	for i, f := range fields {
		switch f.Type {
		case obj.WasmI32, obj.WasmPtr, obj.WasmBool:
			b[i] = I32
		case obj.WasmI64:
			b[i] = I64
		case obj.WasmF32:
			b[i] = F32
		case obj.WasmF64:
			b[i] = F64
		default:
			panic(fmt.Sprintf("fieldsToTypes: unknown field type: %d", f.Type))
		}
	}
	return b
}
