// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux && !wasm

package unix

const is64bit = ^uint(0) >> 63 // 0 for 32-bit hosts, 1 for 64-bit ones.
