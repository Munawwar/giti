package main

import (
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
		fmt.Fprintf(os.Stderr, "usage: %s [--resident|--ephemeral] path revision\n", os.Args[0])
		os.Exit(2)
	}
	runtime.LockOSThread()
	resident, path, revision := os.Args[1] == "--resident", os.Args[2], os.Args[3]
	repo, err := newRepository(path, revision)
	if err != nil {
		fmt.Fprintln(os.Stderr, "giti:", err)
		os.Exit(1)
	}
	glib.SetPrgname(applicationID)
	glib.SetApplicationName("Giti")
	gtk.Init(nil)
	app, err := newGiti(repo, resident)
	if err != nil {
		fmt.Fprintln(os.Stderr, "giti:", err)
		os.Exit(1)
	}
	gtk.Main()
	if app.server != nil {
		app.server.stop()
	}
}
