// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime

import "unsafe"

func sbrk(n uintptr) unsafe.Pointer {
	sbrkSync()
	bl := bloc
	n = memRound(n)
	if bl+n > blocMax {
		grow := (bl + n - blocMax) / physPageSize
		size := growMemory(int32(grow))
		if size < 0 {
			return nil
		}
		resetMemoryDataView()
		blocMax = bl + n
	}
	bloc += n
	return unsafe.Pointer(bl)
}

// sbrkSync adopts what others have grown the memory by: on linux/wasm with
// cgo, C's malloc (musl's sbrk) grows it too, and only the memory's size
// says where new memory starts. Past blocMax the memory is theirs, so the
// break moves to the memory's end (as musl's sbrk does, the other way
// round); what's between bloc and blocMax stays the runtime's, free.
//
// memlock must be held.
func sbrkSync() {
	end := uintptr(currentMemory()) * physPageSize
	if end == blocMax {
		return
	}
	if bloc < blocMax {
		memFree(unsafe.Pointer(bloc), blocMax-bloc)
	}
	bloc, blocMax = end, end
}

// Implemented in src/runtime/sys_wasm.s
func growMemory(pages int32) int32
func currentMemory() int32
