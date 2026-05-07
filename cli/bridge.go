package main

/*
#cgo CFLAGS: -I${SRCDIR}/../abi
#cgo linux LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>
#include "core.h"

typedef const char* (*fa_status_name_fn)(fa_status_code);

static void* sec_dlopen(const char* path) { return dlopen(path, RTLD_NOW); }
static void* sec_dlsym(void* handle, const char* name) { return dlsym(handle, name); }
static const char* sec_dlerror(void) { return dlerror(); }
static const char* sec_status_name(void* fn, fa_status_code code) {
    return ((fa_status_name_fn)fn)(code);
}
*/
import "C"

import (
	"bytes"
	"fmt"
	"os"
	"unsafe"
)

type coreLib struct {
	handle           unsafe.Pointer
	statusName       unsafe.Pointer
	modelRunV1       unsafe.Pointer
	modelFreeV1      unsafe.Pointer
	modelRunV2       unsafe.Pointer
	modelFreeV2      unsafe.Pointer
	riskCheckV1      unsafe.Pointer
	riskOutputFreeV1 unsafe.Pointer
}

func loadCore(path string) (*coreLib, error) {
	if path == "" {
		return nil, fail(2, "--core-lib is required")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fail(2, "core library not found: %s; run `make build` first", path)
	}
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	handle := C.sec_dlopen(cpath)
	if handle == nil {
		return nil, fail(2, "failed to load core library: %s", cString(C.sec_dlerror()))
	}
	load := func(name string) (unsafe.Pointer, error) {
		cname := C.CString(name)
		defer C.free(unsafe.Pointer(cname))
		ptr := C.sec_dlsym(handle, cname)
		if ptr == nil {
			return nil, fail(2, "missing core symbol %s: %s", name, cString(C.sec_dlerror()))
		}
		return ptr, nil
	}
	var err error
	lib := &coreLib{handle: handle}
	if lib.statusName, err = load("fa_status_name"); err != nil {
		return nil, err
	}
	if lib.modelRunV1, err = load("fa_model_run_v1"); err != nil {
		return nil, err
	}
	if lib.modelFreeV1, err = load("fa_model_output_free_v1"); err != nil {
		return nil, err
	}
	if lib.modelRunV2, err = load("fa_model_run_v2"); err != nil {
		return nil, err
	}
	if lib.modelFreeV2, err = load("fa_model_output_free_v2"); err != nil {
		return nil, err
	}
	if lib.riskCheckV1, err = load("fa_risk_check_v1"); err != nil {
		return nil, err
	}
	if lib.riskOutputFreeV1, err = load("fa_risk_output_free_v1"); err != nil {
		return nil, err
	}
	return lib, nil
}

func (c *coreLib) status(code C.fa_status_code) string {
	raw := C.sec_status_name(c.statusName, code)
	if raw == nil {
		return fmt.Sprintf("status_%d", int(code))
	}
	return C.GoString(raw)
}

func cString(raw *C.char) string {
	if raw == nil {
		return ""
	}
	return C.GoString(raw)
}

func cCharArrayString(ptr unsafe.Pointer, n int) string {
	b := C.GoBytes(ptr, C.int(n))
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func setCCharArray(dst *C.char, n int, value string) {
	bytes := []byte(value)
	if len(bytes) > n-1 {
		bytes = bytes[:n-1]
	}
	for i := 0; i < n; i++ {
		*(*C.char)(unsafe.Add(unsafe.Pointer(dst), i)) = 0
	}
	for i, b := range bytes {
		*(*C.char)(unsafe.Add(unsafe.Pointer(dst), i)) = C.char(b)
	}
}
