// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// linux/wasm's runtime/cgo (linux-workers: docs/ports/cgo.md). The runtime
// calls these through asmcgocall, which calls every C function as
// int (*)(void *). Go's pointers are 8 bytes and C's 4: a Go array of
// pointers is read here as 64-bit words.

#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>
#include <unistd.h>

// musl's (arch/wasm32/process_args.h): what the kernel's wasm_get_args
// wrote, which the runtime asked for (os_linux_wasm.go)
struct wasm_process_args {
	int len, envc, argc;
	char **argv, **envp;
};

// musl's start, its crt1's _start's without main (src/env)
extern void __init_libc(char **envp, char *pn);
extern void __libc_start_init(void);
// the constructors, run in order (cmd/link writes it)
extern void __wasm_call_ctors(void);

// x_cgo_init starts libc, as musl's _start would have: from the runtime's
// osinit, before Go's heap grows the memory, so that libc's break starts
// at the memory's end as it is now (the linker's __heap_end), past Go's;
// from then on each grows the memory past the other's (Go's sbrkSync,
// musl's brk).
int
x_cgo_init(void *arg)
{
	struct wasm_process_args *a = arg;

	sbrk(0);
	__init_libc(a->envp, a->argv[0]);
	__libc_start_init();
	__wasm_call_ctors();
	return 0;
}

int
x_cgo_setenv(void *arg)
{
	uint64_t *a = arg;

	setenv((char *)(uintptr_t)a[0], (char *)(uintptr_t)a[1], 1);
	return 0;
}

int
x_cgo_unsetenv(void *arg)
{
	uint64_t *a = arg;

	unsetenv((char *)(uintptr_t)a[0]);
	return 0;
}

int clearenv(void);

int
x_cgo_clearenv(void *arg)
{
	(void)arg;
	clearenv();
	return 0;
}

// _cgo_export.c's: Go calls only come from C that Go called, so the
// runtime is up, and there's no context to release.
size_t
_cgo_wait_runtime_init_done(void)
{
	return 0;
}

void
_cgo_release_context(size_t ctxt)
{
	(void)ctxt;
}
