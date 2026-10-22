// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix && !(linux && wasm)

package syscall

// execArgv is argv or envp as execve takes them (linux/wasm's: 32-bit
// pointers, syscall_linux_wasm.go).
func execArgv(p []*byte) []*byte { return p }
