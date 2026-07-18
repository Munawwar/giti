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
	files                                             []changedFile
	upstreams                                         map[string]string
	graph                                             graphLayout
}

type commitDetails struct {
	sha, subject, body, author, authorEmail, authored, committer, committerEmail, committed string
	parents, branches, tags                                                                 []string
	upstreams                                                                               map[string]string
	additions, deletions, untracked                                                         int
	statistics                                                                              bool
}

type changedFile struct {
	status, path, oldPath string
	conflict              string
	additions, deletions  int
	binary, staged        bool
}

func sortedReferences(branches, tags []string) ([]string, []string) {
	branches, tags = append([]string(nil), branches...), append([]string(nil), tags...)
	sort.Slice(branches, func(left, right int) bool {
		leftHead := branches[left] == "HEAD" || strings.HasSuffix(branches[left], headRefSuffix)
		rightHead := branches[right] == "HEAD" || strings.HasSuffix(branches[right], headRefSuffix)
		if leftHead != rightHead {
			return leftHead
		}
		leftValue := strings.TrimSuffix(branches[left], headRefSuffix)
		rightValue := strings.TrimSuffix(branches[right], headRefSuffix)
		leftRemote := strings.HasPrefix(leftValue, remoteRefPrefix)
		rightRemote := strings.HasPrefix(rightValue, remoteRefPrefix)
		if leftRemote != rightRemote {
			return !leftRemote
		}
		leftName := strings.TrimPrefix(leftValue, remoteRefPrefix)
		rightName := strings.TrimPrefix(rightValue, remoteRefPrefix)
		if leftName != rightName {
			return leftName < rightName
		}
		return branches[left] < branches[right]
	})
	sort.Strings(tags)
	return branches, tags
}

func referenceMetadata(output string) (branches, tags []string, upstreams map[string]string) {
	upstreams = make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		fields := strings.SplitN(line, "\x00", 3)
		ref := fields[0]
		upstream := ""
		if len(fields) > 1 {
			upstream = fields[1]
		}
		switch {
		case strings.HasPrefix(ref, "refs/tags/"):
			tags = append(tags, strings.TrimPrefix(ref, "refs/tags/"))
		case strings.HasPrefix(ref, "refs/heads/"):
			branch := strings.TrimPrefix(ref, "refs/heads/")
			display := branch
			if len(fields) > 2 && strings.TrimSpace(fields[2]) == "*" {
				display += headRefSuffix
			}
			branches = append(branches, display)
			if upstream != "" {
				upstreams[branch] = upstream
			}
		case strings.HasPrefix(ref, remoteRefPrefix):
			branches = append(branches, ref)
		}
	}
	branches, tags = sortedReferences(branches, tags)
	return
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

type workingChanges struct {
	needsResolution, resolutionApplied, unstaged, staged []changedFile
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
	metadata, err := repo.runContext(ctx, "for-each-ref", "--format=%(refname)%00%(upstream)%00%(HEAD)", "refs/heads/")
	if err != nil {
		return nil, false, err
	}
	_, _, upstreams := referenceMetadata(metadata)
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
	changes, err := repo.workingChangesContext(ctx, ignoreWhitespace)
	if err != nil {
		return nil, false, err
	}
	rows := make([]historyRow, 0, count+4)
	for _, synthetic := range []historyRow{
		{kind: "conflict", subject: "Needs resolution", files: changes.needsResolution},
		{kind: "resolved", subject: "Resolution applied", files: changes.resolutionApplied},
		{kind: "unstaged", subject: "Unstaged changes", files: changes.unstaged},
		{kind: "staged", subject: "Staged changes", files: changes.staged},
	} {
		if len(synthetic.files) > 0 {
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
		row := historyRow{kind: "commit", revision: values[0], parents: strings.Fields(values[1]), author: values[2], date: values[3], timestamp: timestamp, refs: strings.TrimSpace(values[5]), subject: values[6], body: body, upstreams: upstreams}
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
	refs, err := repo.runContext(ctx, "for-each-ref", "--format=%(refname)%00%(upstream)%00%(HEAD)", "--points-at="+revision)
	if err != nil {
		return commitDetails{}, err
	}
	details.branches, details.tags, details.upstreams = referenceMetadata(refs)
	return details, nil
}

func (repo *repository) changedFiles(row historyRow, ignoreWhitespace bool) ([]changedFile, error) {
	return repo.changedFilesContext(context.Background(), row, ignoreWhitespace)
}

func (repo *repository) changedFilesContext(ctx context.Context, row historyRow, ignoreWhitespace bool) ([]changedFile, error) {
	return repo.changedFilesForViewContext(ctx, row, ignoreWhitespace, row.kind == "commit" && len(row.parents) > 1)
}

func (repo *repository) changedFilesForViewContext(ctx context.Context, row historyRow, ignoreWhitespace, mergeResolution bool) ([]changedFile, error) {
	if row.kind != "commit" {
		if row.files != nil {
			return append([]changedFile(nil), row.files...), nil
		}
		changes, err := repo.workingChangesContext(ctx, ignoreWhitespace)
		if err != nil {
			return nil, err
		}
		switch row.kind {
		case "conflict":
			return changes.needsResolution, nil
		case "resolved":
			return changes.resolutionApplied, nil
		case "staged":
			return changes.staged, nil
		default:
			return changes.unstaged, nil
		}
	}
	if mergeResolution && len(row.parents) > 1 {
		args := []string{"diff-tree", "--no-commit-id", "-r", "--cc", "--name-status", "-z"}
		if ignoreWhitespace {
			args = append(args, "--ignore-all-space")
		}
		output, err := repo.runContext(ctx, append(args, row.revision)...)
		if err != nil {
			return nil, err
		}
		// Combined --name-status -z emits one row per dense merge-resolution file:
		//
		//	<status-from-each-parent> NUL <path> NUL
		//
		// For a two-parent merge, MM means the result differs from both parents.
		parts, files := strings.Split(output, "\x00"), make([]changedFile, 0)
		for index := 0; index+1 < len(parts); index += 2 {
			if parts[index] != "" && parts[index+1] != "" {
				files = append(files, changedFile{status: parts[index], path: parts[index+1]})
			}
		}
		return files, nil
	}
	return repo.ordinaryChangedFilesContext(ctx, row, ignoreWhitespace)
}

func (repo *repository) ordinaryChangedFilesContext(ctx context.Context, row historyRow, ignoreWhitespace bool) ([]changedFile, error) {
	// Commit rows compare against the first parent, matching the single-parent
	// diff shown elsewhere; root commits compare against Git's empty tree.
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
	// --name-status -z emits:
	//
	//	<status> NUL <path> NUL
	//	<status> NUL <old-path> NUL <new-path> NUL  (rename or copy)
	//
	// Status is A added, M modified, D deleted, T type-changed, U unmerged,
	// R<score> renamed, C<score> copied, X unknown, or B pairing-broken.
	// Giti adds ?? separately for untracked files.
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
	// --numstat -z emits:
	//
	//	<added-lines> TAB <deleted-lines> TAB <path> NUL
	//	<added-lines> TAB <deleted-lines> TAB NUL <old-path> NUL <new-path> NUL
	//
	// Binary files use "-" for both line counts.
	statArgs := append(append([]string(nil), args...), "--numstat", "-z")
	if ignoreWhitespace {
		statArgs = append(statArgs, "--ignore-all-space")
	}
	visible, visibleErr := repo.runContext(ctx, statArgs...)
	if visibleErr != nil {
		return nil, visibleErr
	}
	type fileStat struct {
		additions, deletions int
		binary               bool
	}
	visiblePaths, stats := make(map[string]bool), make(map[string]fileStat)
	for parts, index := strings.Split(visible, "\x00"), 0; index < len(parts) && parts[index] != ""; {
		fields := strings.SplitN(parts[index], "\t", 3)
		index++
		if len(fields) != 3 {
			continue
		}
		paths := []string{fields[2]}
		if fields[2] == "" && index+1 < len(parts) {
			paths = []string{parts[index], parts[index+1]}
			index += 2
		}
		stat := fileStat{binary: fields[0] == "-" || fields[1] == "-"}
		if !stat.binary {
			stat.additions, _ = strconv.Atoi(fields[0])
			stat.deletions, _ = strconv.Atoi(fields[1])
		}
		for _, path := range paths {
			visiblePaths[path], stats[path] = true, stat
		}
	}
	// The whitespace-aware numstat result doubles as the visibility set. Untracked
	// files remain visible but deliberately receive no additions/deletions.
	filtered := files[:0]
	for _, file := range files {
		if stat, ok := stats[file.path]; ok {
			file.additions, file.deletions, file.binary = stat.additions, stat.deletions, stat.binary
		} else if stat, ok := stats[file.oldPath]; ok {
			file.additions, file.deletions, file.binary = stat.additions, stat.deletions, stat.binary
		}
		if !ignoreWhitespace || file.status == "??" || visiblePaths[file.path] || visiblePaths[file.oldPath] {
			filtered = append(filtered, file)
		}
	}
	return filtered, nil
}

func (repo *repository) workingChangesContext(ctx context.Context, ignoreWhitespace bool) (workingChanges, error) {
	unstaged, err := repo.ordinaryChangedFilesContext(ctx, historyRow{kind: "unstaged"}, ignoreWhitespace)
	if err != nil {
		return workingChanges{}, err
	}
	staged, err := repo.ordinaryChangedFilesContext(ctx, historyRow{kind: "staged"}, ignoreWhitespace)
	if err != nil {
		return workingChanges{}, err
	}
	status, err := repo.runContext(ctx, "status", "--porcelain=v2", "-z", "--untracked-files=no")
	if err != nil {
		return workingChanges{}, err
	}
	// Porcelain-v2 unmerged records are:
	//
	//	u <XY> <sub> <mode1> <mode2> <mode3> <worktree-mode> <hash1> <hash2> <hash3> <path> NUL
	//
	// XY is DD both deleted, AU added by us, UD deleted by them, UA added by
	// them, DU deleted by us, AA both added, or UU both modified.
	conflictNames := map[string]string{"DD": "Both deleted", "AU": "Added by us", "UD": "Deleted by them", "UA": "Added by them", "DU": "Deleted by us", "AA": "Both added", "UU": "Both modified"}
	conflicts, conflictPaths := make([]changedFile, 0), make(map[string]bool)
	for _, record := range strings.Split(status, "\x00") {
		fields := strings.SplitN(record, " ", 11)
		if len(fields) != 11 || fields[0] != "u" {
			continue
		}
		path := fields[10]
		conflictPaths[path] = true
		conflicts = append(conflicts, changedFile{status: "!", path: path, conflict: conflictNames[fields[1]]})
	}

	remaining := make(map[string]bool, len(conflicts))
	for path := range conflictPaths {
		remaining[path] = true
	}
	// Rerere leaves the index unmerged unless autoupdate is enabled. Its remaining
	// list is therefore what distinguishes a reused worktree resolution from a
	// conflict that still needs attention.
	if len(conflicts) > 0 {
		rrCache, pathErr := repo.runContext(ctx, "rev-parse", "--git-path", "rr-cache")
		path := strings.TrimSpace(rrCache)
		if path != "" && !filepath.IsAbs(path) {
			path = filepath.Join(repo.path, path)
		}
		_, statErr := os.Stat(path)
		hasNewline := false
		for conflictPath := range conflictPaths {
			hasNewline = hasNewline || strings.ContainsRune(conflictPath, '\n')
		}
		// `rerere remaining` is newline-delimited rather than -z. A newline in
		// any conflict path makes it ambiguous, so keep every conflict unresolved.
		if pathErr == nil && statErr == nil && !hasNewline {
			if output, remainingErr := repo.runContext(ctx, "rerere", "remaining"); remainingErr == nil {
				remaining = make(map[string]bool)
				for _, conflictPath := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
					if conflictPaths[conflictPath] {
						remaining[conflictPath] = true
					}
				}
			}
		}
	}

	changes := workingChanges{}
	for _, file := range conflicts {
		if remaining[file.path] {
			changes.needsResolution = append(changes.needsResolution, file)
		} else {
			file.status = "✓"
			changes.resolutionApplied = append(changes.resolutionApplied, file)
		}
	}

	// Once a resolution is staged, porcelain no longer reports an unmerged row.
	// During a merge, resolve-undo preserves its former stages so it can still be
	// separated from ordinary staged changes (whether rerere or the user staged it).
	mergeHead, _ := repo.runContext(ctx, "rev-parse", "--verify", "-q", "MERGE_HEAD")
	resolvedPaths := make(map[string]bool)
	if strings.TrimSpace(mergeHead) != "" {
		undo, undoErr := repo.runContext(ctx, "ls-files", "--resolve-undo", "-z")
		if undoErr != nil {
			return workingChanges{}, undoErr
		}
		// --resolve-undo -z emits one record for each former conflict stage:
		//
		//	<mode> SP <object> SP <stage> TAB <path> NUL
		//
		// Stage-presence masks identify DD=1, AU=2, UD=1+2, UA=3,
		// DU=1+3, AA=2+3, and UU=1+2+3 (stored below as 1<<stage).
		stages := make(map[string]int)
		for _, record := range strings.Split(undo, "\x00") {
			separator := strings.IndexByte(record, '\t')
			if separator < 0 {
				continue
			}
			metadata, path := strings.Fields(record[:separator]), record[separator+1:]
			if len(metadata) == 3 {
				stage, _ := strconv.Atoi(metadata[2])
				stages[path] |= 1 << stage
			}
		}
		stageStatus := map[int]string{2: "DD", 4: "AU", 6: "UD", 8: "UA", 10: "DU", 12: "AA", 14: "UU"}
		for path, stages := range stages {
			if conflictPaths[path] {
				continue
			}
			resolvedPaths[path] = true
			file := changedFile{status: "✓", path: path, conflict: conflictNames[stageStatus[stages]], staged: true}
			for _, candidate := range staged {
				if candidate.path == path || candidate.oldPath == path {
					file.path, file.oldPath = candidate.path, candidate.oldPath
					break
				}
			}
			changes.resolutionApplied = append(changes.resolutionApplied, file)
		}
	}

	// Conflict-related paths belong to exactly one of the two resolution lists,
	// never to the ordinary staged or unstaged lists shown below them.
	for _, source := range []struct {
		files  []changedFile
		target *[]changedFile
	}{{unstaged, &changes.unstaged}, {staged, &changes.staged}} {
		for _, file := range source.files {
			if !conflictPaths[file.path] && !conflictPaths[file.oldPath] && !resolvedPaths[file.path] && !resolvedPaths[file.oldPath] {
				*source.target = append(*source.target, file)
			}
		}
	}
	return changes, nil
}

func (repo *repository) diff(row historyRow, file changedFile, ignoreWhitespace, fullFile bool) (string, error) {
	return repo.diffContext(context.Background(), row, file, ignoreWhitespace, fullFile)
}

func (repo *repository) diffContext(ctx context.Context, row historyRow, file changedFile, ignoreWhitespace, fullFile bool) (string, error) {
	return repo.diffForViewContext(ctx, row, file, ignoreWhitespace, fullFile, row.kind == "commit" && len(row.parents) > 1)
}

func (repo *repository) diffForViewContext(ctx context.Context, row historyRow, file changedFile, ignoreWhitespace, fullFile, mergeResolution bool) (string, error) {
	if mergeResolution && row.kind == "commit" && len(row.parents) > 1 {
		args := []string{"diff-tree", "--no-commit-id", "-r", "-p", "--cc", "--no-ext-diff", "--no-color"}
		if ignoreWhitespace {
			args = append(args, "--ignore-all-space")
		}
		if fullFile {
			args = append(args, "--unified=999999")
		}
		args = append(args, row.revision, "--", file.path)
		output, truncated, err := repo.runLimitedContext(ctx, diffOutputLimit, true, args...)
		if err != nil {
			return "", err
		}
		if truncated {
			output += fmt.Sprintf("\n\n[Diff truncated at %d MiB]\n", diffOutputLimit/1024/1024)
		}
		return output, nil
	}
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
		if row.kind == "staged" || file.staged {
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
	if (row.kind == "unstaged" || file.conflict != "" && !file.staged) && !strings.HasPrefix(file.status, "D") {
		if info, err := os.Lstat(filepath.Join(repo.path, file.path)); err == nil {
			return info.Size()
		}
	}
	revisions := make([]string, 0, 2)
	if row.kind == "unstaged" || row.kind == "staged" || file.conflict != "" {
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
