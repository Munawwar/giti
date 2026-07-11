package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	fieldMarker     = "__GITSKIM_FIELD__"
	recordMarker    = "__GITSKIM_COMMIT__"
	emptyTree       = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	fullFileLimit   = 2 * 1024 * 1024
	diffOutputLimit = 8 * 1024 * 1024
)

type historyRow struct {
	kind, revision, graph, subject, refs string
}

type changedFile struct {
	status, path, oldPath string
}

func (file changedFile) label() string {
	prefix := ""
	if file.oldPath != "" {
		prefix = file.oldPath + " → "
	}
	return fmt.Sprintf("%-4s %s%s", file.status, prefix, file.path)
}

type repository struct {
	path, revision, revisionArg string
}

func newRepository(path, revision string) (*repository, error) {
	repo := &repository{path: path, revisionArg: revision}
	root, err := repo.runAt(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	repo.path = strings.TrimSpace(root)
	resolved, err := repo.run("rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return nil, err
	}
	repo.revision = strings.TrimSpace(resolved)
	return repo, nil
}

func (repo *repository) run(args ...string) (string, error) {
	return repo.runAt(repo.path, args...)
}

func (repo *repository) runAt(path string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", path}, args...)...)
	output, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", errors.New(strings.TrimSpace(string(exit.Stderr)))
		}
		return "", err
	}
	return string(output), nil
}

func (repo *repository) runLimited(limit int, check bool, args ...string) (string, bool, error) {
	command := exec.Command("git", append([]string{"-C", repo.path}, args...)...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		return "", false, err
	}
	var output bytes.Buffer
	_, readErr := io.CopyN(&output, stdout, int64(limit+1))
	truncated := output.Len() > limit
	if truncated {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", truncated, readErr
	}
	if check && waitErr != nil && !truncated {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = waitErr.Error()
		}
		return "", false, errors.New(message)
	}
	data := output.Bytes()
	if len(data) > limit {
		data = data[:limit]
	}
	return string(data), truncated, nil
}

func (repo *repository) history(count int, ignoreWhitespace bool) ([]historyRow, error) {
	format := recordMarker + "%H" + fieldMarker + "%P" + fieldMarker + "%D" + fieldMarker + "%s"
	output, err := repo.run("log", "--graph", "--topo-order", fmt.Sprintf("-n%d", count), "--format="+format, repo.revisionArg)
	if err != nil {
		return nil, err
	}
	rows := make([]historyRow, 0, count+2)
	for _, synthetic := range []historyRow{{kind: "unstaged", graph: "○", subject: "Unstaged changes"}, {kind: "staged", graph: "○", subject: "Staged changes"}} {
		files, fileErr := repo.changedFiles(synthetic, ignoreWhitespace)
		if fileErr != nil {
			return nil, fileErr
		}
		if len(files) > 0 {
			rows = append(rows, synthetic)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		graph, fields, found := strings.Cut(line, recordMarker)
		if !found {
			rows = append(rows, historyRow{kind: "connector", graph: strings.TrimRight(graph, " ")})
			continue
		}
		parts := strings.SplitN(fields, fieldMarker, 4)
		for len(parts) < 4 {
			parts = append(parts, "")
		}
		rows = append(rows, historyRow{kind: "commit", revision: parts[0], graph: strings.TrimRight(graph, " "), refs: strings.TrimSpace(parts[2]), subject: parts[3]})
	}
	return rows, nil
}

func (repo *repository) changedFiles(row historyRow, ignoreWhitespace bool) ([]changedFile, error) {
	args := []string{"diff", "--name-status", "--find-renames", "--find-copies"}
	if ignoreWhitespace {
		args = append(args, "--ignore-all-space")
	}
	if row.kind == "staged" {
		args = append(args, "--cached")
	} else if row.kind == "commit" {
		parents, err := repo.run("show", "-s", "--format=%P", row.revision)
		if err != nil {
			return nil, err
		}
		parent := emptyTree
		if fields := strings.Fields(parents); len(fields) > 0 {
			parent = fields[0]
		}
		args = append(args, parent, row.revision)
	}
	output, err := repo.run(args...)
	if err != nil {
		return nil, err
	}
	files := make([]changedFile, 0)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		file := changedFile{status: parts[0], path: parts[1]}
		if strings.ContainsAny(parts[0][:1], "RC") && len(parts) > 2 {
			file.oldPath, file.path = parts[1], parts[2]
		}
		files = append(files, file)
	}
	if row.kind == "unstaged" {
		untracked, trackErr := repo.run("ls-files", "--others", "--exclude-standard")
		if trackErr != nil {
			return nil, trackErr
		}
		for _, path := range strings.Split(strings.TrimSuffix(untracked, "\n"), "\n") {
			if path != "" {
				files = append(files, changedFile{status: "??", path: path})
			}
		}
	}
	filtered := files[:0]
	for _, file := range files {
		if file.status == "??" || !ignoreWhitespace {
			filtered = append(filtered, file)
			continue
		}
		patch, patchErr := repo.diff(row, file, true, false)
		if patchErr != nil {
			return nil, patchErr
		}
		if patch != "" {
			filtered = append(filtered, file)
		}
	}
	return filtered, nil
}

func (repo *repository) diff(row historyRow, file changedFile, ignoreWhitespace, fullFile bool) (string, error) {
	args := []string{"diff", "--no-ext-diff", "--no-color", "--find-renames", "--find-copies"}
	check := true
	if file.status == "??" {
		args, check = []string{"diff", "--no-index", "--no-color"}, false
	}
	if ignoreWhitespace {
		args = append(args, "--ignore-all-space")
	}
	if fullFile {
		args = append(args, "--unified=999999")
	}
	if file.status == "??" {
		args = append(args, "--", "/dev/null", file.path)
	} else {
		if row.kind == "staged" {
			args = append(args, "--cached")
		} else if row.kind == "commit" {
			parents, err := repo.run("show", "-s", "--format=%P", row.revision)
			if err != nil {
				return "", err
			}
			parent := emptyTree
			if fields := strings.Fields(parents); len(fields) > 0 {
				parent = fields[0]
			}
			args = append(args, parent, row.revision)
		}
		args = append(args, "--")
		if file.oldPath != "" {
			args = append(args, file.oldPath)
		}
		args = append(args, file.path)
	}
	output, truncated, err := repo.runLimited(diffOutputLimit, check, args...)
	if err != nil {
		return "", err
	}
	if truncated {
		output += fmt.Sprintf("\n\n[Diff truncated at %d MiB]\n", diffOutputLimit/1024/1024)
	}
	return output, nil
}

func (repo *repository) fileSize(row historyRow, file changedFile) int64 {
	if row.kind == "unstaged" && !strings.HasPrefix(file.status, "D") {
		if info, err := os.Lstat(filepath.Join(repo.path, file.path)); err == nil {
			return info.Size()
		}
	}
	revisions := make([]string, 0, 2)
	if row.kind == "unstaged" || row.kind == "staged" {
		revisions = append(revisions, ":"+file.path, "HEAD:"+file.path)
	} else {
		revisions = append(revisions, row.revision+":"+file.path)
		if parents, err := repo.run("show", "-s", "--format=%P", row.revision); err == nil {
			if fields := strings.Fields(parents); len(fields) > 0 {
				path := file.path
				if file.oldPath != "" {
					path = file.oldPath
				}
				revisions = append(revisions, fields[0]+":"+path)
			}
		}
	}
	for _, revision := range revisions {
		if output, err := repo.run("cat-file", "-s", revision); err == nil {
			if size, parseErr := strconv.ParseInt(strings.TrimSpace(output), 10, 64); parseErr == nil {
				return size
			}
		}
	}
	return 0
}
