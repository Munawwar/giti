package main

import (
	"bufio"
	"encoding/json"
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
		if data, readErr := os.ReadFile(filepath.Join(runtimeDir, "giti.pid")); readErr == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid != os.Getpid() {
				executablePath := filepath.Join("/proc", strconv.Itoa(pid), "exe")
				target, linkErr := os.Readlink(executablePath)
				if linkErr == nil && isGitiAppPath(target) {
					_ = syscall.Kill(pid, syscall.SIGTERM)
					deadline := time.Now().Add(2 * time.Second)
					_, linkErr = os.Readlink(executablePath)
					for linkErr == nil && time.Now().Before(deadline) {
						time.Sleep(10 * time.Millisecond)
						_, linkErr = os.Readlink(executablePath)
					}
					if linkErr == nil {
						_ = syscall.Kill(pid, syscall.SIGKILL)
						deadline = time.Now().Add(time.Second)
						_, linkErr = os.Readlink(executablePath)
						for linkErr == nil && time.Now().Before(deadline) {
							time.Sleep(10 * time.Millisecond)
							_, linkErr = os.Readlink(executablePath)
						}
						if linkErr == nil {
							fmt.Fprintf(os.Stderr, "could not stop Giti resident %d\n", pid)
							os.Exit(1)
						}
					}
				}
			}
		}
		_ = os.Remove(filepath.Join(runtimeDir, "giti.pid"))
		_ = os.Remove(filepath.Join(runtimeDir, "giti.sock"))
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
