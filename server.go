package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type openRequest struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
}

type residentServer struct {
	application *giti
	listener    net.Listener
	lockFile    *os.File
	socketPath  string
}

func runtimeDirectory() string {
	if path := os.Getenv("XDG_RUNTIME_DIR"); path != "" {
		return path
	}
	return filepath.Join(os.TempDir(), "giti-"+strconv.Itoa(os.Getuid()))
}

func newResidentServer(app *giti) *residentServer {
	return &residentServer{application: app, socketPath: filepath.Join(runtimeDirectory(), "giti.sock")}
}

func (server *residentServer) start() error {
	runtimeDir := filepath.Dir(server.socketPath)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(runtimeDir, "giti.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	server.lockFile = lock
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		server.lockFile = nil
		return errors.New("another Giti resident is already running")
	}
	_ = os.Remove(server.socketPath)
	listener, err := net.Listen("unix", server.socketPath)
	if err != nil {
		server.stop()
		return err
	}
	server.listener = listener
	if err = os.Chmod(server.socketPath, 0o600); err == nil {
		err = os.WriteFile(filepath.Join(runtimeDir, "giti.pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
	}
	if err != nil {
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
		go server.handle(connection)
	}
}

func (server *residentServer) handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	var request openRequest
	if err := json.NewDecoder(io.LimitReader(connection, 64*1024)).Decode(&request); err != nil {
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
	addMainSource(0, func() bool { return server.application.openRepository(request.Path, request.Revision) })
}

func (server *residentServer) stop() {
	if server.listener != nil {
		_ = server.listener.Close()
	}
	_ = os.Remove(server.socketPath)
	_ = os.Remove(filepath.Join(filepath.Dir(server.socketPath), "giti.pid"))
	if server.lockFile != nil {
		_ = syscall.Flock(int(server.lockFile.Fd()), syscall.LOCK_UN)
		_ = server.lockFile.Close()
		server.lockFile = nil
	}
}
