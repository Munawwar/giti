package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gotk3/gotk3/glib"
)

type openRequest struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
}

type residentServer struct {
	application *gitSkim
	listener    net.Listener
	socketPath  string
}

func runtimeDirectory() string {
	if path := os.Getenv("XDG_RUNTIME_DIR"); path != "" {
		return path
	}
	return filepath.Join(os.TempDir(), "gitskim-"+strconv.Itoa(os.Getuid()))
}

func newResidentServer(app *gitSkim) *residentServer {
	return &residentServer{application: app, socketPath: filepath.Join(runtimeDirectory(), "gitskim.sock")}
}

func (server *residentServer) start() error {
	if err := os.MkdirAll(filepath.Dir(server.socketPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(server.socketPath)
	listener, err := net.Listen("unix", server.socketPath)
	if err != nil {
		return err
	}
	server.listener = listener
	if err = os.Chmod(server.socketPath, 0o600); err != nil {
		server.stop()
		return err
	}
	go server.serve()
	return nil
}

func (server *residentServer) serve() {
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		server.handle(connection)
	}
}

func (server *residentServer) handle(connection net.Conn) {
	defer connection.Close()
	var request openRequest
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&request); err != nil {
		return
	}
	server.application.stateMu.Lock()
	if server.application.busy {
		server.application.stateMu.Unlock()
		connection.Write([]byte("BUSY\n"))
		return
	}
	server.application.busy = true
	server.application.stateMu.Unlock()
	connection.Write([]byte("OK\n"))
	glib.IdleAdd(func() bool { return server.application.openRepository(request.Path, request.Revision) })
}

func (server *residentServer) stop() {
	if server.listener != nil {
		_ = server.listener.Close()
	}
	_ = os.Remove(server.socketPath)
}
