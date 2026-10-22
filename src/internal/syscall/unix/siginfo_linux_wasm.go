// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unix

// linux/wasm's ABI is 32-bit (its siginfo_t has no padding for pointers),
// though Go's uint is 64-bit
const is64bit = 0
