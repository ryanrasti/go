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
