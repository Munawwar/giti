//go:build !darwin

package main

/*
#cgo pkg-config: gdk-x11-3.0
#include <stdint.h>
typedef struct _GdkWindow GdkWindow;
uint32_t gdk_x11_get_server_time(GdkWindow *window);
*/
import "C"

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
)

const primaryMask, primaryLabel = gdk.CONTROL_MASK, "Ctrl+"

func setupPlatformWindow(*giti) {}

// presentWindow raises the window with an X server timestamp, which focus-stealing
// prevention requires when a resident instance is reopened from a terminal.
func presentWindow(window *gtk.Window) {
	if gdkWindow, err := window.GetWindow(); err == nil && gdkWindow.IsA(glib.TypeFromName("GdkX11Window")) {
		window.PresentWithTime(uint32(C.gdk_x11_get_server_time((*C.GdkWindow)(unsafe.Pointer(gdkWindow.Native())))))
	} else {
		window.Present()
	}
}

// processExecutable fails once the process has exited or become a zombie.
func processExecutable(pid int) (string, error) {
	target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	return strings.TrimSuffix(target, " (deleted)"), err
}
