// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "go_asm.h"
#include "textflag.h"

// _rt0_wasm_linux is the process's entry, exported as _start_c (as musl's,
// which LKL's exec calls): one parameter, the start block that the kernel
// laid out as on any Linux (argc, then argv's and envp's 32-bit pointers).
// R0: the start block (i32)
TEXT _rt0_wasm_linux(SB),NOSPLIT,$0
	MOVD $runtime·wasmStack+(m0Stack__size-16)(SB), SP

	Get SP
	Get R0
	I64Load32U $0 // argc
	I64Store $0

	Get SP
	Get R0
	I64ExtendI32U
	I64Const $4 // argv
	I64Add
	I64Store $8

	I32Const $0 // entry PC_B
	Call runtime·rt0_go(SB)
	Drop
	Call wasm_pc_f_loop(SB)

	Return
