// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux && !wasm

package syscall

// in assembly, asm_linux_$GOARCH.s (linux/wasm's: syscall_linux_wasm.go)

func rawSyscallNoError(trap, a1, a2, a3 uintptr) (r1, r2 uintptr)
func rawVforkSyscall(trap, a1, a2, a3 uintptr) (r1 uintptr, err Errno)
