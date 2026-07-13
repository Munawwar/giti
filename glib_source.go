package main

/*
#cgo pkg-config: glib-2.0
#include <glib.h>
#include <stdint.h>

guint giti_add_source(guint seconds, uintptr_t handle);
*/
import "C"

import "runtime/cgo"

// addMainSource schedules work on GTK's main context without presenting an
// integer callback handle to Go's garbage collector as a pointer.
func addMainSource(seconds uint, callback func() bool) {
	handle := cgo.NewHandle(callback)
	if C.giti_add_source(C.guint(seconds), C.uintptr_t(handle)) == 0 {
		handle.Delete()
	}
}

//export gitiSourceFunc
func gitiSourceFunc(handle C.uintptr_t) C.gboolean {
	if cgo.Handle(handle).Value().(func() bool)() {
		return 1
	}
	return 0
}

//export gitiSourceDestroy
func gitiSourceDestroy(handle C.uintptr_t) {
	cgo.Handle(handle).Delete()
}
