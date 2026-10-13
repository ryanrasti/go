// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package poll

import (
	"syscall"
	"unsafe"
)

// (linux/wasm: the kernel's 32-bit pointer. The buffers are writev's, which
// keeps them alive, and in the heap: they come in a [][]byte.)
func newIovecWithBase(base *byte) syscall.Iovec {
	return syscall.Iovec{Base: uint32(uintptr(unsafe.Pointer(base)))}
}
