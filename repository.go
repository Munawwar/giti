package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	fieldMarker     = "__GITI_FIELD__"
	recordMarker    = "__GITI_COMMIT__"
	emptyTree       = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	fullFileLimit   = 2 * 1024 * 1024
	diffOutputLimit = 8 * 1024 * 1024
	remoteRefPrefix = "refs/remotes/"
	headRefSuffix   = " <- HEAD"
)

type historyRow struct {
	kind, revision, subject, body, refs, author, date string
	timestamp                                         int64
	parents                                           []string
	graph                                             graphLayout
}

type commitDetails struct {
	sha, subject, body, author, authorEmail, authored, committer, committerEmail, committed string
	parents, branches, tags                                                                 []string
}

type changedFile struct {
	status, path, oldPath string
}

func sortedReferences(branches, tags []string) ([]string, []string) {
	branches, tags = append([]string(nil), branches...), append([]string(nil), tags...)
	sort.Slice(branches, func(left, right int) bool {
		leftHead := branches[left] == "HEAD" || strings.HasSuffix(branches[left], headRefSuffix)
		rightHead := branches[right] == "HEAD" || strings.HasSuffix(branches[right], headRefSuffix)
		if leftHead != rightHead {
			return leftHead
		}
		leftName := strings.TrimPrefix(strings.TrimSuffix(branches[left], headRefSuffix), remoteRefPrefix)
		rightName := strings.TrimPrefix(strings.TrimSuffix(branches[right], headRefSuffix), remoteRefPrefix)
		if leftName != rightName {
			return leftName < rightName
		}
		return branches[left] < branches[right]
	})
	sort.Strings(tags)
	return branches, tags
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
	return repo.runContext(context.Background(), args...)
}

func (repo *repository) runContext(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", repo.path}, args...)...)
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", errors.New(strings.TrimSpace(string(exit.Stderr)))
		}
	}
	return string(output), err
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
	return repo.runLimitedContext(context.Background(), limit, check, args...)
}

func (repo *repository) runLimitedContext(ctx context.Context, limit int, check bool, args ...string) (string, bool, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", repo.path}, args...)...)
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
	if ctx.Err() != nil {
		return "", truncated, ctx.Err()
	}
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

func (repo *repository) history(count int, ignoreWhitespace, includeMessages bool) ([]historyRow, bool, error) {
	format := recordMarker + strings.Join([]string{"%H", "%P", "%an", "%as", "%at", "%D", "%s"}, fieldMarker)
	output, err := repo.run("log", "--topo-order", "--decorate=full", fmt.Sprintf("-n%d", count+1), "--format="+format, repo.revisionArg)
	if err != nil {
		return nil, false, err
	}
	var bodies map[string]string
	if includeMessages {
		bodies = make(map[string]string)
		messages, messageErr := repo.run("log", "--topo-order", fmt.Sprintf("-n%d", count+1), "--format=%x00%H%x00%b", repo.revisionArg)
		if messageErr != nil {
			return nil, false, messageErr
		}
		fields := strings.Split(messages, "\x00")
		for index := 1; index+1 < len(fields); index += 2 {
			bodies[fields[index]] = strings.TrimSpace(fields[index+1])
		}
	}
	rows := make([]historyRow, 0, count+2)
	for _, synthetic := range []historyRow{{kind: "unstaged", subject: "Unstaged changes"}, {kind: "staged", subject: "Staged changes"}} {
		files, fileErr := repo.changedFiles(synthetic, ignoreWhitespace)
		if fileErr != nil {
			return nil, false, fileErr
		}
		if len(files) > 0 {
			rows = append(rows, synthetic)
		}
	}
	commits := make([]historyRow, 0, count+1)
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		fields := strings.TrimPrefix(line, recordMarker)
		parts := strings.SplitN(fields, fieldMarker, 7)
		for len(parts) < 7 {
			parts = append(parts, "")
		}
		timestamp, _ := strconv.ParseInt(parts[4], 10, 64)
		commits = append(commits, historyRow{kind: "commit", revision: parts[0], parents: strings.Fields(parts[1]), author: parts[2], date: parts[3], timestamp: timestamp, refs: strings.TrimSpace(parts[5]), subject: parts[6], body: bodies[parts[0]]})
	}
	layoutGraph(commits)
	hasMore := len(commits) > count
	if hasMore {
		commits = commits[:count]
	}
	return append(rows, commits...), hasMore, nil
}

func (repo *repository) commitDetailsContext(ctx context.Context, revision string) (commitDetails, error) {
	format := strings.Join([]string{"%H", "%s", "%an", "%ae", "%aI", "%cn", "%ce", "%cI", "%P", "%b"}, fieldMarker)
	output, err := repo.runContext(ctx, "show", "-s", "--format="+format, revision)
	if err != nil {
		return commitDetails{}, err
	}
	parts := strings.SplitN(strings.TrimSuffix(output, "\n"), fieldMarker, 10)
	for len(parts) < 10 {
		parts = append(parts, "")
	}
	details := commitDetails{sha: parts[0], subject: parts[1], author: parts[2], authorEmail: parts[3], authored: parts[4], committer: parts[5], committerEmail: parts[6], committed: parts[7], parents: strings.Fields(parts[8]), body: strings.TrimSpace(parts[9])}
	refs, err := repo.runContext(ctx, "for-each-ref", "--format=%(refname)", "--points-at="+revision)
	if err != nil {
		return commitDetails{}, err
	}
	for _, ref := range strings.Fields(refs) {
		switch {
		case strings.HasPrefix(ref, "refs/tags/"):
			details.tags = append(details.tags, strings.TrimPrefix(ref, "refs/tags/"))
		case strings.HasPrefix(ref, "refs/heads/"):
			details.branches = append(details.branches, strings.TrimPrefix(ref, "refs/heads/"))
		case strings.HasPrefix(ref, "refs/remotes/"):
			details.branches = append(details.branches, ref)
		}
	}
	details.branches, details.tags = sortedReferences(details.branches, details.tags)
	return details, nil
}

func (repo *repository) changedFiles(row historyRow, ignoreWhitespace bool) ([]changedFile, error) {
	return repo.changedFilesContext(context.Background(), row, ignoreWhitespace)
}

func (repo *repository) changedFilesContext(ctx context.Context, row historyRow, ignoreWhitespace bool) ([]changedFile, error) {
	args := []string{"diff", "--name-status", "--find-renames", "--find-copies"}
	if ignoreWhitespace {
		args = append(args, "--ignore-all-space")
	}
	if row.kind == "staged" {
		args = append(args, "--cached")
	} else if row.kind == "commit" {
		parents, err := repo.runContext(ctx, "show", "-s", "--format=%P", row.revision)
		if err != nil {
			return nil, err
		}
		parent := emptyTree
		if fields := strings.Fields(parents); len(fields) > 0 {
			parent = fields[0]
		}
		args = append(args, parent, row.revision)
	}
	output, err := repo.runContext(ctx, args...)
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
		untracked, trackErr := repo.runContext(ctx, "ls-files", "--others", "--exclude-standard")
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
		patch, patchErr := repo.diffContext(ctx, row, file, true, false)
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
	return repo.diffContext(context.Background(), row, file, ignoreWhitespace, fullFile)
}

func (repo *repository) diffContext(ctx context.Context, row historyRow, file changedFile, ignoreWhitespace, fullFile bool) (string, error) {
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
			parents, err := repo.runContext(ctx, "show", "-s", "--format=%P", row.revision)
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
	output, truncated, err := repo.runLimitedContext(ctx, diffOutputLimit, check, args...)
	if err != nil {
		return "", err
	}
	if truncated {
		output += fmt.Sprintf("\n\n[Diff truncated at %d MiB]\n", diffOutputLimit/1024/1024)
	}
	return output, nil
}

func (repo *repository) fileSize(row historyRow, file changedFile) int64 {
	return repo.fileSizeContext(context.Background(), row, file)
}

func (repo *repository) fileSizeContext(ctx context.Context, row historyRow, file changedFile) int64 {
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
		if parents, err := repo.runContext(ctx, "show", "-s", "--format=%P", row.revision); err == nil {
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
		if output, err := repo.runContext(ctx, "cat-file", "-s", revision); err == nil {
			if size, parseErr := strconv.ParseInt(strings.TrimSpace(output), 10, 64); parseErr == nil {
				return size
			}
		}
	}
	return 0
}
