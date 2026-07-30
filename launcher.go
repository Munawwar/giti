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
	path, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	history, foreground, force, err := launcherArguments(args, path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
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
		response = contactResident(openRequest{Path: path, History: history})
	}
	if response == "OK" {
		return
	}
	debug := strings.HasSuffix(filepath.Base(executable), "-debug")
	mode := launchMode(response, foreground)
	historyJSON, err := json.Marshal(history)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if (response == "" || response == "BUSY") && !debug && !foreground {
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
				command := exec.Command(executable, mode, path, string(historyJSON))
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
	if err = syscall.Exec(executable, []string{executable, mode, path, string(historyJSON)}, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func launchMode(response string, foreground bool) string {
	if foreground || response == "BUSY" {
		return "--ephemeral"
	}
	return "--resident"
}

func launcherArguments(args []string, path string) (history historySpec, foreground, force bool, err error) {
	history.Revision = "HEAD"
	usage := fmt.Errorf("usage: %s [-f|--foreground] [--follow] [revision] [--] [path] | -1", args[0])
	positionals, paths, explicitPaths := make([]string, 0), make([]string, 0), false
	// Giti owns its process flags. Everything after -- is always a path, allowing
	// filenames beginning with a dash without forwarding arbitrary Git options.
	for index := 1; index < len(args); index++ {
		switch args[index] {
		case "-f", "--foreground":
			foreground = true
		case "-1":
			force = true
		case "--follow":
			history.Follow = true
		case "--":
			explicitPaths = true
			paths = append(paths, args[index+1:]...)
			index = len(args)
		default:
			if strings.HasPrefix(args[index], "-") {
				return history, false, false, usage
			}
			positionals = append(positionals, args[index])
		}
	}
	// A forced resident restart is deliberately exclusive of repository/history
	// arguments, so it cannot accidentally discard a requested open operation.
	if force {
		if foreground || history.Follow || explicitPaths || len(positionals) > 0 || len(paths) > 0 {
			return history, false, false, usage
		}
		return history, false, true, nil
	}
	if explicitPaths {
		if len(positionals) > 1 {
			return history, false, false, usage
		}
		if len(positionals) == 1 {
			history.Revision = positionals[0]
		}
	} else if len(positionals) > 0 {
		// Match Gitk's convenient no-separator form for one starting revision:
		// classify the first value as a commit when possible and the rest as paths.
		candidate := positionals[0]
		command := exec.Command("git", "-C", path, "rev-parse", "--verify", "--quiet", candidate+"^{commit}")
		isRevision := command.Run() == nil
		candidatePath := candidate
		if !filepath.IsAbs(candidatePath) {
			candidatePath = filepath.Join(path, candidatePath)
		}
		_, pathErr := os.Lstat(candidatePath)
		if isRevision && pathErr == nil {
			return history, false, false, fmt.Errorf("ambiguous argument %q: both a revision and a path; use -- before paths", candidate)
		}
		if isRevision {
			for _, value := range positionals[1:] {
				if exec.Command("git", "-C", path, "rev-parse", "--verify", "--quiet", value+"^{commit}").Run() == nil {
					return history, false, false, fmt.Errorf("only one starting revision is supported; use -- before a path named %q", value)
				}
			}
			history.Revision, paths = candidate, append(paths, positionals[1:]...)
		} else {
			if pathErr != nil {
				if strings.Contains(candidate, "..") {
					return history, false, false, errors.New("revision ranges are not supported yet")
				}
				commits, historyErr := exec.Command("git", "-C", path, "log", "-1", "--format=%H", "HEAD", "--", ":(literal)"+candidate).Output()
				if historyErr != nil || strings.TrimSpace(string(commits)) == "" {
					return history, false, false, fmt.Errorf("branch, tag, commit, or file %q does not exist in this repository; use -- before a literal file-history path", candidate)
				}
			}
			paths = append(paths, positionals...)
		}
	}
	// File mode intentionally accepts one literal file or directory. Follow has
	// the additional Git limitation that this path must identify a file.
	if len(paths) > 1 {
		return history, false, false, errors.New("file search supports one path")
	}
	if len(paths) == 1 {
		history.Path = paths[0]
	}
	if history.Follow && history.Path == "" {
		return history, false, false, fmt.Errorf("--follow requires exactly one file path")
	}
	if history.Follow {
		followPath := history.Path
		if !filepath.IsAbs(followPath) {
			followPath = filepath.Join(path, followPath)
		}
		if info, statErr := os.Stat(followPath); statErr == nil && info.IsDir() {
			return history, false, false, fmt.Errorf("--follow supports files, not directories")
		}
	}
	return history, foreground, false, nil
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
