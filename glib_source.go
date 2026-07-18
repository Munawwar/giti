package main

/*
#cgo pkg-config: gtk+-3.0
#include <glib.h>
#include <stdint.h>
#include <stdlib.h>

guint giti_add_source(guint milliseconds, uintptr_t handle);
void giti_set_accessibility(uintptr_t widget, const char *name, const char *description);
*/
import "C"

import (
	"runtime/cgo"
	"time"
	"unsafe"

	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
)

func init() {
	// gotk3 otherwise runs GObject finalizers on Go's finalizer goroutine. Queue
	// unrefs on GTK's context so widget and CSS teardown cannot race UI work.
	glib.FinalizerStrategy = func(finalize glib.Finalizer) {
		addMainSource(0, func() bool {
			finalize()
			return false
		})
	}
}

// addMainSource schedules work on GTK's main context without presenting an
// integer callback handle to Go's garbage collector as a pointer.
func addMainSource(delay time.Duration, callback func() bool) {
	handle := cgo.NewHandle(callback)
	if C.giti_add_source(C.guint(max(0, delay.Milliseconds())), C.uintptr_t(handle)) == 0 {
		handle.Delete()
	}
}

func setAccessibility(widget *gtk.Widget, name, description string) {
	cName, cDescription := C.CString(name), C.CString(description)
	defer C.free(unsafe.Pointer(cName))
	defer C.free(unsafe.Pointer(cDescription))
	C.giti_set_accessibility(C.uintptr_t(widget.Native()), cName, cDescription)
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
