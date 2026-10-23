// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime

import "unsafe"

// cgo on linux/wasm (linux-workers: docs/ports/cgo.md). C is linked into
// the program's module (cmd/link), with a stack of its own in the memory
// (its __stack_pointer); a call into C is asmcgocall's call_indirect, on
// the goroutine's wasm stack, and C calls back into Go through
// runtime/cgo's crosscall2, a wasmexport: on the goroutine's Go stack,
// below the cgocall's frame, as the wasmexport wrappers of wasip1 run.
//
// Go switches goroutines by unwinding the wasm stack to the loop that
// runs them; a callback's wrapper stops that at its own loop
// (wasm_pc_f_loop_export), so a callback can block: other goroutines run
// in that loop, inside the C frames, and the loop returns to C when the
// callback's goroutine returns to the wrapper. Those goroutines may call
// C and be called back too, nesting deeper: C's frames can only be left
// in the order they were entered, so a callback returns once it's the
// innermost (cgoWasmCallbackExit); until then its goroutine waits.

// cgoWasmCallbacks are the goroutines in callbacks, innermost last, and
// whether each is waiting to return.
var cgoWasmCallbacks []cgoWasmActive

type cgoWasmActive struct {
	gp      *g
	waiting bool
}

func cgoWasmCallbackEnter(gp *g) {
	cgoWasmCallbacks = append(cgoWasmCallbacks, cgoWasmActive{gp: gp})
}

func cgoWasmCallbackExit(gp *g) {
	for {
		n := len(cgoWasmCallbacks)
		if n == 0 {
			throw("cgo callback: none entered")
		}
		if cgoWasmCallbacks[n-1].gp == gp {
			break
		}
		// (an outer callback's: it returns once those inside it have)
		for i := range cgoWasmCallbacks {
			if cgoWasmCallbacks[i].gp == gp {
				cgoWasmCallbacks[i].waiting = true
			}
		}
		gopark(nil, nil, waitReasonCgoCallbackReturn, traceBlockSync, 1)
	}
	n := len(cgoWasmCallbacks) - 1
	cgoWasmCallbacks[n] = cgoWasmActive{}
	cgoWasmCallbacks = cgoWasmCallbacks[:n]
	if n > 0 && cgoWasmCallbacks[n-1].waiting {
		cgoWasmCallbacks[n-1].waiting = false
		goready(cgoWasmCallbacks[n-1].gp, 1)
	}
}

// cgoWasmCallback is crosscall2's (runtime/cgo): a call from C into Go's
// fn (a function pointer: its table index), with its argument frame (in
// C's stack).
//
//go:linkname cgoWasmCallback
//go:nosplit
func cgoWasmCallback(fn, frame, ctxt uintptr) {
	gp := getg()
	if gp.m.ncgo == 0 || gp != gp.m.curg {
		// (from a C constructor, run before the runtime starts, or from
		// a signal handler of C's: no goroutine to run it on)
		writeErrStr("fatal error: cgo callback outside of a call to C\n")
		exit(2)
	}
	cgocallbackg(unsafe.Pointer(fn<<16), unsafe.Pointer(frame), ctxt)
}

// cgoWasmTopOfStack is _cgo_topofstack's (runtime/cgo), which cgo's C
// wrappers use to find their argument frame again after a callback that
// may have moved the goroutine's stack.
//
//go:linkname cgoWasmTopOfStack
//go:nosplit
func cgoWasmTopOfStack() uintptr {
	return getg().m.curg.stack.hi
}
