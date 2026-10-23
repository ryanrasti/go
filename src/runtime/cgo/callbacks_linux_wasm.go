// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cgo

import _ "unsafe" // for go:linkname

// linux/wasm (linux-workers: docs/ports/cgo.md, runtime/cgocall_wasm.go):
// one thread, so of the runtime's hooks only libc's start (x_cgo_init,
// which osinit calls) and the environment's (setenv.go, clearenv.go). C
// calls Go through crosscall2 and _cgo_topofstack, wasmexports here (C
// calls them as functions of its own; cmd/link resolves them).

//go:cgo_import_static x_cgo_init
//go:linkname x_cgo_init x_cgo_init
//go:linkname _cgo_init _cgo_init
var x_cgo_init byte
var _cgo_init = &x_cgo_init

// crosscall2 is cgo's entry from C into Go: fn (a function pointer) with
// its argument frame a, of n bytes. (nosplit: the goroutine is in a
// system call, cgocall's, until cgocallbackg leaves it)
//
//go:wasmexport crosscall2
//go:nosplit
func crosscall2(fn, a uint32, n int32, ctxt uint32) {
	cgoWasmCallback(uintptr(fn), uintptr(a), uintptr(ctxt))
}

//go:wasmexport _cgo_topofstack
//go:nosplit
func _cgo_topofstack() uint32 {
	return uint32(cgoWasmTopOfStack())
}

//go:linkname cgoWasmCallback runtime.cgoWasmCallback
func cgoWasmCallback(fn, frame, ctxt uintptr)

//go:linkname cgoWasmTopOfStack runtime.cgoWasmTopOfStack
func cgoWasmTopOfStack() uintptr
