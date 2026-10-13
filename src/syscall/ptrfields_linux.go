// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux && !wasm

package syscall

import "unsafe"

// The pointers in the kernel's structs. (linux/wasm's are 32-bit words, its
// Go pointers 64: syscall_linux_wasm.go.)

func (iov *Iovec) setBase(b *byte) { iov.Base = b }

func (msg *Msghdr) setName(p unsafe.Pointer) { msg.Name = (*byte)(p) }

func (msg *Msghdr) setIov(iov *Iovec) { msg.Iov = iov }

func (msg *Msghdr) setControl(b *byte) { msg.Control = b }

func (p *SockFprog) setFilter(f *SockFilter) { p.Filter = f }
