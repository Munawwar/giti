package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type openRequest struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
}

func main() {
	if len(os.Args) > 2 {
		fmt.Fprintf(os.Stderr, "usage: %s [HEAD|branch|tag|sha]\n", os.Args[0])
		os.Exit(2)
	}
	path, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	revision := "HEAD"
	if len(os.Args) == 2 {
		revision = os.Args[1]
	}
	response := contactResident(openRequest{Path: path, Revision: revision})
	if response == "OK" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	appName := "gitskim-app"
	if strings.HasSuffix(filepath.Base(executable), "-debug") {
		appName += "-debug"
	}
	app := filepath.Join(filepath.Dir(executable), appName)
	mode := "--resident"
	if response == "BUSY" {
		mode = "--ephemeral"
	}
	if err = syscall.Exec(app, []string{app, mode, path, revision}, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func contactResident(request openRequest) string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(os.TempDir(), "gitskim-"+strconv.Itoa(os.Getuid()))
	}
	connection, err := net.DialTimeout("unix", filepath.Join(runtimeDir, "gitskim.sock"), 250*time.Millisecond)
	if err != nil {
		return ""
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(250 * time.Millisecond))
	if err = json.NewEncoder(connection).Encode(request); err != nil {
		return ""
	}
	response, _ := bufio.NewReader(connection).ReadString('\n')
	return strings.TrimSpace(response)
}
