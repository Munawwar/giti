package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
)

const applicationID = "io.github.Munawwar.Giti"

func main() {
	if len(os.Args) < 2 || os.Args[1] != "--resident" && os.Args[1] != "--ephemeral" {
		launch(os.Args)
		return
	}
	if len(os.Args) != 4 {
		fmt.Fprintf(os.Stderr, "usage: %s [--resident|--ephemeral] path history-json\n", os.Args[0])
		os.Exit(2)
	}
	runtime.LockOSThread()
	resident, path, history := os.Args[1] == "--resident", os.Args[2], historySpec{}
	if err := json.Unmarshal([]byte(os.Args[3]), &history); err != nil {
		fmt.Fprintln(os.Stderr, "giti:", err)
		os.Exit(2)
	}
	repo, err := newRepository(path, history)
	if err != nil {
		fmt.Fprintln(os.Stderr, "giti:", err)
		os.Exit(1)
	}
	glib.SetPrgname(applicationID)
	glib.SetApplicationName("Giti")
	gtk.Init(nil)
	application, err := gtk.ApplicationNew(applicationID, glib.APPLICATION_NON_UNIQUE)
	if err != nil {
		fmt.Fprintln(os.Stderr, "giti:", err)
		os.Exit(1)
	}
	var app *giti
	var appErr error
	application.Connect("activate", func() {
		app, appErr = newGiti(repo, resident, application)
		if appErr != nil {
			application.Quit()
			return
		}
		if resident {
			application.Hold()
		}
	})
	application.Run([]string{os.Args[0]})
	if appErr != nil {
		fmt.Fprintln(os.Stderr, "giti:", appErr)
		os.Exit(1)
	}
	if app.server != nil {
		app.server.stop()
	}
}
