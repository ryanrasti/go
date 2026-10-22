// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "textflag.h"

// func rawVforkSyscall(trap, a1, a2, a3 uintptr) (r1 uintptr, err Errno)
//
// vfork on linux/wasm: clone(fn, arg, flags, ptid) with CLONE_VM, whose
// child the host runs as a new instance of the program, sharing its
// memory, entered at fn (the runtime's wasm_vfork_child) with arg: this
// call's g and SP, so that the child returns from this call, with 0, as
// the parent does with its pid. (Go's wasm keeps no state of a function in
// the wasm stack across a call: the caller resumes from its return PC on
// the Go stack, as it does when a goroutine switches.) The parent waits
// for the child's exec or exit (CLONE_VFORK), and its frames above this
// one are left alone by the child (forkAndExecInChild1's contract).
TEXT ·rawVforkSyscall(SB),NOSPLIT,$56-48
	// the child's way back: our caller's g, and SP as we return
	MOVD g, ·wasmVfork+0(SB)
	MOVD $trap+0(FP), R0
	MOVD R0, ·wasmVfork+8(SB)

	// vforkClone(trap, fn, arg, flags, ptid uintptr) (r1 uintptr, err Errno)
	MOVD trap+0(FP), R0
	MOVD R0, 0(SP)
	MOVD $wasm_vfork_child(SB), R0
	Get R0
	I64Const $16
	I64ShrU
	Set R0
	MOVD R0, 8(SP) // (its index in the table: its PC_F)
	MOVD $·wasmVfork(SB), 16(SP)
	MOVD a1+8(FP), R0
	MOVD R0, 24(SP)
	MOVD a3+24(FP), R0
	MOVD R0, 32(SP)
	CALL ·vforkClone(SB)
	MOVD 40(SP), R0
	MOVD R0, r1+32(FP)
	MOVD 48(SP), R0
	MOVD R0, err+40(FP)
	RET
