// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !wasm

package runtime

// pause is only used on wasm.
func pause(newsp uintptr) { panic("unreachable") }

// cgoWasmCallbackEnter and cgoWasmCallbackExit are only used on wasm
// (cgocall_wasm.go).
func cgoWasmCallbackEnter(gp *g) { panic("unreachable") }
func cgoWasmCallbackExit(gp *g)  { panic("unreachable") }
