// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !(linux && wasm)

package runtime

// argv's and envp's entries are pointers (but on linux/wasm: os_linux_wasm.go)
const argv32 = false
