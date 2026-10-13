// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// linux-workers: LKL's wasm32 processes. Their system calls are the
// asm-generic ones for 32 bits, with riscv32's choices: no 32-bit time
// calls (the _time64 ones instead), stat only as statx (as loong64's),
// fcntl64, _llseek, the *64 calls for 64-bit sizes and offsets, which take
// two words, low first (mksyscall -l32). GOARCH=wasm's pointers and
// uintptr are 64-bit, the ABI's 32: in its structs they're uint32.

package syscall

import (
	runtimesyscall "internal/runtime/syscall/linux"
	"sync"
	"sync/atomic"
	"unsafe"
)

const (
	_SYS_setgroups  = SYS_SETGROUPS
	_SYS_clone3     = 435
	_SYS_faccessat2 = 439
	_SYS_fchmodat2  = 452

	// the generic code's names for the 32-bit ABI's calls
	SYS_FCNTL     = SYS_FCNTL64
	SYS_UTIMENSAT = SYS_UTIMENSAT_TIME64
)

//sys	EpollWait(epfd int, events []EpollEvent, msec int) (n int, err error) = SYS_EPOLL_PWAIT
//sys	Fchown(fd int, uid int, gid int) (err error)
//sys	fstatfs64(fd int, size int, buf *Statfs_t) (err error) = SYS_FSTATFS64
//sys	Ftruncate(fd int, length int64) (err error) = SYS_FTRUNCATE64
//sysnb	Getegid() (egid int)
//sysnb	Geteuid() (euid int)
//sysnb	Getgid() (gid int)
//sysnb	Getuid() (uid int)
//sys	Listen(s int, n int) (err error)
//sys	pread(fd int, p []byte, offset int64) (n int, err error) = SYS_PREAD64
//sys	pwrite(fd int, p []byte, offset int64) (n int, err error) = SYS_PWRITE64
//sys	Renameat(olddirfd int, oldpath string, newdirfd int, newpath string) (err error) = SYS_RENAMEAT2
//sys	_llseek(fd int, offhi int, offlo int, result *int64, whence int) (err error) = SYS__LLSEEK
//sys	sendfile(outfd int, infd int, offset *int64, count int) (written int, err error) = SYS_SENDFILE64
//sys	Setfsgid(gid int) (err error)
//sys	Setfsuid(uid int) (err error)
//sys	Shutdown(fd int, how int) (err error)
//sys	Splice(rfd int, roff *int64, wfd int, woff *int64, len int, flags int) (n int64, err error)
//sys	statx(dirfd int, path string, flags int, mask int, stat *statx_t) (err error)
//sys	statfs64(path string, size int, buf *Statfs_t) (err error) = SYS_STATFS64
//sys	SyncFileRange(fd int, off int64, n int64, flags int) (err error)
//sys	Truncate(path string, length int64) (err error) = SYS_TRUNCATE64
//sys	accept4(s int, rsa *RawSockaddrAny, addrlen *_Socklen, flags int) (fd int, err error)
//sys	bind(s int, addr unsafe.Pointer, addrlen _Socklen) (err error)
//sys	connect(s int, addr unsafe.Pointer, addrlen _Socklen) (err error)
//sysnb	getgroups(n int, list *_Gid_t) (nn int, err error)
//sys	getsockopt(s int, level int, name int, val unsafe.Pointer, vallen *_Socklen) (err error)
//sys	setsockopt(s int, level int, name int, val unsafe.Pointer, vallen uintptr) (err error)
//sysnb	socket(domain int, typ int, proto int) (fd int, err error)
//sysnb	socketpair(domain int, typ int, proto int, fd *[2]int32) (err error)
//sysnb	getpeername(fd int, rsa *RawSockaddrAny, addrlen *_Socklen) (err error)
//sysnb	getsockname(fd int, rsa *RawSockaddrAny, addrlen *_Socklen) (err error)
//sys	recvfrom(fd int, p []byte, flags int, from *RawSockaddrAny, fromlen *_Socklen) (n int, err error)
//sys	sendto(s int, buf []byte, flags int, to unsafe.Pointer, addrlen _Socklen) (err error)
//sys	recvmsg(s int, msg *Msghdr, flags int) (n int, err error)
//sys	sendmsg(s int, msg *Msghdr, flags int) (n int, err error)
//sys	pselect(nfd int, r *FdSet, w *FdSet, e *FdSet, timeout *Timespec, sigmask *sigset_t) (n int, err error) = SYS_PSELECT6_TIME64
//sys	ppoll(fds *pollFd, nfds int, timeout *Timespec, sigmask *sigset_t) (n int, err error) = SYS_PPOLL_TIME64

func Fstatfs(fd int, buf *Statfs_t) error {
	return fstatfs64(fd, int(unsafe.Sizeof(*buf)), buf)
}

func Statfs(path string, buf *Statfs_t) error {
	return statfs64(path, int(unsafe.Sizeof(*buf)), buf)
}

func Seek(fd int, offset int64, whence int) (int64, error) {
	var off int64
	err := _llseek(fd, int(offset>>32), int(uint32(offset)), &off, whence)
	return off, err
}

// makedev makes C dev_t from major and minor numbers the glibc way:
// 0xMMMM_MMMM 0xmmmm_mmmm -> 0xMMMM_Mmmm_mmmM_MMmm
func makedev(major uint32, minor uint32) uint64 {
	majorH := uint64(major >> 12)
	majorL := uint64(major & 0xfff)
	minorH := uint64(minor >> 8)
	minorL := uint64(minor & 0xff)
	return (majorH << 44) | (minorH << 20) | (majorL << 8) | minorL
}

func timespecFromStatxTimestamp(x statxTimestamp) Timespec {
	return Timespec{
		Sec:  x.Sec,
		Nsec: int32(x.Nsec),
	}
}

func fstatat(dirfd int, path string, stat *Stat_t, flags int) (err error) {
	var r statx_t
	// Do it the glibc way, add AT_NO_AUTOMOUNT.
	if err = statx(dirfd, path, _AT_NO_AUTOMOUNT|flags, _STATX_BASIC_STATS, &r); err != nil {
		return err
	}

	stat.Dev = makedev(r.Dev_major, r.Dev_minor)
	stat.Ino = r.Ino
	stat.Mode = uint32(r.Mode)
	stat.Nlink = r.Nlink
	stat.Uid = r.Uid
	stat.Gid = r.Gid
	stat.Rdev = makedev(r.Rdev_major, r.Rdev_minor)
	stat.Size = int64(r.Size)
	stat.Blksize = int32(r.Blksize)
	stat.Blocks = int64(r.Blocks)
	stat.Atim = timespecFromStatxTimestamp(r.Atime)
	stat.Mtim = timespecFromStatxTimestamp(r.Mtime)
	stat.Ctim = timespecFromStatxTimestamp(r.Ctime)

	return nil
}

func Fstatat(fd int, path string, stat *Stat_t, flags int) (err error) {
	return fstatat(fd, path, stat, flags)
}

func Fstat(fd int, stat *Stat_t) (err error) {
	return fstatat(fd, "", stat, _AT_EMPTY_PATH)
}

func Stat(path string, stat *Stat_t) (err error) {
	return fstatat(_AT_FDCWD, path, stat, 0)
}

func Lchown(path string, uid int, gid int) (err error) {
	return Fchownat(_AT_FDCWD, path, uid, gid, _AT_SYMLINK_NOFOLLOW)
}

func Lstat(path string, stat *Stat_t) (err error) {
	return fstatat(_AT_FDCWD, path, stat, _AT_SYMLINK_NOFOLLOW)
}

type sigset_t struct {
	X__val [32]uint32
}

func Select(nfd int, r *FdSet, w *FdSet, e *FdSet, timeout *Timeval) (n int, err error) {
	var ts *Timespec
	if timeout != nil {
		ts = &Timespec{Sec: int64(timeout.Sec), Nsec: timeout.Usec * 1000}
	}
	return pselect(nfd, r, w, e, ts, nil)
}

func setTimespec(sec, nsec int64) Timespec {
	return Timespec{Sec: sec, Nsec: int32(nsec)}
}

func setTimeval(sec, usec int64) Timeval {
	return Timeval{Sec: int32(sec), Usec: int32(usec)}
}

func futimesat(dirfd int, path string, tv *[2]Timeval) (err error) {
	if tv == nil {
		return utimensat(dirfd, path, nil, 0)
	}

	ts := []Timespec{
		NsecToTimespec(TimevalToNsec(tv[0])),
		NsecToTimespec(TimevalToNsec(tv[1])),
	}
	return utimensat(dirfd, path, (*[2]Timespec)(unsafe.Pointer(&ts[0])), 0)
}

func Time(t *Time_t) (Time_t, error) {
	var tv Timeval
	err := Gettimeofday(&tv)
	if err != nil {
		return 0, err
	}
	if t != nil {
		*t = Time_t(tv.Sec)
	}
	return Time_t(tv.Sec), nil
}

func Utime(path string, buf *Utimbuf) error {
	tv := []Timeval{
		{Sec: int32(buf.Actime)},
		{Sec: int32(buf.Modtime)},
	}
	return Utimes(path, tv)
}

func utimes(path string, tv *[2]Timeval) (err error) {
	if tv == nil {
		return utimensat(_AT_FDCWD, path, nil, 0)
	}

	ts := []Timespec{
		NsecToTimespec(TimevalToNsec(tv[0])),
		NsecToTimespec(TimevalToNsec(tv[1])),
	}
	return utimensat(_AT_FDCWD, path, (*[2]Timespec)(unsafe.Pointer(&ts[0])), 0)
}

func (iov *Iovec) SetLen(length int) {
	iov.Len = uint32(length)
}

// The pointers in the kernel's structs, as the ABI's 32-bit words. A Go
// pointer kept only as a number doesn't keep its object alive, nor in
// place: a stack's objects move when it grows. So each pointee escapes, to
// the heap, whose objects don't move (escape), and the callers keep them
// alive (they're in use until the system call returns).
var escapeSink unsafe.Pointer

func escape(p unsafe.Pointer) uint32 {
	atomic.StorePointer(&escapeSink, p)
	return uint32(uintptr(p))
}

func (iov *Iovec) setBase(b *byte) { iov.Base = escape(unsafe.Pointer(b)) }

func (msg *Msghdr) setName(p unsafe.Pointer) { msg.Name = escape(p) }

func (msg *Msghdr) setIov(iov *Iovec) { msg.Iov = escape(unsafe.Pointer(iov)) }

func (msg *Msghdr) setControl(b *byte) { msg.Control = escape(unsafe.Pointer(b)) }

func (p *SockFprog) setFilter(f *SockFilter) { p.Filter = escape(unsafe.Pointer(f)) }

func (msghdr *Msghdr) SetControllen(length int) {
	msghdr.Controllen = uint32(length)
}

func (cmsg *Cmsghdr) SetLen(length int) {
	cmsg.Len = uint32(length)
}

func InotifyInit() (fd int, err error) {
	return InotifyInit1(0)
}

func Pause() error {
	_, err := ppoll(nil, 0, nil, nil)
	return err
}

// mmap: the kernel can't map pages into the process's memory (as musl's
// does too, in its wasm32 port): the mapping is memory of ours, a copy of
// the file's, for private mappings; shared ones' writes aren't written back.
var wasmMaps struct {
	sync.Mutex
	m map[uintptr][]byte
}

func mmap(addr uintptr, length uintptr, prot int, flags int, fd int, offset int64) (uintptr, error) {
	if length == 0 || flags&MAP_FIXED != 0 {
		return 0, EINVAL
	}
	b := make([]byte, length)
	if flags&MAP_ANONYMOUS == 0 {
		for n := 0; n < len(b); {
			m, err := pread(fd, b[n:], offset+int64(n))
			if err != nil {
				return 0, err
			}
			if m == 0 {
				break
			}
			n += m
		}
	}
	p := uintptr(unsafe.Pointer(&b[0]))
	wasmMaps.Lock()
	if wasmMaps.m == nil {
		wasmMaps.m = make(map[uintptr][]byte)
	}
	wasmMaps.m[p] = b
	wasmMaps.Unlock()
	return p, nil
}

func munmap(addr uintptr, length uintptr) error {
	wasmMaps.Lock()
	defer wasmMaps.Unlock()
	if _, ok := wasmMaps.m[addr]; !ok {
		return EINVAL
	}
	delete(wasmMaps.m, addr)
	return nil
}

// What the ABI lacks, 32-bit time's calls, from others (as musl's).

// wait4, by waitid (musl's __emulate_wait4): the rusage is the kernel's for
// both
func wait4(pid int, wstatus *_C_int, options int, rusage *Rusage) (wpid int, err error) {
	idtype, id := _P_PID, pid
	switch {
	case pid < -1:
		idtype, id = _P_PGID, -pid
	case pid == -1:
		idtype, id = _P_ALL, 0
	case pid == 0:
		idtype = _P_PGID
	}
	var info [32]int32 // siginfo_t: signo, errno, code; pid, uid, status
	_, _, e := Syscall6(SYS_WAITID, uintptr(idtype), uintptr(id), uintptr(unsafe.Pointer(&info)),
		uintptr(options|_WEXITED), uintptr(unsafe.Pointer(rusage)), 0)
	if e != 0 {
		return 0, errnoErr(e)
	}
	if info[3] != 0 && wstatus != nil {
		code, status := info[2], info[5]
		var sw int32
		switch code {
		case _CLD_CONTINUED:
			sw = 0xffff
		case _CLD_DUMPED:
			sw = status&0x7f | 0x80
		case _CLD_EXITED:
			sw = (status & 0xff) << 8
		case _CLD_KILLED:
			sw = status & 0x7f
		case _CLD_STOPPED, _CLD_TRAPPED:
			sw = status<<8 + 0x7f
		}
		*wstatus = _C_int(sw)
	}
	return int(info[3]), nil
}

const (
	_P_ALL  = 0
	_P_PID  = 1
	_P_PGID = 2

	_WEXITED = 4

	_CLD_EXITED    = 1
	_CLD_KILLED    = 2
	_CLD_DUMPED    = 3
	_CLD_TRAPPED   = 4
	_CLD_STOPPED   = 5
	_CLD_CONTINUED = 6

	_CLOCK_REALTIME = 0
)

func Gettimeofday(tv *Timeval) (err error) {
	var ts [2]int64 // struct __kernel_timespec
	_, _, e := RawSyscall(SYS_CLOCK_GETTIME64, _CLOCK_REALTIME, uintptr(unsafe.Pointer(&ts)), 0)
	if e != 0 {
		return errnoErr(e)
	}
	tv.Sec, tv.Usec = int32(ts[0]), int32(ts[1]/1000)
	return nil
}

func Settimeofday(tv *Timeval) (err error) {
	ts := [2]int64{int64(tv.Sec), int64(tv.Usec) * 1000}
	_, _, e := RawSyscall(SYS_CLOCK_SETTIME64, _CLOCK_REALTIME, uintptr(unsafe.Pointer(&ts)), 0)
	if e != 0 {
		return errnoErr(e)
	}
	return nil
}

func Adjtimex(buf *Timex) (state int, err error) {
	return -1, ENOSYS
}

func rawSyscallNoError(trap, a1, a2, a3 uintptr) (r1, r2 uintptr) {
	r1, r2, _ = runtimesyscall.Syscall6(trap, a1, a2, a3, 0, 0, 0)
	return
}

// vfork's child can't run on wasm: it would return twice from the call.
// (Exec needs a spawn of the kernel's, later.)
func rawVforkSyscall(trap, a1, a2, a3 uintptr) (r1 uintptr, err Errno) {
	return 0, ENOSYS
}

