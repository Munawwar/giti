package main

import (
	"bufio"
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
	emptyTree          = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	fullFileLimit      = 2 * 1024 * 1024
	diffOutputLimit    = 8 * 1024 * 1024
	historyOutputLimit = 32 * 1024 * 1024
	remoteRefPrefix    = "refs/remotes/"
	headRefSuffix      = " <- HEAD"
)

type historyRow struct {
	kind, revision, subject, body, refs, author, date string
	searchSubject, searchBody, searchRefs             string
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
	return repo.historyContext(context.Background(), count, ignoreWhitespace, includeMessages)
}

func (repo *repository) historyIndexContext(ctx context.Context, revision string, limit int) (int, bool, error) {
	command := exec.CommandContext(ctx, "git", "-C", repo.path, "rev-list", "--topo-order", repo.revisionArg)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return -1, false, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		return -1, false, err
	}
	scanner := bufio.NewScanner(stdout)
	for index := 0; scanner.Scan(); index++ {
		if scanner.Text() == revision {
			_ = command.Process.Kill()
			_ = command.Wait()
			return index, false, nil
		}
		if index+1 >= limit {
			_ = command.Process.Kill()
			_ = command.Wait()
			return -1, true, nil
		}
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return -1, false, ctx.Err()
	}
	if err = scanner.Err(); err != nil {
		return -1, false, err
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = waitErr.Error()
		}
		return -1, false, errors.New(message)
	}
	return -1, false, nil
}

func (repo *repository) historyContext(ctx context.Context, count int, ignoreWhitespace, includeMessages bool) ([]historyRow, bool, error) {
	fields := []string{"%H", "%P", "%an", "%as", "%at", "%D", "%s"}
	if includeMessages {
		fields = append(fields, "%b")
	}
	format := strings.Join(fields, "%x00")
	output, truncated, err := repo.runLimitedContext(ctx, historyOutputLimit, true, "log", "-z", "--topo-order", "--decorate=full", fmt.Sprintf("-n%d", count+1), "--format="+format, repo.revisionArg)
	if err != nil {
		return nil, false, err
	}
	if truncated {
		return nil, false, fmt.Errorf("history metadata exceeds %d MiB; disable commit-description search or load fewer commits", historyOutputLimit/1024/1024)
	}
	rows := make([]historyRow, 0, count+2)
	for _, synthetic := range []historyRow{{kind: "unstaged", subject: "Unstaged changes"}, {kind: "staged", subject: "Staged changes"}} {
		files, fileErr := repo.changedFilesContext(ctx, synthetic, ignoreWhitespace)
		if fileErr != nil {
			return nil, false, fileErr
		}
		if len(files) > 0 {
			rows = append(rows, synthetic)
		}
	}
	commits, parts, width := make([]historyRow, 0, count+1), strings.Split(output, "\x00"), len(fields)
	for index := 0; index+width <= len(parts); index += width {
		values := parts[index : index+width]
		timestamp, _ := strconv.ParseInt(values[4], 10, 64)
		body := ""
		if includeMessages {
			body = strings.TrimSpace(values[7])
		}
		row := historyRow{kind: "commit", revision: values[0], parents: strings.Fields(values[1]), author: values[2], date: values[3], timestamp: timestamp, refs: strings.TrimSpace(values[5]), subject: values[6], body: body}
		row.searchSubject, row.searchBody, row.searchRefs = strings.ToLower(row.subject), strings.ToLower(row.body), strings.ToLower(row.refs)
		commits = append(commits, row)
	}
	layoutGraph(commits)
	hasMore := len(commits) > count
	if hasMore {
		commits = commits[:count]
	}
	return append(rows, commits...), hasMore, nil
}

func (repo *repository) commitDetailsContext(ctx context.Context, revision string) (commitDetails, error) {
	format := strings.Join([]string{"%H", "%s", "%an", "%ae", "%aI", "%cn", "%ce", "%cI", "%P", "%b"}, "%x00")
	output, err := repo.runContext(ctx, "show", "-s", "--format="+format, revision)
	if err != nil {
		return commitDetails{}, err
	}
	parts := strings.SplitN(strings.TrimSuffix(output, "\n"), "\x00", 10)
	for len(parts) < 10 {
		parts = append(parts, "")
	}
	details := commitDetails{sha: parts[0], subject: parts[1], author: parts[2], authorEmail: parts[3], authored: parts[4], committer: parts[5], committerEmail: parts[6], committed: parts[7], parents: strings.Fields(parts[8]), body: strings.TrimSpace(parts[9])}
	refs, err := repo.runContext(ctx, "for-each-ref", "--format=%(refname)", "--points-at="+revision)
	if err != nil {
		return commitDetails{}, err
	}
	for _, ref := range strings.Split(strings.TrimSuffix(refs, "\n"), "\n") {
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
	args := []string{"diff", "--find-renames", "--find-copies"}
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
	statusArgs := append(append([]string(nil), args...), "--name-status", "-z")
	output, err := repo.runContext(ctx, statusArgs...)
	if err != nil {
		return nil, err
	}
	files := make([]changedFile, 0)
	parts := strings.Split(output, "\x00")
	for index := 0; index+1 < len(parts); {
		file := changedFile{status: parts[index], path: parts[index+1]}
		index += 2
		if file.status != "" && strings.ContainsAny(file.status[:1], "RC") && index < len(parts) {
			file.oldPath, file.path = file.path, parts[index]
			index++
		}
		files = append(files, file)
	}
	if row.kind == "unstaged" {
		untracked, trackErr := repo.runContext(ctx, "ls-files", "--others", "--exclude-standard", "-z")
		if trackErr != nil {
			return nil, trackErr
		}
		for _, path := range strings.Split(untracked, "\x00") {
			if path != "" {
				files = append(files, changedFile{status: "??", path: path})
			}
		}
	}
	if !ignoreWhitespace {
		return files, nil
	}
	visible, visibleErr := repo.runContext(ctx, append(append([]string(nil), args...), "--numstat", "-z", "--ignore-all-space")...)
	if visibleErr != nil {
		return nil, visibleErr
	}
	visiblePaths := make(map[string]bool)
	for parts, index := strings.Split(visible, "\x00"), 0; index < len(parts) && parts[index] != ""; index++ {
		fields := strings.SplitN(parts[index], "\t", 3)
		if len(fields) != 3 {
			continue
		}
		if fields[2] == "" && index+2 < len(parts) {
			visiblePaths[parts[index+1]], visiblePaths[parts[index+2]] = true, true
			index += 2
		} else {
			visiblePaths[fields[2]] = true
		}
	}
	filtered := files[:0]
	for _, file := range files {
		if file.status == "??" || visiblePaths[file.path] || visiblePaths[file.oldPath] {
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
