// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// linux-workers: Linux on wasm, tombl's arch/wasm (binfmt_wasm.c): the
// process's memory is its own wasm memory (env.memory, imported), which it
// grows itself (the kernel has no memory system calls: as on other wasm),
// and the kernel's system calls are its linux.syscall import, the 32-bit
// asm-generic ABI's (as riscv32's), with arch/wasm's own numbers above 244.
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
	_SYS_wasm_get_args           = 245 // __NR_arch_specific_syscall + 1
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

// wasmProcessArgs is where the kernel puts the arguments, environment and
// auxiliary vector (struct wasm_process_args: len, envc, argc, then argv and
// envp, 32-bit pointers), as musl's crt (arch/wasm32/crt_arch.h): four
// pages, the kernel's limit.
var wasmProcessArgs [256 << 10 / 4]uint32

// wasmArgs is rt0_go's args on linux/wasm: from the kernel's wasm_get_args.
//
//go:nosplit
func wasmArgs() {
	if _, e := syscall6(_SYS_wasm_get_args, uintptr(unsafe.Pointer(&wasmProcessArgs[0])), uintptr(len(wasmProcessArgs)*4), 0, 0, 0, 0); e != 0 {
		exit(127)
	}
	args(int32(wasmProcessArgs[2]), (**byte)(unsafe.Pointer(uintptr(wasmProcessArgs[3]))))
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


// Signals. The kernel calls a process's handler at a system call's return,
// through the host: table entry sa_handler, called with the signal's number,
// on the wasm stack of the call it interrupts (arch/wasm/kernel/signal.c).
// The runtime's handler, wasmSigtramp, only notes the signal; what
// os/signal wants of it is sent on (sigsend) from a goroutine, the one
// waiting for signals (notetsleepg), where the runtime can do anything.
// Handlers are installed for the signals os/signal asks for, and SIGPIPE
// (which, unless asked for, is ignored: the write fails with EPIPE), as on
// other Linux; the others keep the kernel's default actions.

const (
	_SYS_kill         = 129
	_SYS_rt_sigaction = 134
	_SYS_getpid       = 172
	_SA_RESTART       = 0x10000000
	_SIG_DFL          = 0
	_SIG_IGN          = 1
	_SIGPIPE          = 13
)

// the kernel's struct sigaction (asm-generic's, with sa_restorer)
type sigactiont struct {
	_        structs.HostLayout
	handler  uint32
	flags    uint32
	restorer uint32
	mask     [2]uint32
}

const _NSIG = 65

// signals the kernel delivered, not yet sent on
var wasmPendingSignals uint64

// wasmSigtramp is the runtime's signal handler, called by the host as the
// process's table entry: nosplit, on the stack of the goroutine whose
// system call it interrupts.
//
//go:wasmexport runtime_wasm_sigtramp
//go:nosplit
func wasmSigtramp(sig int32) {
	wasmPendingSignals |= 1 << (uint32(sig) & 63)
}

// wasmSigtrampPC is wasmSigtramp's wasm entry (its wasmexport wrapper,
// runtime_wasm_sigtramp), as a PC (rt0_linux_wasm.s).
func wasmSigtrampPC() uintptr

func setsig(sig uint32, handler uint32) {
	sa := sigactiont{handler: handler}
	if handler > _SIG_IGN {
		sa.flags = _SA_RESTART
	}
	syscall6(_SYS_rt_sigaction, uintptr(sig), uintptr(unsafe.Pointer(&sa)), 0, 8, 0, 0)
}

// the handler, as the kernel calls it: an index in the process's table (PC_F)
func wasmSigHandler() uint32 {
	return uint32(wasmSigtrampPC() >> 16)
}

func initsig(preinit bool) {
	setsig(_SIGPIPE, wasmSigHandler())
}

func sigenable(sig uint32) {
	setsig(sig, wasmSigHandler())
}

func sigdisable(sig uint32) {
	if sig != _SIGPIPE {
		setsig(sig, _SIG_DFL)
	}
}

func sigignore(sig uint32) {
	setsig(sig, _SIG_IGN)
}

// wasmDeliverSignals sends the noted signals on to os/signal; one it
// doesn't want (it stopped wanting it) takes the kernel's default action,
// but SIGPIPE, which is ignored.
func wasmDeliverSignals() {
	for wasmPendingSignals != 0 {
		p := wasmPendingSignals
		wasmPendingSignals = 0
		for s := uint32(1); s < 64; s++ {
			if p&(1<<s) != 0 && !sigsend(s) && s != _SIGPIPE {
				dieFromSignal(s)
			}
		}
	}
}

// dieFromSignal ends the process by the signal's default action.
func dieFromSignal(sig uint32) {
	setsig(sig, _SIG_DFL)
	pid, _ := syscall6(_SYS_getpid, 0, 0, 0, 0, 0, 0)
	syscall6(_SYS_kill, pid, uintptr(sig), 0, 0, 0, 0)
	exit(2)
}

// os.sigpipe: a write to standard output or error failed with EPIPE, which
// ends the program by SIGPIPE, as on other Linux.
//
//go:linkname os_sigpipe os.sigpipe
func os_sigpipe() {
	dieFromSignal(_SIGPIPE)
}
