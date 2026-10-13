// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// linux-workers: Linux on wasm, as LKL runs processes (src/lkl/host.ts in
// linux-workers): the process's memory is its own wasm memory, which it
// grows itself (no mmap: as on other wasm), and the kernel's system calls
// are its lkl.syscall import, the 32-bit asm-generic ABI's (as riscv32's).
// The runtime is the single-threaded one of wasm (os_wasm.go): this file
// gives it what wasip1 gives it through WASI (os_wasip1.go), from Linux.

package runtime

import (
	"internal/runtime/syscall/linux"
	"structs"
	"unsafe"
)

// the 32-bit ABI's words: argv's and envp's entries
const argv32 = true

const (
	_SYS_exit_group              = 94
	_SYS_write                   = 64
	_SYS_sched_yield             = 124
	_SYS_getrandom               = 278
	_SYS_clock_gettime64         = 403
	_SYS_clock_nanosleep_time64  = 407
	_CLOCK_REALTIME              = 0
	_CLOCK_MONOTONIC             = 1
	_EINTR                       = 4
	_EAGAIN                      = 11
)

// struct __kernel_timespec
type timespec64 struct {
	_    structs.HostLayout
	sec  int64
	nsec int64
}

//go:nosplit
func syscall6(num, a1, a2, a3, a4, a5, a6 uintptr) (uintptr, uintptr) {
	r, _, e := linux.Syscall6(num, a1, a2, a3, a4, a5, a6)
	return r, e
}

func exit(code int32) {
	for {
		syscall6(_SYS_exit_group, uintptr(code), 0, 0, 0, 0, 0)
	}
}

//go:nosplit
func write1(fd uintptr, p unsafe.Pointer, n int32) int32 {
	r, e := syscall6(_SYS_write, fd, uintptr(p), uintptr(n), 0, 0, 0)
	if e != 0 {
		return -int32(e)
	}
	return int32(r)
}

//go:nosplit
func read(fd int32, p unsafe.Pointer, n int32) int32 {
	r, e := linux.Read(int(fd), unsafe.Slice((*byte)(p), n))
	if e != 0 {
		return -int32(e)
	}
	return int32(r)
}

//go:nosplit
func open(name *byte, mode, perm int32) int32 {
	fd, e := linux.Open(name, int(mode), uint32(perm))
	if e != 0 {
		return -int32(e)
	}
	return int32(fd)
}

//go:nosplit
func closefd(fd int32) int32 {
	if e := linux.Close(int(fd)); e != 0 {
		return -int32(e)
	}
	return 0
}

func madvise(addr unsafe.Pointer, n uintptr, flags int32) int32 {
	return 0
}

func usleep(usec uint32) {
	ts := timespec64{sec: int64(usec / 1e6), nsec: int64(usec%1e6) * 1e3}
	for {
		_, e := syscall6(_SYS_clock_nanosleep_time64, _CLOCK_MONOTONIC, 0, uintptr(unsafe.Pointer(&ts)), uintptr(unsafe.Pointer(&ts)), 0, 0)
		if e != _EINTR {
			return
		}
	}
}

func readRandom(r []byte) int {
	n, e := syscall6(_SYS_getrandom, uintptr(unsafe.Pointer(&r[0])), uintptr(len(r)), 0, 0, 0, 0)
	if e != 0 {
		return 0
	}
	return int(n)
}

func goenvs() {
	goenvs_unix()
}

//go:nosplit
func clockGettime(clock uintptr) timespec64 {
	var ts timespec64
	if _, e := syscall6(_SYS_clock_gettime64, clock, uintptr(unsafe.Pointer(&ts)), 0, 0, 0, 0); e != 0 {
		throw("clock_gettime64 failed")
	}
	return ts
}

func walltime() (sec int64, nsec int32) {
	ts := clockGettime(_CLOCK_REALTIME)
	return ts.sec, int32(ts.nsec)
}

//go:nosplit
func nanotime1() int64 {
	ts := clockGettime(_CLOCK_MONOTONIC)
	return ts.sec*1e9 + ts.nsec
}

func resetMemoryDataView() {}

// (as wasip1's, for lock_wasip1.go: 0, or an error)
func sched_yield() uint32 {
	_, e := syscall6(_SYS_sched_yield, 0, 0, 0, 0, 0, 0)
	return uint32(e)
}

// Pulled by other packages (as os_linux.go's, signal_unix.go's).

//go:linkname fcntl
//go:nosplit
func fcntl(fd, cmd, arg int32) (ret int32, errno int32) {
	r, _, err := linux.Syscall6(linux.SYS_FCNTL, uintptr(fd), uintptr(cmd), uintptr(arg), 0, 0, 0)
	return int32(r), int32(err)
}

// one thread: the call, on it
//
//go:linkname syscall_runtime_doAllThreadsSyscall syscall.runtime_doAllThreadsSyscall
func syscall_runtime_doAllThreadsSyscall(trap, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2, err uintptr) {
	return linux.Syscall6(trap, a1, a2, a3, a4, a5, a6)
}

// no signal handlers to ignore SIGSYS with
//
//go:linkname ignoreSIGSYS os.ignoreSIGSYS
func ignoreSIGSYS() {}

//go:linkname restoreSIGSYS os.restoreSIGSYS
func restoreSIGSYS() {}

