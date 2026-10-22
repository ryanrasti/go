// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "go_asm.h"
#include "textflag.h"

// _rt0_wasm_linux is the program's entry, exported as _start (as musl's,
// which the kernel's binfmt_wasm enters, with no parameters): the
// arguments come from the wasm_get_args system call (wasmArgs, in
// os_linux_wasm.go, from rt0_go).
TEXT _rt0_wasm_linux(SB),NOSPLIT,$0
	MOVD $runtime·wasmStack+(m0Stack__size-16)(SB), SP

	I32Const $0 // entry PC_B
	Call runtime·rt0_go(SB)
	Drop
	Call wasm_pc_f_loop(SB)

	Return

// func wasmSigtrampPC() uintptr
TEXT runtime·wasmSigtrampPC(SB), NOSPLIT, $0-8
	MOVD $runtime_wasm_sigtramp(SB), ret+0(FP)
	RET

// wasm_vfork_child is a vfork child's entry (syscall.rawVforkSyscall's
// clone function, which the kernel calls as int (*)(u32)): it returns
// from rawVforkSyscall, with 0, as the parent's goroutine, on its stack.
// R0: syscall.wasmVfork (i32): the goroutine's g, and SP as it returns.
TEXT wasm_vfork_child(SB),NOSPLIT,$0
	Get R0
	I64Load $0
	Set g

	Get R0
	I64Load $8
	I32WrapI64
	Set SP

	// rawVforkSyscall's results (r1+32(FP), err+40(FP)): 0, 0
	Get SP
	I64Const $0
	I64Store $32
	Get SP
	I64Const $0
	I64Store $40

	// its caller, from its return PC, at -8(SP)
	Call wasm_pc_f_loop(SB)

	I32Const $0
	Return
