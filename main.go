package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/gotk3/gotk3/gtk"
)

func main() {
	runtime.LockOSThread()
	resident, path, revision, err := arguments(os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	repo, err := newRepository(path, revision)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gitskim:", err)
		os.Exit(1)
	}
	gtk.Init(nil)
	app := newGitSkim(repo, resident)
	gtk.Main()
	if app.server != nil {
		app.server.stop()
	}
}

func arguments(args []string) (resident bool, path, revision string, err error) {
	path, revision = ".", "HEAD"
	if len(args) > 1 && (args[1] == "--resident" || args[1] == "--ephemeral") {
		if len(args) != 4 {
			return false, "", "", fmt.Errorf("usage: %s [--resident|--ephemeral] path revision", args[0])
		}
		return args[1] == "--resident", args[2], args[3], nil
	}
	if len(args) > 2 {
		return false, "", "", fmt.Errorf("usage: %s [HEAD|branch|tag|sha]", args[0])
	}
	if len(args) == 2 {
		revision = args[1]
	}
	return false, path, revision, nil
}
