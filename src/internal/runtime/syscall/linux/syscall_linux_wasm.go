// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package linux

// wasmSyscall is the kernel's system call, each process's linux.syscall
// import (tombl's arch/wasm): arguments and result are the 32-bit ABI's
// words (a 64-bit argument takes two, low word first).
//
//go:wasmimport linux syscall
//go:noescape
func wasmSyscall(num, a1, a2, a3, a4, a5, a6 int32) int32

// Syscall6 calls system call number 'num' with arguments a1-6.
//
//go:nosplit
func Syscall6(num, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2, errno uintptr) {
	r := wasmSyscall(int32(num), int32(a1), int32(a2), int32(a3), int32(a4), int32(a5), int32(a6))
	if uint32(r) > -4096&0xffffffff {
		return ^uintptr(0), 0, uintptr(-r)
	}
	return uintptr(uint32(r)), 0, 0
}
