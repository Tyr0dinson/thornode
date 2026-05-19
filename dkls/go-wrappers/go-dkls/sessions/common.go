// Provides helper functions for facilitating interoperability between Go and Rust

//
//nolint:gocritic // cgo calls in this file expand into duplicate-looking expressions.
package session

/*
#cgo darwin,arm64 LDFLAGS: -L${SRCDIR}/../../includes/darwin/arm64 -Wl,-rpath,${SRCDIR}/../../includes/darwin/arm64 -lgodkls
#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/../../includes/linux/amd64 -Wl,-rpath,${SRCDIR}/../../includes/linux/amd64 -lgodkls
#cgo linux,arm64 LDFLAGS: -L${SRCDIR}/../../includes/linux/arm64 -Wl,-rpath,${SRCDIR}/../../includes/linux/arm64 -lgodkls
#include "../include/go-dkls.h"
#include <stdlib.h>
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// GoLang representation of C.Handle
type Handle int32

func cGoSlice(byteArray []byte, pinner *runtime.Pinner) *C.go_slice {
	var cGoSlice *C.go_slice
	if byteArray != nil {
		cGoSlice = (*C.go_slice)(unsafe.Pointer(&byteArray))
		pinner.Pin(&byteArray[0])
	}

	return cGoSlice
}

func cHandle(handle Handle) C.Handle {
	return C.Handle{
		_0: C.int32_t(handle),
	}
}

func cTssBuffer(byteArray []byte, pinner *runtime.Pinner) *C.tss_buffer {
	var cTssBuffer *C.tss_buffer
	if byteArray != nil {
		cTssBuffer = (*C.tss_buffer)(unsafe.Pointer(&byteArray))
		pinner.Pin(&byteArray[0])
	}

	return cTssBuffer
}
