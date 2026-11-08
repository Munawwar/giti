package main

/*
#include <libproc.h>
*/
import "C"

import (
	"errors"
	"unsafe"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
)

// GTK's quartz backend reports the Command key as MOD2.
const primaryMask, primaryLabel = gdk.MOD2_MASK | gdk.META_MASK, "⌘"

// setupPlatformWindow puts the window controls on the left and adds the standard
// Command-Q and Command-W, since there is no native title bar to supply them.
func setupPlatformWindow(app *giti) {
	app.windowHeader.SetDecorationLayout("close,minimize,maximize:")
	add := func(name, accel string, activate func()) {
		action := glib.SimpleActionNew(name, nil)
		action.Connect("activate", activate)
		app.application.AddAction(action)
		app.application.SetAccelsForAction("app."+name, []string{accel})
	}
	add("close-window", "<Primary>w", app.window.Close)
	add("quit", "<Primary>q", func() {
		if app.server != nil {
			app.server.stop()
		}
		app.quit()
	})
}

func presentWindow(window *gtk.Window) { window.Present() }

// processExecutable fails once the process has exited or become a zombie.
func processExecutable(pid int) (string, error) {
	buffer := make([]byte, C.PROC_PIDPATHINFO_MAXSIZE)
	if C.proc_pidpath(C.int(pid), unsafe.Pointer(&buffer[0]), C.uint32_t(len(buffer))) <= 0 {
		return "", errors.New("process has no executable")
	}
	return C.GoString((*C.char)(unsafe.Pointer(&buffer[0]))), nil
}
