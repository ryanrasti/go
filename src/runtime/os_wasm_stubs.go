// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// (linux/wasm has these: os_linux_wasm.go)
//go:build wasm && !linux

package runtime

import "unsafe"

// Stubs so tests can link correctly. These should never be called.
func open(name *byte, mode, perm int32) int32        { panic("not implemented") }
func closefd(fd int32) int32                         { panic("not implemented") }
func read(fd int32, p unsafe.Pointer, n int32) int32 { panic("not implemented") }

// No signals (but on linux/wasm: os_linux_wasm.go).

func initsig(preinit bool) {
}

// Do nothing on WASM platform, always return EPIPE to caller.
//
//go:linkname os_sigpipe os.sigpipe
func os_sigpipe() {}

func sigdisable(uint32) {}
func sigenable(uint32)  {}
func sigignore(uint32)  {}

const _NSIG = 0 // wasm has no signals
