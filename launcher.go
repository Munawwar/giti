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

func launch(args []string) {
	revision, foreground, force, err := launcherArguments(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	path, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	runtimeDir := runtimeDirectory()
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if force {
		if err = stopResident(runtimeDir, executable); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	response := ""
	if !force && !foreground {
		response = contactResident(openRequest{Path: path, Revision: revision})
	}
	if response == "OK" || response == "BUSY" {
		return
	}
	debug := strings.HasSuffix(filepath.Base(executable), "-debug")
	if response == "" && !debug && !foreground {
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
				command := exec.Command(executable, "--resident", path, revision)
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
	mode := "--resident"
	if foreground {
		mode = "--ephemeral"
	}
	if err = syscall.Exec(executable, []string{executable, mode, path, revision}, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func launcherArguments(args []string) (revision string, foreground, force bool, err error) {
	revision = "HEAD"
	usage := fmt.Errorf("usage: %s [-f|--foreground] [HEAD|branch|tag|sha] | -1", args[0])
	switch len(args) {
	case 1:
		return
	case 2:
		switch args[1] {
		case "-f", "--foreground":
			foreground = true
		case "-1":
			force = true
		default:
			if strings.HasPrefix(args[1], "-") {
				err = usage
			} else {
				revision = args[1]
			}
		}
	case 3:
		if args[1] != "-f" && args[1] != "--foreground" || strings.HasPrefix(args[2], "-") {
			err = usage
		} else {
			foreground, revision = true, args[2]
		}
	default:
		err = usage
	}
	return
}

func stopResident(runtimeDir, executable string) error {
	pidPath, socketPath := filepath.Join(runtimeDir, "giti.pid"), filepath.Join(runtimeDir, "giti.sock")
	data, readErr := os.ReadFile(pidPath)
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if readErr == nil && parseErr == nil && pid != os.Getpid() {
		executablePath := filepath.Join("/proc", strconv.Itoa(pid), "exe")
		target, linkErr := os.Readlink(executablePath)
		target = strings.TrimSuffix(target, " (deleted)")
		name := filepath.Base(target)
		legacy := filepath.Dir(target) == filepath.Dir(executable) && (name == "giti-app" || name == "giti-app-debug")
		if linkErr == nil && (target == executable || legacy) {
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
