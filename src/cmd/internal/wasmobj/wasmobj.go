// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package wasmobj reads WebAssembly relocatable object files, as clang
// and wasm-ld -r write them (the "linking" and "reloc.*" custom sections
// of https://github.com/WebAssembly/tool-conventions/blob/main/Linking.md):
// what cgo needs of them (DWARF, data symbols' contents) and what
// cmd/link needs to link one into a linux/wasm module (cgo's C side).
package wasmobj

import (
	"bytes"
	"errors"
	"fmt"
)

// Section ids.
const (
	SecCustom    = 0
	SecType      = 1
	SecImport    = 2
	SecFunction  = 3
	SecTable     = 4
	SecMemory    = 5
	SecGlobal    = 6
	SecExport    = 7
	SecStart     = 8
	SecElem      = 9
	SecCode      = 10
	SecData      = 11
	SecDataCount = 12
	SecTag       = 13
)

// Import and export kinds.
const (
	KindFunc   = 0
	KindTable  = 1
	KindMemory = 2
	KindGlobal = 3
	KindTag    = 4
)

// Symbol kinds (the linking section's symbol table).
const (
	SymFunction = 0
	SymData     = 1
	SymGlobal   = 2
	SymSection  = 3
	SymTag      = 4
	SymTable    = 5
)

// Symbol flags.
const (
	FlagWeak         = 0x1
	FlagLocal        = 0x2
	FlagHidden       = 0x4
	FlagUndefined    = 0x10
	FlagExported     = 0x20
	FlagExplicitName = 0x40
	FlagNoStrip      = 0x80
	FlagTLS          = 0x100
	FlagAbsolute     = 0x200
)

// Segment flags.
const (
	SegStrings = 0x1
	SegTLS     = 0x2
	SegRetain  = 0x4
)

// Relocation types.
const (
	R_FUNCTION_INDEX_LEB     = 0
	R_TABLE_INDEX_SLEB       = 1
	R_TABLE_INDEX_I32        = 2
	R_MEMORY_ADDR_LEB        = 3
	R_MEMORY_ADDR_SLEB       = 4
	R_MEMORY_ADDR_I32        = 5
	R_TYPE_INDEX_LEB         = 6
	R_GLOBAL_INDEX_LEB       = 7
	R_FUNCTION_OFFSET_I32    = 8
	R_SECTION_OFFSET_I32     = 9
	R_TAG_INDEX_LEB          = 10
	R_MEMORY_ADDR_REL_SLEB   = 11
	R_TABLE_INDEX_REL_SLEB   = 12
	R_GLOBAL_INDEX_I32       = 13
	R_MEMORY_ADDR_LEB64      = 14
	R_MEMORY_ADDR_SLEB64     = 15
	R_MEMORY_ADDR_I64        = 16
	R_MEMORY_ADDR_REL_SLEB64 = 17
	R_TABLE_INDEX_SLEB64     = 18
	R_TABLE_INDEX_I64        = 19
	R_TABLE_NUMBER_LEB       = 20
	R_MEMORY_ADDR_TLS_SLEB   = 21
	R_FUNCTION_OFFSET_I64    = 22
	R_MEMORY_ADDR_LOCREL_I32 = 23
	R_TABLE_INDEX_REL_SLEB64 = 24
	R_MEMORY_ADDR_TLS_SLEB64 = 25
	R_FUNCTION_INDEX_I32     = 26
	numRelocTypes            = 27
)

func hasAddend(t uint8) bool {
	switch t {
	case R_MEMORY_ADDR_LEB, R_MEMORY_ADDR_SLEB, R_MEMORY_ADDR_I32,
		R_FUNCTION_OFFSET_I32, R_SECTION_OFFSET_I32,
		R_MEMORY_ADDR_REL_SLEB, R_MEMORY_ADDR_LEB64, R_MEMORY_ADDR_SLEB64,
		R_MEMORY_ADDR_I64, R_MEMORY_ADDR_REL_SLEB64, R_MEMORY_ADDR_TLS_SLEB,
		R_FUNCTION_OFFSET_I64, R_MEMORY_ADDR_LOCREL_I32, R_MEMORY_ADDR_TLS_SLEB64:
		return true
	}
	return false
}

// FuncType is a function type: its parameters' and results' value types.
type FuncType struct {
	Params, Results []byte
}

func (t FuncType) Equal(u FuncType) bool {
	return bytes.Equal(t.Params, u.Params) && bytes.Equal(t.Results, u.Results)
}

// Import is an import: a function's has its type; a global's, its value
// type and mutability; a tag's, its type.
type Import struct {
	Module, Name string
	Kind         byte
	Type         uint32 // function and tag: type index
	ValType      byte   // global
	Mutable      bool   // global
}

// Func is a defined function: its type and body (locals and code, as in
// the code section, without the size).
type Func struct {
	Type uint32
	Body []byte
	// Off is the body's offset in the code section's payload (where
	// relocations' offsets count from).
	Off uint32
}

// Global is a defined global.
type Global struct {
	ValType byte
	Mutable bool
	Init    []byte // the initializer expression, with its end
}

// Segment is a data segment.
type Segment struct {
	Name  string
	Align uint32 // log2
	Flags uint32
	Data  []byte
	// Off is the data's offset in the data section's payload.
	Off uint32
	// Passive: no offset expression (clang's TLS segments, in a -pthread build).
	Passive bool
}

// Symbol is an entry of the linking section's symbol table.
type Symbol struct {
	Kind  byte
	Flags uint32
	Name  string
	// Index: a function's, global's, tag's or table's index (imports
	// first); a section symbol's section index.
	Index uint32
	// A defined data symbol's segment, offset in it, and size.
	Segment, Offset, Size uint32
}

func (s *Symbol) Undefined() bool { return s.Flags&FlagUndefined != 0 }
func (s *Symbol) Local() bool     { return s.Flags&FlagLocal != 0 }
func (s *Symbol) Weak() bool      { return s.Flags&FlagWeak != 0 }

// InitFunc is a constructor: a function symbol, run in priority order.
type InitFunc struct {
	Priority uint32
	Symbol   uint32
}

// Reloc is a relocation: Offset is in its section's payload; Index is a
// symbol's (a type's, for R_TYPE_INDEX_LEB).
type Reloc struct {
	Type   uint8
	Offset uint32
	Index  uint32
	Addend int64
}

// Section is a section, by its id (and name, if custom).
type Section struct {
	ID      byte
	Name    string
	Payload []byte
}

// File is a relocatable object.
type File struct {
	Sections []Section
	Types    []FuncType
	Imports  []Import
	Funcs    []Func
	Globals  []Global
	Tags     []uint32 // defined tags' types
	Segments []Segment
	Symbols  []Symbol
	Init     []InitFunc
	// Relocs, by section index (into Sections).
	Relocs map[int][]Reloc

	// imports by kind, counted
	NumFuncImports, NumGlobalImports, NumTableImports, NumTagImports int

	codeSec, dataSec int
}

// Custom returns a custom section's payload, or nil.
func (f *File) Custom(name string) []byte {
	for _, s := range f.Sections {
		if s.ID == SecCustom && s.Name == name {
			return s.Payload
		}
	}
	return nil
}

// CodeRelocs and DataRelocs are the code and data sections' relocations.
func (f *File) CodeRelocs() []Reloc { return f.Relocs[f.codeSec] }
func (f *File) DataRelocs() []Reloc { return f.Relocs[f.dataSec] }

// IsObject reports whether b starts as a wasm module.
func IsObject(b []byte) bool {
	return len(b) >= 8 && string(b[:4]) == "\x00asm" && b[4] == 1 && b[5] == 0 && b[6] == 0 && b[7] == 0
}

type reader struct {
	b   []byte
	off int
	err error
}

func (r *reader) fail(format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf("wasm object: at 0x%x: %s", r.off, fmt.Sprintf(format, args...))
	}
	r.off = len(r.b)
}

func (r *reader) byte() byte {
	if r.off >= len(r.b) {
		r.fail("unexpected end")
		return 0
	}
	c := r.b[r.off]
	r.off++
	return c
}

func (r *reader) uleb() uint64 {
	var v uint64
	for shift := uint(0); ; shift += 7 {
		if shift >= 64 {
			r.fail("bad LEB128")
			return 0
		}
		c := r.byte()
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return v
		}
	}
}

func (r *reader) u32() uint32 {
	v := r.uleb()
	if v > 0xffffffff {
		r.fail("LEB128 too big")
	}
	return uint32(v)
}

func (r *reader) sleb() int64 {
	var v int64
	var shift uint
	var c byte
	for {
		if shift >= 64 {
			r.fail("bad LEB128")
			return 0
		}
		c = r.byte()
		v |= int64(c&0x7f) << shift
		shift += 7
		if c&0x80 == 0 {
			break
		}
	}
	if shift < 64 && c&0x40 != 0 {
		v |= -1 << shift
	}
	return v
}

func (r *reader) bytes(n uint32) []byte {
	if uint64(r.off)+uint64(n) > uint64(len(r.b)) {
		r.fail("unexpected end")
		return nil
	}
	b := r.b[r.off : r.off+int(n)]
	r.off += int(n)
	return b
}

func (r *reader) name() string { return string(r.bytes(r.u32())) }

func (r *reader) limits() {
	flags := r.byte()
	r.uleb()
	if flags&1 != 0 {
		r.uleb()
	}
}

// expr skips a constant expression, returning it with its end.
func (r *reader) expr() []byte {
	start := r.off
	for r.err == nil {
		switch op := r.byte(); op {
		case 0x0b: // end
			return r.b[start:r.off]
		case 0x41: // i32.const
			r.sleb()
		case 0x42: // i64.const
			r.sleb()
		case 0x43: // f32.const
			r.bytes(4)
		case 0x44: // f64.const
			r.bytes(8)
		case 0x23: // global.get
			r.u32()
		case 0xd2: // ref.func
			r.u32()
		case 0xd0: // ref.null
			r.byte()
		case 0x6a, 0x6b, 0x6c, 0x7c, 0x7d, 0x7e: // extended const: add, sub, mul
		default:
			r.fail("unsupported opcode 0x%x in a constant expression", op)
		}
	}
	return nil
}

// Read parses a relocatable object.
func Read(b []byte) (*File, error) {
	if !IsObject(b) {
		return nil, errors.New("wasm object: not a WebAssembly module")
	}
	f := &File{Relocs: map[int][]Reloc{}, codeSec: -1, dataSec: -1}
	r := &reader{b: b, off: 8}
	var funcTypes []uint32
	type relocSec struct {
		payload []byte
	}
	var relocSecs []relocSec
	var linking []byte
	for r.off < len(b) && r.err == nil {
		id := r.byte()
		size := r.u32()
		payload := r.bytes(size)
		if r.err != nil {
			break
		}
		sec := Section{ID: id, Payload: payload}
		if id == SecCustom {
			sr := &reader{b: payload}
			sec.Name = sr.name()
			if sr.err != nil {
				return nil, sr.err
			}
			sec.Payload = payload[sr.off:]
		}
		f.Sections = append(f.Sections, sec)
		idx := len(f.Sections) - 1
		s := &reader{b: payload}
		switch id {
		case SecType:
			for n := s.u32(); n > 0 && s.err == nil; n-- {
				if form := s.byte(); form != 0x60 {
					s.fail("unsupported type form 0x%x", form)
				}
				var t FuncType
				t.Params = append([]byte(nil), s.bytes(s.u32())...)
				t.Results = append([]byte(nil), s.bytes(s.u32())...)
				f.Types = append(f.Types, t)
			}
		case SecImport:
			for n := s.u32(); n > 0 && s.err == nil; n-- {
				im := Import{Module: s.name(), Name: s.name(), Kind: s.byte()}
				switch im.Kind {
				case KindFunc:
					im.Type = s.u32()
					f.NumFuncImports++
				case KindTable:
					s.byte()
					s.limits()
					f.NumTableImports++
				case KindMemory:
					s.limits()
				case KindGlobal:
					im.ValType = s.byte()
					im.Mutable = s.byte() != 0
					f.NumGlobalImports++
				case KindTag:
					s.byte() // attribute: exception
					im.Type = s.u32()
					f.NumTagImports++
				default:
					s.fail("unsupported import kind %d", im.Kind)
				}
				f.Imports = append(f.Imports, im)
			}
		case SecFunction:
			for n := s.u32(); n > 0 && s.err == nil; n-- {
				funcTypes = append(funcTypes, s.u32())
			}
		case SecTable, SecMemory, SecExport, SecStart, SecElem, SecDataCount:
			// (an object's table, memory and exports are the linker's to
			// make; its element segment lists what relocations say too)
		case SecGlobal:
			for n := s.u32(); n > 0 && s.err == nil; n-- {
				g := Global{ValType: s.byte(), Mutable: s.byte() != 0}
				g.Init = s.expr()
				f.Globals = append(f.Globals, g)
			}
		case SecTag:
			for n := s.u32(); n > 0 && s.err == nil; n-- {
				s.byte()
				f.Tags = append(f.Tags, s.u32())
			}
		case SecCode:
			f.codeSec = idx
			n := s.u32()
			if int(n) != len(funcTypes) {
				s.fail("%d function bodies for %d functions", n, len(funcTypes))
			}
			for i := 0; i < int(n) && s.err == nil; i++ {
				size := s.u32()
				off := s.off
				body := s.bytes(size)
				f.Funcs = append(f.Funcs, Func{Type: funcTypes[i], Body: body, Off: uint32(off)})
			}
		case SecData:
			f.dataSec = idx
			for n := s.u32(); n > 0 && s.err == nil; n-- {
				var seg Segment
				switch flags := s.u32(); flags {
				case 0:
					s.expr()
				case 1:
					seg.Passive = true
				case 2:
					s.u32()
					s.expr()
				default:
					s.fail("unsupported data segment flags %d", flags)
				}
				size := s.u32()
				seg.Off = uint32(s.off)
				seg.Data = s.bytes(size)
				f.Segments = append(f.Segments, seg)
			}
		case SecCustom:
			switch {
			case sec.Name == "linking":
				linking = sec.Payload
			case len(sec.Name) > 6 && sec.Name[:6] == "reloc.":
				relocSecs = append(relocSecs, relocSec{sec.Payload})
			}
		}
		if s.err != nil {
			return nil, fmt.Errorf("section %d: %v", id, s.err)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	if linking == nil {
		return nil, errors.New("wasm object: no linking section (not a relocatable object)")
	}
	if err := f.readLinking(linking); err != nil {
		return nil, err
	}
	for _, rs := range relocSecs {
		s := &reader{b: rs.payload}
		sec := int(s.u32())
		var rels []Reloc
		for n := s.u32(); n > 0 && s.err == nil; n-- {
			rel := Reloc{Type: s.byte()}
			rel.Offset = s.u32()
			rel.Index = s.u32()
			if rel.Type >= numRelocTypes {
				s.fail("unknown relocation type %d", rel.Type)
			}
			if hasAddend(rel.Type) {
				rel.Addend = s.sleb()
			}
			rels = append(rels, rel)
		}
		if s.err != nil {
			return nil, s.err
		}
		if sec >= len(f.Sections) {
			return nil, fmt.Errorf("wasm object: relocations for section %d of %d", sec, len(f.Sections))
		}
		f.Relocs[sec] = rels
	}
	return f, nil
}

func (f *File) readLinking(b []byte) error {
	s := &reader{b: b}
	if v := s.u32(); v != 2 {
		return fmt.Errorf("wasm object: linking section version %d (want 2)", v)
	}
	for s.off < len(b) && s.err == nil {
		typ := s.byte()
		sub := &reader{b: s.bytes(s.u32())}
		switch typ {
		case 5: // WASM_SEGMENT_INFO
			n := sub.u32()
			if int(n) != len(f.Segments) {
				return fmt.Errorf("wasm object: segment info for %d of %d segments", n, len(f.Segments))
			}
			for i := range f.Segments {
				f.Segments[i].Name = sub.name()
				f.Segments[i].Align = sub.u32()
				f.Segments[i].Flags = sub.u32()
			}
		case 6: // WASM_INIT_FUNCS
			for n := sub.u32(); n > 0 && sub.err == nil; n-- {
				f.Init = append(f.Init, InitFunc{Priority: sub.u32(), Symbol: sub.u32()})
			}
		case 7: // WASM_COMDAT_INFO: wasm-ld -r has resolved them
		case 8: // WASM_SYMBOL_TABLE
			for n := sub.u32(); n > 0 && sub.err == nil; n-- {
				sym := Symbol{Kind: sub.byte(), Flags: sub.u32()}
				switch sym.Kind {
				case SymFunction, SymGlobal, SymTag, SymTable:
					sym.Index = sub.u32()
					if !sym.Undefined() || sym.Flags&FlagExplicitName != 0 {
						sym.Name = sub.name()
					} else {
						sym.Name = f.importName(sym.Kind, sym.Index)
					}
				case SymData:
					sym.Name = sub.name()
					if !sym.Undefined() {
						sym.Segment = sub.u32()
						sym.Offset = sub.u32()
						sym.Size = sub.u32()
					}
				case SymSection:
					sym.Index = sub.u32()
				default:
					sub.fail("unknown symbol kind %d", sym.Kind)
				}
				f.Symbols = append(f.Symbols, sym)
			}
		}
		if sub.err != nil {
			return sub.err
		}
	}
	return s.err
}

// importName is an undefined symbol's name when it has none of its own:
// its import's field name.
func (f *File) importName(kind byte, index uint32) string {
	want := map[byte]byte{SymFunction: KindFunc, SymGlobal: KindGlobal, SymTag: KindTag, SymTable: KindTable}[kind]
	i := uint32(0)
	for _, im := range f.Imports {
		if im.Kind != want {
			continue
		}
		if i == index {
			return im.Name
		}
		i++
	}
	return ""
}

// FuncImport is the function import at index i (of the function index
// space: imports first).
func (f *File) FuncImport(i uint32) *Import {
	n := uint32(0)
	for k := range f.Imports {
		if f.Imports[k].Kind != KindFunc {
			continue
		}
		if n == i {
			return &f.Imports[k]
		}
		n++
	}
	return nil
}

// GlobalImport is the global import at index i.
func (f *File) GlobalImport(i uint32) *Import {
	n := uint32(0)
	for k := range f.Imports {
		if f.Imports[k].Kind != KindGlobal {
			continue
		}
		if n == i {
			return &f.Imports[k]
		}
		n++
	}
	return nil
}

// DataSymbol returns a defined data symbol's contents (nil if it's not
// one, or is in a segment of zeros it isn't given in).
func (f *File) DataSymbol(name string) []byte {
	for i := range f.Symbols {
		s := &f.Symbols[i]
		if s.Kind != SymData || s.Undefined() || s.Name != name || int(s.Segment) >= len(f.Segments) {
			continue
		}
		d := f.Segments[s.Segment].Data
		if uint64(s.Offset)+uint64(s.Size) > uint64(len(d)) {
			return nil
		}
		return d[s.Offset : s.Offset+s.Size]
	}
	return nil
}
