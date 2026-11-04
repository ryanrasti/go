#!/usr/bin/env bash
# Copyright 2026 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# linux-workers: the generated files for linux/wasm, a wasm32 Linux's processes.
#
# Their ABI is Linux's asm-generic one for 32 bits, with wasm32's data
# layout: riscv32's (no 32-bit time system calls, stat only as statx), and
# ILP32 with 64-bit integers 8-aligned, little endian, char signed. So the C
# side is clang for 32-bit ARM, whose data layout that is (with
# -fsigned-char), on the processes' own headers (musl's for wasm32, and the
# kernel's), as "gcc": cgo reads its objects' DWARF, which it can't for
# wasm32's (not ELF) or riscv32's (relocations). GOARCH=wasm's
# pointers and uintptr are 64 bits, the ABI's 32: in the types, fields of
# those become uint32 (mkpost_linux_wasm.go).
#
# usage: CLANG=clang SYSROOT_INCLUDE=dir ./mkall_linux_wasm.sh
set -euo pipefail
cd "$(dirname "$0")"
: "${CLANG:?clang}" "${SYSROOT_INCLUDE:?the include directory of the processes}"
shim=$(mktemp -d)
# (and mkerrors.sh's cgo leaves an object behind)
trap 'rm -rf "$shim" _cgo_*.o' EXIT
# (mkerrors.sh also builds and runs a program that prints the errors' and
# signals' names: a program for here, with HOSTCC; their numbers are the
# same as asm-generic's on any Linux)
cat > "$shim/gcc" <<SHIM
#!/bin/sh
case " \$* " in *" -o _errors "*) exec "${HOSTCC:-cc}" -DSIGUNUSED=31 "\$@" ;; esac
exec "$CLANG" --target=armv7-unknown-linux-musleabi -fsigned-char -D__daddr_t=int -D"__ino_t=unsigned long" -nostdinc -isystem "$SYSROOT_INCLUDE" "\$@"
SHIM
chmod +x "$shim/gcc"
# GOARCH=riscv: cgo's 32-bit view of the C types (cgo knows it for gccgo)
export PATH="$shim:$PATH" CC="$shim/gcc" GOOS=linux GOARCH=riscv

# musl's numbers, as the 32-bit ABI names them: its header gives the *64
# calls (fcntl64, _llseek, ...) as aliases of asm-generic's __NR3264_
# numbers, which mksysnum_linux.pl would name by their 64-bit names, so
# the numbers are resolved first, by a program printing them. But the
# 32-bit time calls (the kernel's __ARCH_WANT_TIME32_SYSCALLS'): their
# structs aren't ours (Timespec and Timeval are 64-bit time's, as on
# riscv32), so they're left out, the generic code's names for them made
# the _time64 calls' in syscall_linux_wasm.go
time32="adjtimex clock_adjtime clock_getres clock_gettime clock_nanosleep clock_settime futex
	gettimeofday io_getevents io_pgetevents mq_timedreceive mq_timedsend nanosleep ppoll pselect6
	recvmmsg rt_sigtimedwait sched_rr_get_interval semtimedop settimeofday timerfd_gettime
	timerfd_settime timer_gettime timer_settime utimensat wait4"
names=$(sed -n 's/^#define \(__NR_[a-z0-9_]*\) .*/\1/p' "$SYSROOT_INCLUDE/bits/syscall.h" |
	grep -vxF -e __NR_syscalls $(printf -- '-e __NR_%s ' $time32))
{
	echo "#include <stdio.h>"
	echo "#include \"$SYSROOT_INCLUDE/bits/syscall.h\""
	echo "int main(void) {"
	for n in $names; do echo "printf(\"#define $n %d\\n\", $n);"; done
	echo "return 0; }"
} > "$shim/sysnum.c"
"${HOSTCC:-cc}" -o "$shim/sysnum" "$shim/sysnum.c"
"$shim/sysnum" > "$shim/syscall.h"
perl mksysnum_linux.pl "$shim/syscall.h" | sed "1s|$shim/syscall.h|bits/syscall.h (musl's, resolved: mkall_linux_wasm.sh)|" > zsysnum_linux_wasm.go
./mkerrors.sh | sed 's/^\/\/go:build riscv && linux$/\/\/go:build wasm \&\& linux/' > zerrors_linux_wasm.go
# (glibc's types in types_linux.go's ustat: defined in the shim, for musl)
go tool cgo -godefs types_linux.go | GOOS= GOARCH= go run mkpost.go > ztypes_linux_wasm.go
# pointers in the ABI's structs: 32-bit words (GOARCH=wasm's are 64)
perl -pi -e 's/^(\t\w+\s+)\*\w+$/${1}uint32 \/\/ a pointer/' ztypes_linux_wasm.go
# timeval: the kernel's (getrusage, wait4: still 32-bit), not musl's
perl -0pi -e 's/type Timeval struct \{\n\tSec\s+int64\n\tUsec\s+int64\n\}/type Timeval struct {\n\tSec  int32\n\tUsec int32\n}/' ztypes_linux_wasm.go
grep -q 'Sec  int32' ztypes_linux_wasm.go
# no registers to ptrace: no C type for them here
sed -i 's/^type PtraceRegs _cgopackage.Incomplete$/type PtraceRegs struct{}/' ztypes_linux_wasm.go
gofmt -w zerrors_linux_wasm.go zsysnum_linux_wasm.go ztypes_linux_wasm.go
# (syscall_linux.go's calls that linux/wasm has its own of, in
# syscall_linux_wasm.go: munmap; and 32-bit time's, made as musl makes
# them, with the 64-bit time calls, as our Timespec and Timeval are)
grep -Ev '^//sys(nb)?	(munmap|wait4|Adjtimex|Settimeofday|utimensat|Nanosleep)\(' syscall_linux.go > "$shim/syscall_linux.go"
(cd "$shim" && GOOS= GOARCH= perl "$OLDPWD/mksyscall.pl" -l32 -tags linux,wasm syscall_linux.go "$OLDPWD/syscall_linux_wasm.go") |
	sed "s| $shim/| |; s| $PWD/| |" > zsyscall_linux_wasm.go
gofmt -w zsyscall_linux_wasm.go
