package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
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
		fmt.Fprintf(os.Stderr, "usage: %s [HEAD|branch|tag|sha|-1]\n", os.Args[0])
		os.Exit(2)
	}
	path, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	revision, force := "HEAD", len(os.Args) == 2 && os.Args[1] == "-1"
	if len(os.Args) == 2 && !force {
		revision = os.Args[1]
	}
	runtimeDir := runtimeDirectory()
	if force {
		if err = stopResident(runtimeDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	response := ""
	if !force {
		response = contactResident(openRequest{Path: path, Revision: revision})
	}
	if response == "OK" || response == "BUSY" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	appName := "giti-app"
	debug := strings.HasSuffix(filepath.Base(executable), "-debug")
	if debug {
		appName += "-debug"
	}
	app := filepath.Join(filepath.Dir(executable), appName)
	if response == "" && !debug {
		if err = os.MkdirAll(runtimeDir, 0o700); err == nil {
			var input, log *os.File
			input, err = os.Open(os.DevNull)
			if input != nil {
				defer input.Close()
			}
			if err == nil {
				log, err = os.OpenFile(filepath.Join(runtimeDir, "giti.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			}
			if log != nil {
				defer log.Close()
			}
			if err == nil {
				command := exec.Command(app, "--resident", path, revision)
				command.Stdin, command.Stdout, command.Stderr = input, log, log
				command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				if err = command.Start(); err == nil {
					err = command.Process.Release()
				}
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err = syscall.Exec(app, []string{app, "--resident", path, revision}, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func isGitiAppPath(target string) bool {
	name := filepath.Base(strings.TrimSuffix(target, " (deleted)"))
	return name == "giti-app" || name == "giti-app-debug"
}

func stopResident(runtimeDir string) error {
	pidPath, socketPath := filepath.Join(runtimeDir, "giti.pid"), filepath.Join(runtimeDir, "giti.sock")
	data, readErr := os.ReadFile(pidPath)
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if readErr == nil && parseErr == nil && pid != os.Getpid() {
		executablePath := filepath.Join("/proc", strconv.Itoa(pid), "exe")
		target, linkErr := os.Readlink(executablePath)
		if linkErr == nil && isGitiAppPath(target) {
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("stop Giti resident %d: %w", pid, err)
			}
			if !waitForExit(executablePath, 2*time.Second) {
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
					return fmt.Errorf("kill Giti resident %d: %w", pid, err)
				}
				if !waitForExit(executablePath, time.Second) {
					return fmt.Errorf("could not stop Giti resident %d", pid)
				}
			}
		}
	}
	lock, err := os.OpenFile(filepath.Join(runtimeDir, "giti.lock"), os.O_RDWR, 0)
	if err == nil {
		defer lock.Close()
		if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return errors.New("a Giti resident still owns the process lock; coordination files were preserved")
		}
		if err != nil {
			return fmt.Errorf("check Giti resident lock: %w", err)
		}
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("open Giti resident lock: %w", err)
	}
	_ = os.Remove(pidPath)
	_ = os.Remove(socketPath)
	return nil
}

func waitForExit(executablePath string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	_, err := os.Readlink(executablePath)
	for err == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		_, err = os.Readlink(executablePath)
	}
	return err != nil
}

func contactResident(request openRequest) string {
	connection, err := net.DialTimeout("unix", filepath.Join(runtimeDirectory(), "giti.sock"), 250*time.Millisecond)
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

func runtimeDirectory() string {
	if path := os.Getenv("XDG_RUNTIME_DIR"); path != "" {
		return path
	}
	return filepath.Join(os.TempDir(), "giti-"+strconv.Itoa(os.Getuid()))
}
