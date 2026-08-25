package main

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/gtk"
)

func referenceLists(refs string) (branches, tags []string) {
	for _, decoration := range strings.Split(refs, ", ") {
		decoration = strings.TrimSpace(decoration)
		switch {
		case strings.HasPrefix(decoration, "tag: "):
			tags = append(tags, strings.TrimPrefix(strings.TrimPrefix(decoration, "tag: "), "refs/tags/"))
		case strings.HasPrefix(decoration, "HEAD -> "):
			branch := strings.TrimPrefix(decoration, "HEAD -> ")
			branch = strings.TrimPrefix(strings.TrimPrefix(branch, "refs/heads/"), remoteRefPrefix)
			branches = append(branches, branch+headRefSuffix)
		case strings.HasPrefix(decoration, "refs/heads/"):
			branches = append(branches, strings.TrimPrefix(decoration, "refs/heads/"))
		case strings.HasPrefix(decoration, remoteRefPrefix):
			branches = append(branches, decoration)
		case decoration != "":
			branches = append(branches, decoration)
		}
	}
	return sortedReferences(branches, tags)
}

func referenceBadge(value, kind string) string {
	value, _, background, foreground := referenceAppearance(value, kind)
	return fmt.Sprintf(`<span background="%s" foreground="%s" weight="bold"> %s </span>`, background, foreground, html.EscapeString(value))
}

func referenceAppearance(value, kind string) (display, style, background, foreground string) {
	style, background, foreground = "giti-ref-local", "#d8f0dd", "#1f5131"
	if kind == "tag" {
		style, background, foreground = "giti-ref-tag", "#f8e7a3", "#594600"
	} else if strings.HasPrefix(value, remoteRefPrefix) {
		style, background, foreground = "giti-ref-remote", "#dce8f8", "#244e7a"
		value = strings.TrimPrefix(value, remoteRefPrefix)
	} else if value == "HEAD" {
		style, background, foreground = "giti-ref-head", "#e5e7eb", "#374151"
	}
	if kind == "branch" {
		value = strings.Replace(value, headRefSuffix, " ← HEAD", 1)
	}
	return value, style, background, foreground
}

type referencePart struct {
	markup   string
	label    string
	branches []string
	segments []string
	synced   bool
	overflow bool
}

type remoteBranch struct {
	value, display, remote, path string
}

func syncedBranchBadges(local string, remote remoteBranch) (combined, remoteBadge, localBadge string) {
	localDisplay, _, localBackground, localForeground := referenceAppearance(local, "branch")
	_, _, remoteBackground, remoteForeground := referenceAppearance(remote.value, "branch")
	prefix := strings.TrimSuffix(remote.display, remote.path)
	remoteBadge = fmt.Sprintf(`<span background="%s" foreground="%s" weight="bold"> %s</span>`, remoteBackground, remoteForeground, html.EscapeString(prefix))
	localBadge = fmt.Sprintf(`<span background="%s" foreground="%s" weight="bold">%s </span>`, localBackground, localForeground, html.EscapeString(localDisplay))
	return remoteBadge + localBadge, remoteBadge, localBadge
}

func branchReferenceParts(branches []string, upstreams map[string]string) []referencePart {
	const displayLimit = 4
	// Normalize local and remote ordering before pairing them; Git decoration
	// order is presentation-oriented and is not stable enough for this layout.
	locals, remotes := make([]string, 0, len(branches)), make([]remoteBranch, 0, len(branches))
	for _, branch := range branches {
		if !strings.HasPrefix(branch, remoteRefPrefix) {
			locals = append(locals, branch)
			continue
		}
		display := strings.TrimPrefix(branch, remoteRefPrefix)
		remote, path := display, ""
		if separator := strings.IndexByte(display, '/'); separator >= 0 {
			remote, path = display[:separator], display[separator+1:]
		}
		remotes = append(remotes, remoteBranch{branch, display, remote, path})
	}
	locals, _ = sortedReferences(locals, nil)
	sort.Slice(remotes, func(left, right int) bool {
		if remotes[left].path != remotes[right].path {
			return remotes[left].path < remotes[right].path
		}
		if remotes[left].remote != remotes[right].remote {
			return remotes[left].remote < remotes[right].remote
		}
		return remotes[left].value < remotes[right].value
	})
	// A remote is emitted once: preferably joined to its configured upstream,
	// otherwise adjacent to a same-path local branch, then as an unmatched ref.
	byValue, used := make(map[string]remoteBranch, len(remotes)), make(map[string]bool, len(remotes))
	for _, remote := range remotes {
		byValue[remote.value] = remote
	}
	parts := make([]referencePart, 0, len(branches)+1)
	for _, local := range locals {
		name := strings.TrimSuffix(local, headRefSuffix)
		upstream, tracks := byValue[upstreams[name]]
		tracks = tracks && !used[upstream.value]
		if tracks && name != "HEAD" && upstream.path == name {
			markup, remoteBadge, localBadge := syncedBranchBadges(local, upstream)
			parts = append(parts, referencePart{markup: markup, branches: []string{upstream.value, local}, segments: []string{remoteBadge, localBadge}, synced: true})
			used[upstream.value] = true
		} else {
			parts = append(parts, referencePart{markup: referenceBadge(local, "branch"), branches: []string{local}})
			if tracks {
				parts = append(parts, referencePart{markup: referenceBadge(upstream.value, "branch"), branches: []string{upstream.value}})
				used[upstream.value] = true
			}
		}
		if name == "HEAD" {
			continue
		}
		for _, remote := range remotes {
			if !used[remote.value] && remote.path == name {
				parts = append(parts, referencePart{markup: referenceBadge(remote.value, "branch"), branches: []string{remote.value}})
				used[remote.value] = true
			}
		}
	}
	for _, remote := range remotes {
		if !used[remote.value] {
			parts = append(parts, referencePart{markup: referenceBadge(remote.value, "branch"), branches: []string{remote.value}})
		}
	}
	// Collapse only after pairing so the overflow count reflects hidden branch
	// names rather than the smaller number of joined visual parts.
	if len(parts) <= displayLimit {
		return parts
	}
	hidden := 0
	for _, part := range parts[displayLimit:] {
		hidden += len(part.branches)
	}
	label := fmt.Sprintf("+%d more branches", hidden)
	return append(parts[:displayLimit], referencePart{markup: `<span foreground="#4b5563" weight="bold">` + label + `</span>`, label: label, overflow: true})
}

func referenceParts(branches, tags []string, upstreams map[string]string) []referencePart {
	parts := branchReferenceParts(branches, upstreams)
	for _, tag := range tags[:min(2, len(tags))] {
		parts = append(parts, referencePart{markup: referenceBadge(tag, "tag")})
	}
	if len(tags) > 2 {
		parts = append(parts, referencePart{markup: referenceBadge("+ more tags", "tag"), overflow: true})
	}
	return parts
}

func historyReferenceParts(row historyRow) []referencePart {
	branches, tags := referenceLists(row.refs)
	parts := make([]referencePart, 0, 6)
	if len(tags) == 1 {
		parts = append(parts, referencePart{markup: referenceBadge(tags[0], "tag")})
	} else if len(tags) > 1 {
		parts = append(parts, referencePart{markup: referenceBadge(fmt.Sprintf("%d tags", len(tags)), "tag"), overflow: true})
	}
	return append(parts, branchReferenceParts(branches, row.upstreams)...)
}

func historyLabel(row historyRow) string {
	if row.kind != "commit" {
		return "<b>" + html.EscapeString(row.subject) + "</b>"
	}
	var refs strings.Builder
	for _, part := range historyReferenceParts(row) {
		refs.WriteString(part.markup)
		refs.WriteString("  ")
	}
	age, when := time.Since(time.Unix(row.timestamp, 0)), "just now"
	switch {
	case age < time.Second:
	case age < time.Minute:
		seconds := int(age / time.Second)
		unit := "sec"
		if seconds != 1 {
			unit += "s"
		}
		when = fmt.Sprintf("%d %s ago", seconds, unit)
	case age < time.Hour:
		minutes := int(age / time.Minute)
		unit := "min"
		if minutes != 1 {
			unit += "s"
		}
		when = fmt.Sprintf("%d %s ago", minutes, unit)
	case age < 24*time.Hour:
		hours := int(age / time.Hour)
		plural := ""
		if hours != 1 {
			plural = "s"
		}
		when = fmt.Sprintf("%d hour%s ago", hours, plural)
	default:
		date, err := time.Parse("2006-01-02", row.date)
		if err != nil {
			date = time.Unix(row.timestamp, 0)
		}
		when = date.Format("Jan 2, 2006")
	}
	return fmt.Sprintf("%s<b>%s</b>\n<span foreground=\"#374151\"><tt>%s</tt>  ·  %s  ·  %s</span>", refs.String(), html.EscapeString(row.subject), html.EscapeString(row.revision[:7]), html.EscapeString(row.author), when)
}

func formatCommitTime(value string, location *time.Location, layout string) string {
	date, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return date.In(location).Format(layout)
}

type searchMatch struct {
	row                            historyRow
	branches, tags                 []string
	score, index                   int
	matchesDescription, matchesSHA bool
}

type searchOptions struct {
	messages, references bool
}

func searchHistory(rows []historyRow, query string, options searchOptions) []searchMatch {
	phrase := strings.ToLower(strings.TrimSpace(query))
	if phrase == "" {
		return nil
	}
	words := strings.Fields(phrase)
	matches := make([]searchMatch, 0, len(rows))
	type field struct {
		text               string
		phraseScore, score int
	}
	for index, row := range rows {
		if row.kind != "commit" {
			continue
		}
		subject, body, refs := row.searchSubject, row.searchBody, row.searchRefs
		if subject == "" && row.subject != "" {
			subject = strings.ToLower(row.subject)
		}
		if body == "" && row.body != "" {
			body = strings.ToLower(row.body)
		}
		if refs == "" && row.refs != "" {
			refs = strings.ToLower(row.refs)
		}
		shaMatch := isSHAQuery(phrase) && strings.HasPrefix(strings.ToLower(row.revision), phrase)
		// Exact phrases outrank repeated word hits; subjects outrank references,
		// which outrank descriptions, and an explicit SHA prefix wins overall.
		fields := []field{{subject, 1000, 100}}
		if options.references {
			fields = append(fields, field{refs, 750, 50})
		}
		if options.messages {
			fields = append(fields, field{body, 500, 25})
		}
		score, descriptionScore := 0, 0
		for fieldIndex, field := range fields {
			text := field.text
			fieldScore := 0
			if strings.Contains(text, phrase) {
				fieldScore += field.phraseScore
			}
			for _, word := range words {
				fieldScore += strings.Count(text, word) * field.score
			}
			score += fieldScore
			if options.messages && fieldIndex == len(fields)-1 {
				descriptionScore = fieldScore
			}
		}
		if shaMatch {
			score += 2000
		}
		if score > 0 {
			// Results only need the match hints; commit details are loaded on
			// selection, so do not retain potentially large descriptions.
			row.body, row.searchSubject, row.searchBody, row.searchRefs = "", "", "", ""
			match := searchMatch{row: row, score: score, index: index, matchesDescription: descriptionScore > 0, matchesSHA: shaMatch}
			if options.references {
				branches, tags := referenceLists(row.refs)
				matchesQuery := func(value string) bool {
					value = strings.ToLower(value)
					if strings.Contains(value, phrase) {
						return true
					}
					for _, word := range words {
						if strings.Contains(value, word) {
							return true
						}
					}
					return false
				}
				for _, branch := range branches {
					if matchesQuery(branch) {
						match.branches = append(match.branches, branch)
					}
				}
				for _, tag := range tags {
					if matchesQuery(tag) {
						match.tags = append(match.tags, tag)
					}
				}
			}
			matches = append(matches, match)
		}
	}
	sort.SliceStable(matches, func(left, right int) bool { return searchMatchBefore(matches[left], matches[right]) })
	return matches
}

func searchMatchBefore(left, right searchMatch) bool {
	if left.score != right.score {
		return left.score > right.score
	}
	if left.row.timestamp != right.row.timestamp {
		return left.row.timestamp > right.row.timestamp
	}
	return left.index < right.index
}

func isSHAQuery(query string) bool {
	if len(query) < 5 {
		return false
	}
	for _, char := range query {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func (app *giti) updateGraphSearch() {
	query, _ := app.historySearch.GetText()
	app.searchGeneration++
	if app.searchCancel != nil {
		app.searchCancel()
		app.searchCancel = nil
	}
	app.searchMatches, app.searchDepths = nil, nil
	app.clearSearchResults()
	if !app.searchLoadFooter.busy {
		app.searchLoadFooter.setVisible(false)
	}
	if strings.TrimSpace(query) == "" {
		app.setSearchBusy(false)
		app.searchLoadFooter.setBusy(false)
		app.searchViewingResult = false
		app.searchBack.Hide()
		app.historyStack.SetVisibleChildName("graph")
		app.loadFooter.setVisible(app.historyHasMore)
		return
	}
	app.setSearchBusy(true)
	if app.searchFileMode.GetActive() {
		app.searchPlaceholder.SetText("No commits touch this path.")
	} else {
		app.searchPlaceholder.SetText("Searching all commits…")
		app.searchDepths = make(map[string]int)
	}
	generation := app.searchGeneration
	addMainSource(150*time.Millisecond, func() bool {
		if generation == app.searchGeneration {
			app.renderGraphSearch(query)
		}
		return false
	})
}

func (app *giti) renderGraphSearch(query string) {
	generation, repo := app.searchGeneration, app.repository
	if app.searchFileMode.GetActive() {
		ctx, cancel := context.WithCancel(context.Background())
		app.searchCancel = cancel
		follow, limit := app.searchFollow.GetActive(), app.searchLimit
		// Every query owns a generation, context, and repository snapshot. The GTK
		// callback applies results only while all three still identify current work.
		go func() {
			rows, hasMore, err := repo.fileHistoryContext(ctx, query, follow, limit)
			addMainSource(0, func() bool {
				if ctx.Err() != nil || generation != app.searchGeneration || repo != app.repository {
					return false
				}
				app.searchCancel = nil
				app.setSearchBusy(false)
				restoreFocus := app.searchLoadFooter.takeFocus()
				app.searchLoadFooter.setBusy(false)
				if err != nil {
					app.searchPlaceholder.SetText("Could not search: " + err.Error())
					app.showSearchMatches(nil)
					if restoreFocus {
						app.historySearch.GrabFocus()
					}
					return false
				}
				matches := make([]searchMatch, len(rows))
				for index, row := range rows {
					branches, tags := referenceLists(row.refs)
					matches[index] = searchMatch{row: row, index: index, branches: branches, tags: tags}
				}
				app.searchLoadFooter.update(len(matches), "result", hasMore)
				app.showSearchMatches(matches)
				if restoreFocus {
					if hasMore {
						app.searchLoadButton.GrabFocus()
					} else if result := app.searchResults.GetRowAtIndex(0); result != nil {
						result.GrabFocus()
					}
				}
				return false
			})
		}()
		return
	}
	app.searchLoadFooter.setBusy(false)
	app.searchLoadFooter.setVisible(false)
	if !app.searchViewingResult {
		app.historyStack.SetVisibleChildName("search")
		app.loadFooter.setVisible(false)
	}
	ctx, cancel := context.WithCancel(context.Background())
	app.searchCancel = cancel
	options := searchOptions{app.searchMessages.GetActive(), app.searchReferences.GetActive()}
	matches := make([]searchMatch, 0)
	// Pin the starting revision, then stream bounded batches until Git reaches the
	// repository root. Waiting for each GTK update prevents an unbounded queue
	// when Git can produce pages faster than the result list can render them.
	go func() {
		defer cancel()
		revision, err := repo.runContext(ctx, "rev-parse", "--verify", repo.revisionArg+"^{commit}")
		revision = strings.TrimSpace(revision)
		offset := 0
		if err == nil {
			err = repo.streamCommitHistoryContext(ctx, textSearchBatch, options.messages, revision, func(rows []historyRow) bool {
				pageMatches := searchHistory(rows, query, options)
				for index := range pageMatches {
					pageMatches[index].index += offset
				}
				offset += len(rows)
				applied := make(chan struct{})
				addMainSource(0, func() bool {
					defer close(applied)
					if ctx.Err() != nil || generation != app.searchGeneration || repo != app.repository {
						return false
					}
					for _, match := range pageMatches {
						position := sort.Search(len(matches), func(index int) bool { return searchMatchBefore(match, matches[index]) })
						matches = append(matches, searchMatch{})
						copy(matches[position+1:], matches[position:])
						matches[position] = match
						app.searchMatches = append(app.searchMatches, historyRow{})
						copy(app.searchMatches[position+1:], app.searchMatches[position:])
						app.searchMatches[position] = match.row
						app.searchDepths[match.row.revision] = match.index
						app.searchResults.Insert(app.searchResultRow(match), position)
					}
					app.searchResults.ShowAll()
					return false
				})
				select {
				case <-ctx.Done():
					return false
				case <-applied:
					return ctx.Err() == nil && generation == app.searchGeneration && repo == app.repository
				}
			})
		}
		if ctx.Err() == nil {
			addMainSource(0, func() bool {
				if generation == app.searchGeneration && repo == app.repository {
					app.searchCancel = nil
					app.setSearchBusy(false)
					if err != nil {
						app.searchPlaceholder.SetText("Could not search: " + err.Error())
						app.showError(err)
					} else if len(matches) == 0 {
						app.searchPlaceholder.SetText("No commits match this search.")
					}
				}
				return false
			})
		}
	}()
}

func (app *giti) setSearchBusy(busy bool) {
	if busy {
		app.historySearch.SetIconFromPixbuf(gtk.ENTRY_ICON_PRIMARY, app.searchIconSpacer)
		app.searchSpinner.Start()
		app.searchSpinner.Show()
		return
	}
	app.searchSpinner.Stop()
	app.searchSpinner.Hide()
	app.historySearch.SetIconFromIconName(gtk.ENTRY_ICON_PRIMARY, "edit-find-symbolic")
}

func (app *giti) showSearchMatches(matches []searchMatch) {
	app.clearSearchResults()
	app.searchMatches = make([]historyRow, len(matches))
	for index, match := range matches {
		app.searchMatches[index] = match.row
		app.searchResults.Insert(app.searchResultRow(match), -1)
	}
	if !app.searchViewingResult {
		app.historyStack.SetVisibleChildName("search")
		app.loadFooter.setVisible(false)
	}
	app.searchResults.ShowAll()
}

func (app *giti) searchResultRow(match searchMatch) *gtk.ListBoxRow {
	result := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	result.SetMarginStart(8)
	result.SetMarginEnd(8)
	result.SetMarginTop(6)
	result.SetMarginBottom(6)
	label := must(gtk.LabelNew(""))
	label.SetXAlign(0)
	label.SetLineWrap(true)
	label.SetMarkup(searchResultMarkup(match))
	result.PackStart(label, true, true, 0)
	result.PackEnd(app.copySHAButton(match.row.revision), false, false, 0)
	row := must(gtk.ListBoxRowNew())
	rowContext, _ := row.GetStyleContext()
	rowContext.AddClass("giti-selection-row")
	row.Add(result)
	return row
}

func (app *giti) clearSearchResults() {
	if children := app.searchResults.GetChildren(); children != nil {
		children.Foreach(func(child any) { app.searchResults.Remove(child.(gtk.IWidget)) })
		children.Free()
	}
}

func (app *giti) updateSearchMode() {
	fileMode := app.searchFileMode.GetActive()
	app.searchLimit = initialHistoryLimit
	app.searchLoadFooter.setBusy(false)
	app.searchViewingResult = false
	app.searchBack.Hide()
	if fileMode {
		app.historySearch.SetPlaceholderText("File or directory path")
		app.historySearch.SetTooltipText("Enter a path relative to the repository root")
		setAccessibility(&app.historySearch.Widget, "File or directory history search", "Enter a literal path relative to the repository root")
		setAccessibility(&app.searchResults.Widget, "File history search results", "Commits that changed the requested file or directory")
		app.searchPlaceholder.SetText("No commits touch this path.")
		app.searchTextOptions.Hide()
		app.searchFileOptions.ShowAll()
	} else {
		app.historySearch.SetPlaceholderText("Search commits")
		app.historySearch.SetTooltipText("Case-insensitive: exact phrases rank above separate word matches")
		setAccessibility(&app.historySearch.Widget, "Commit text search", "Search commit history in the background")
		setAccessibility(&app.searchResults.Widget, "Commit text search results", "Commits matching the search text")
		app.searchPlaceholder.SetText("No commits match this search.")
		app.searchFileOptions.Hide()
		app.searchTextOptions.ShowAll()
	}
	app.updateGraphSearch()
}

func searchResultMarkup(match searchMatch) string {
	var badges strings.Builder
	for _, part := range referenceParts(match.branches, match.tags, match.row.upstreams) {
		badges.WriteString("  ")
		badges.WriteString(part.markup)
	}
	hints := make([]string, 0, 2)
	if match.matchesSHA {
		hints = append(hints, "matches commit SHA")
	}
	if match.matchesDescription {
		hints = append(hints, "matches commit description")
	}
	hint := ""
	if len(hints) > 0 {
		hint = "\n<span size=\"small\" foreground=\"#4b5563\">" + html.EscapeString(strings.Join(hints, " · ")) + "</span>"
	}
	return fmt.Sprintf("<b>%s</b>%s\n<span foreground=\"#374151\">%s  ·  %s  ·  <tt>%s</tt></span>%s", html.EscapeString(match.row.subject), badges.String(), html.EscapeString(match.row.date), html.EscapeString(match.row.author), html.EscapeString(match.row.revision[:7]), hint)
}

func (app *giti) selectHistoryRevision(revision string) bool {
	for index, row := range app.historyRows {
		if row.revision == revision {
			path := must(gtk.TreePathNewFromIndicesv([]int{index}))
			selection, _ := app.historyView.GetSelection()
			selection.SelectPath(path)
			app.historyView.ScrollToCell(path, nil, true, 0, .5)
			app.historyView.GrabFocus()
			return true
		}
	}
	return false
}

func (app *giti) revealHistoryRevision(revision string) {
	if !app.selectHistoryRevision(revision) {
		app.loadHistoryTo(revision, false, false)
	}
}

func (app *giti) openSearchResult(index int) {
	if index >= 0 && index < len(app.searchMatches) {
		revision := app.searchMatches[index].revision
		if depth, ok := app.searchDepths[revision]; ok {
			app.historyLimit = max(app.historyLimit, depth+initialHistoryLimit)
		}
		app.searchViewingResult = true
		app.historyStack.SetVisibleChildName("graph")
		app.searchBack.Show()
		app.loadFooter.setVisible(app.historyHasMore)
		app.revealHistoryRevision(revision)
	}
}

func (app *giti) onHistorySelected() {
	app.showDiffPage()
	selection, _ := app.historyView.GetSelection()
	_, iter, ok := selection.GetSelected()
	if !ok {
		return
	}
	path, err := app.historyStore.GetPath(iter)
	if err != nil || len(path.GetIndices()) == 0 {
		return
	}
	index := path.GetIndices()[0]
	if index >= len(app.historyRows) {
		return
	}
	row := app.historyRows[index]
	merge := row.kind == "commit" && len(row.parents) > 1
	sameSelection := app.currentRow != nil && app.currentRow.kind == "commit" && app.currentRow.revision == row.revision
	app.fullMergeToggle.HandlerBlock(app.fullMergeHandler)
	if !merge || !sameSelection {
		app.fullMergeToggle.SetActive(false)
	}
	app.fullMergeToggle.SetVisible(merge)
	app.fullMergeToggle.HandlerUnblock(app.fullMergeHandler)
	mergeResolution := merge && !app.fullMergeToggle.GetActive()
	description := "Selected " + row.subject
	if row.kind == "commit" {
		topology := "root commit"
		if len(row.parents) == 1 {
			topology = "one parent"
		} else if len(row.parents) > 1 {
			topology = fmt.Sprintf("merge commit with %d parents", len(row.parents))
		}
		description = fmt.Sprintf("Selected %s, %s, by %s", row.subject, topology, row.author)
	}
	setAccessibility(&app.historyView.Widget, "Commit history", description)
	// Cancel both layers of the previous selection. The generation numbers also
	// reject callbacks that reached GLib just before their contexts were canceled.
	app.selectionGeneration++
	generation := app.selectionGeneration
	if app.selectionCancel != nil {
		app.selectionCancel()
	}
	if app.diffCancel != nil {
		app.diffCancel()
	}
	app.diffGeneration++
	app.resetDiffOverview()
	app.rememberDiffScroll()
	app.clearDiff()
	app.setCommitHeader(commitDetails{subject: "Loading commit details…"})
	app.fileSummary.SetText("Loading changed files…")
	previousPath := ""
	focusFile := app.fileTargetPath != ""
	if focusFile {
		previousPath, app.fileTargetPath = app.fileTargetPath, ""
	} else if app.currentFile != nil {
		previousPath = app.currentFile.path
	}
	app.currentRow, app.currentFile, app.files, app.diffLoaded = &app.historyRows[index], nil, nil, false
	app.fileStore.Clear()
	app.fileTreeStore.Clear()
	clear(app.fileExpandedSubtrees)
	ctx, cancel := context.WithCancel(context.Background())
	app.selectionCancel = cancel
	repo, row := app.repository, *app.currentRow
	ignoreWhitespace := !app.whitespaceToggle.GetActive()
	go func() {
		// Commit metadata and changed-file statistics are independent Git queries,
		// but run serially to avoid competing Git work for a short-lived selection.
		details, detailsErr := commitDetails{}, error(nil)
		if row.kind == "commit" {
			details, detailsErr = repo.commitDetailsContext(ctx, row.revision)
		}
		files, loadErr := repo.changedFilesForViewContext(ctx, row, ignoreWhitespace, mergeResolution)
		addMainSource(0, func() bool {
			if ctx.Err() != nil || generation != app.selectionGeneration || repo != app.repository {
				return false
			}
			app.selectionCancel = nil
			if detailsErr != nil {
				app.showError(detailsErr)
				return false
			}
			if loadErr != nil {
				app.showError(loadErr)
				return false
			}
			// Untracked files are a distinct state: they count toward the file summary
			// but cannot contribute reliable line totals until Git tracks them.
			added, deleted, updated, untracked, additions, deletions := 0, 0, 0, 0, 0, 0
			for _, file := range files {
				switch {
				case file.status == "??":
					untracked++
				case strings.HasPrefix(file.status, "A"):
					added++
				case strings.HasPrefix(file.status, "D"):
					deleted++
				default:
					updated++
				}
				if file.status != "??" && !file.binary {
					additions, deletions = additions+file.additions, deletions+file.deletions
				}
			}
			details.additions, details.deletions, details.untracked = additions, deletions, untracked
			details.statistics = row.kind != "conflict" && row.kind != "resolved" && !mergeResolution
			if row.kind != "commit" {
				details.subject = row.subject
			}
			app.setCommitHeader(details)
			summary := fmt.Sprintf("%d files · %d added · %d deleted · %d updated", len(files), added, deleted, updated)
			markup := fmt.Sprintf("<b>%d files</b> · <span foreground=\"#2e8c47\">%d added</span> · <span foreground=\"#c7404d\">%d deleted</span> · %d updated", len(files), added, deleted, updated)
			switch {
			case row.kind == "conflict":
				summary = fmt.Sprintf("%d conflicts · Needs resolution", len(files))
				markup = fmt.Sprintf("<b>%d conflicts</b> · <span foreground=\"#c7404d\">Needs resolution</span>", len(files))
			case row.kind == "resolved":
				summary = fmt.Sprintf("%d conflicts · Resolution applied", len(files))
				markup = fmt.Sprintf("<b>%d conflicts</b> · <span foreground=\"#2e8c47\">Resolution applied</span>", len(files))
			case mergeResolution:
				summary = fmt.Sprintf("%d merge-resolution files", len(files))
				markup = fmt.Sprintf("<b>%d files</b> · Merge resolution", len(files))
			case untracked > 0:
				summary += fmt.Sprintf(" · %d untracked", untracked)
				markup += fmt.Sprintf(" · <span foreground=\"#4b5563\">%d untracked</span>", untracked)
			}
			app.fileSummary.SetMarkup(markup)
			app.fileSummary.SetTooltipText(summary)
			// Replace the model only after its summary and header agree with the same
			// result, then restore the previous path when it is still present.
			app.files = files
			if len(files) == 0 {
				return false
			}
			app.refreshFileView(previousPath)
			if focusFile {
				app.fileView.GrabFocus()
			}
			return false
		})
	}()
}

func (app *giti) loadHistory(refreshSearch bool) {
	app.loadHistoryTo("", refreshSearch, false)
}

func (app *giti) loadHistoryTo(reveal string, refreshSearch, preserveView bool) {
	// Generation checks protect GTK from callbacks already queued on the main loop.
	// Pagination retains downstream work because existing rows remain unchanged.
	app.historyGeneration++
	if app.historyCancel != nil {
		app.historyCancel()
	}
	historyGeneration := app.historyGeneration
	preserveWork := preserveView && app.currentRow != nil && reveal == ""
	if !preserveWork {
		app.selectionGeneration++
		app.diffGeneration++
		if app.selectionCancel != nil {
			app.selectionCancel()
		}
		if app.diffCancel != nil {
			app.diffCancel()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	app.historyCancel = cancel
	repo, limit := app.repository, app.historyLimit
	ignoreWhitespace := !app.whitespaceToggle.GetActive()
	app.loadFooter.setBusy(true)
	go func() {
		var rows []historyRow
		var graphs []*gdk.Pixbuf
		var hasMore, found, beyondAutoLimit bool
		var err error
		// Parent navigation first locates the requested revision without building
		// every intervening row, then expands the normal history query just enough.
		autoLimit := max(limit, maxAutoHistory)
		if reveal != "" {
			index := -1
			index, beyondAutoLimit, err = repo.historyIndexContext(ctx, reveal, autoLimit)
			found = index >= 0
			if found {
				limit = max(limit, index+1)
			}
		}
		if err == nil {
			rows, hasMore, err = repo.historyContext(ctx, limit, ignoreWhitespace, false)
			if reveal != "" && found {
				found = false
				for _, row := range rows {
					found = found || row.revision == reveal
				}
			}
		}
		// Render at the nominal row height off the GTK thread. A measured-height
		// second pass runs after the model is visible when font scaling requires it.
		graphWidth := 48
		for _, row := range rows {
			graphWidth = max(graphWidth, max(len(row.graph.lanes), len(row.graph.next))*graphLaneWidth)
		}
		if err == nil {
			graphs, err = renderGraphs(rows, graphWidth, graphRowHeight, func() bool { return ctx.Err() != nil })
		}
		addMainSource(0, func() bool {
			if ctx.Err() != nil || historyGeneration != app.historyGeneration || repo != app.repository {
				return false
			}
			restoreFocus := app.loadFooter.takeFocus()
			app.loadFooter.setBusy(false)
			if err != nil {
				app.historyCancel = nil
				app.historyTargetKind = ""
				app.showError(err)
				if restoreFocus {
					app.loadButton.GrabFocus()
				}
				return false
			}
			// Commit the result to GTK only after the stale-work guard above; all
			// model selection and follow-up measurement stays on the main thread.
			app.historyLimit, app.graphWidth, app.historyRows, app.historyHasMore = limit, graphWidth, rows, hasMore
			commitCount := 0
			for _, row := range rows {
				if row.kind == "commit" {
					commitCount++
				}
			}
			app.loadFooter.update(commitCount, "commit", hasMore && (app.historyStack.GetVisibleChildName() == "graph" || app.searchViewingResult))
			app.graphColumn.SetFixedWidth(graphWidth)
			selection, _ := app.historyView.GetSelection()
			historyScroll := app.historyScroller.GetVAdjustment().GetValue()
			preferredKind, preferredRevision := "", ""
			if app.currentRow != nil {
				preferredKind, preferredRevision = app.currentRow.kind, app.currentRow.revision
			}
			if app.historyTargetKind != "" {
				preferredKind, preferredRevision, app.historyTargetKind = app.historyTargetKind, "", ""
			}
			preserveSelection := preserveView && preferredKind != "" && reveal == ""
			if preserveSelection {
				selection.HandlerBlock(app.historySelectionHandler)
			}
			app.historyStore.Clear()
			target, preferredFound := -1, false
			for index, row := range rows {
				iter := app.historyStore.Append()
				app.historyStore.Set(iter, []int{0, 1, 2}, []any{graphs[index], historyLabel(row), row.kind})
				if target < 0 {
					target = index
				}
				if row.kind == preferredKind && (preferredRevision == "" || preferredRevision == row.revision) {
					target, preferredFound = index, true
				}
				if reveal != "" && row.revision == reveal {
					target = index
				}
			}
			if target >= 0 {
				path := must(gtk.TreePathNewFromIndicesv([]int{target}))
				selection.SelectPath(path)
				if reveal != "" {
					app.historyView.ScrollToCell(path, nil, true, 0, .5)
					app.historyView.GrabFocus()
				}
			}
			if preserveSelection {
				selection.HandlerUnblock(app.historySelectionHandler)
				if preferredFound {
					app.currentRow = &app.historyRows[target]
				} else if target >= 0 {
					app.onHistorySelected()
				}
			}
			if restoreFocus {
				if hasMore && app.loadFooter.box.GetVisible() {
					app.loadButton.GrabFocus()
				} else {
					app.historyView.GrabFocus()
				}
			}
			if refreshSearch {
				app.updateGraphSearch()
			}
			addMainSource(0, func() bool {
				if historyGeneration == app.historyGeneration && repo == app.repository {
					app.fitGraphRows(ctx, historyGeneration, repo)
					if reveal != "" {
						app.selectHistoryRevision(reveal)
					} else if preserveSelection {
						adjustment := app.historyScroller.GetVAdjustment()
						adjustment.SetValue(min(historyScroll, max(adjustment.GetLower(), adjustment.GetUpper()-adjustment.GetPageSize())))
					}
				}
				return false
			})
			if reveal != "" && !found {
				message := "Parent " + reveal[:7] + " is not present in this repository history."
				if beyondAutoLimit {
					message = fmt.Sprintf("Parent %s was not found in the first %d commits.", reveal[:7], autoLimit)
				}
				app.showNotification(message, 5*time.Second)
			}
			return false
		})
	}()
}

func (app *giti) fitGraphRows(ctx context.Context, historyGeneration uint64, repo *repository) {
	column := app.historyView.GetColumn(0)
	height := graphRowHeight
	for index, row := range app.historyRows {
		if row.kind == "commit" {
			path := must(gtk.TreePathNewFromIndicesv([]int{index}))
			height = max(height, app.historyView.GetCellArea(path, column).GetHeight())
			break
		}
	}
	if height == graphRowHeight {
		app.historyCancel = nil
		return
	}
	rows, width := app.historyRows, app.graphWidth
	go func() {
		graphs, err := renderGraphs(rows, width, height, func() bool { return ctx.Err() != nil })
		if ctx.Err() != nil {
			return
		}
		addMainSource(0, func() bool {
			if ctx.Err() != nil || historyGeneration != app.historyGeneration || repo != app.repository {
				return false
			}
			app.historyCancel = nil
			if err != nil {
				app.showError(err)
				return false
			}
			for index, graph := range graphs {
				path := must(gtk.TreePathNewFromIndicesv([]int{index}))
				if iter, iterErr := app.historyStore.GetIter(path); iterErr == nil {
					app.historyStore.SetValue(iter, 0, graph)
				}
			}
			return false
		})
	}()
}

func (app *giti) populateBranchSelector(reload bool) {
	if app.branchList == nil {
		return
	}
	current, currentMarkup := "detached at "+app.repository.revision[:min(7, len(app.repository.revision))], ""
	if branch, err := app.repository.run("symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		current = strings.TrimSpace(branch)
		currentMarkup = referenceBadge(current, "branch")
	} else {
		currentMarkup = referenceBadge("HEAD"+headRefSuffix, "branch") + ` <span foreground="#6b7280">` + current + `</span>`
	}
	if reload {
		app.branchRevisions = []string{"HEAD"}
		app.branchLabels = []string{"Current — " + current}
		app.branchMarkups = []string{"Current — " + currentMarkup}
		output, err := app.repository.run("for-each-ref", "--format=%(refname)%00%(symref)", "refs/heads/", "refs/remotes/")
		if err == nil {
			type branchOption struct {
				revision, label, markup string
				remote                  bool
			}
			var branches []branchOption
			for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
				fields := strings.SplitN(line, "\x00", 2)
				if fields[0] == "" || len(fields) > 1 && fields[1] != "" {
					continue
				}
				revision, label := fields[0], strings.TrimPrefix(fields[0], "refs/heads/")
				remote := strings.HasPrefix(revision, remoteRefPrefix)
				if remote {
					label = strings.TrimPrefix(revision, remoteRefPrefix)
				} else {
					revision = label
				}
				branches = append(branches, branchOption{revision, label, referenceBadge(revision, "branch"), remote})
			}
			sort.Slice(branches, func(left, right int) bool {
				if branches[left].remote != branches[right].remote {
					return !branches[left].remote
				}
				leftName, rightName := strings.ToLower(branches[left].label), strings.ToLower(branches[right].label)
				return leftName < rightName || leftName == rightName && branches[left].label < branches[right].label
			})
			for _, branch := range branches {
				app.branchRevisions = append(app.branchRevisions, branch.revision)
				app.branchLabels = append(app.branchLabels, branch.label)
				app.branchMarkups = append(app.branchMarkups, branch.markup)
			}
		}
		app.branchRepositoryPath = app.repository.path
		app.renderBranchSelector()
	} else {
		for row, index := range app.branchVisible {
			if app.branchRevisions[index] == app.repository.revisionArg {
				app.branchList.SelectRow(app.branchList.GetRowAtIndex(row))
				break
			}
		}
	}
	viewed := app.repository.revisionArg
	viewedMarkup := referenceBadge(viewed, "branch")
	if viewed == "HEAD" {
		viewed = "Current — " + current
		viewedMarkup = "Current — " + currentMarkup
	}
	app.branchLabel.SetMarkup(viewedMarkup)
	app.branchButton.SetTooltipText("Browse history from " + viewed)
	setAccessibility(&app.branchButton.Widget, "History branch: "+viewed, "Choose a branch to browse without changing the working tree")
}

func (app *giti) renderBranchSelector() {
	app.branchList.UnselectAll()
	if children := app.branchList.GetChildren(); children != nil {
		children.Foreach(func(child any) { app.branchList.Remove(child.(gtk.IWidget)) })
		children.Free()
	}
	text, _ := app.branchSearch.GetText()
	query, matches := strings.ToLower(strings.TrimSpace(text)), 0
	app.branchVisible = nil
	for index, name := range app.branchLabels {
		if query != "" && !strings.Contains(strings.ToLower(name), query) {
			continue
		}
		matches++
		if len(app.branchVisible) >= branchResultLimit {
			continue
		}
		app.branchVisible = append(app.branchVisible, index)
		label := must(gtk.LabelNew(""))
		label.SetMarkup(app.branchMarkups[index])
		label.SetXAlign(0)
		label.SetMarginStart(8)
		label.SetMarginEnd(8)
		label.SetMarginTop(6)
		label.SetMarginBottom(6)
		row := must(gtk.ListBoxRowNew())
		rowContext, _ := row.GetStyleContext()
		rowContext.AddClass("giti-selection-row")
		row.Add(label)
		app.branchList.Add(row)
		if app.branchRevisions[index] == app.repository.revisionArg {
			app.branchList.SelectRow(row)
		}
	}
	app.branchList.ShowAll()
	remaining := matches - len(app.branchVisible)
	if remaining == 0 {
		app.branchLimitLabel.Hide()
		return
	}
	message := fmt.Sprintf("… %d more branches. Search to show them.", remaining)
	if query != "" {
		message = fmt.Sprintf("… %d more matches. Refine your search to show them.", remaining)
	}
	app.branchLimitLabel.SetText(message)
	app.branchLimitLabel.Show()
}

func (app *giti) buildHistoryPane(state uiState) *gtk.Box {
	// History graph and commit metadata share a row so graph geometry remains
	// aligned with text after GTK applies font and scale settings.
	app.historyView = must(gtk.TreeViewNewWithModel(app.historyStore))
	setAccessibility(&app.historyView.Widget, "Commit history", "Git commits ordered from newest to oldest; each row shows its author and commit time")
	app.historyView.SetHeadersVisible(false)
	historyContext, _ := app.historyView.GetStyleContext()
	historyContext.AddClass("giti-list")
	graphRenderer := must(gtk.CellRendererPixbufNew())
	historyRenderer := must(gtk.CellRendererTextNew())
	historyColumn := must(gtk.TreeViewColumnNewWithAttribute("Graph", graphRenderer, "pixbuf", 0))
	historyColumn.SetMinWidth(48)
	historyColumn.SetSizing(gtk.TREE_VIEW_COLUMN_FIXED)
	app.graphColumn = historyColumn
	app.historyView.AppendColumn(historyColumn)
	historyColumn = must(gtk.TreeViewColumnNewWithAttribute("History", historyRenderer, "markup", 1))
	historyColumn.SetSizing(gtk.TREE_VIEW_COLUMN_AUTOSIZE)
	app.historyView.AppendColumn(historyColumn)
	historySelection, _ := app.historyView.GetSelection()
	historySelection.SetSelectFunction(func(_ *gtk.TreeSelection, model *gtk.TreeModel, path *gtk.TreePath, _ bool) bool {
		iter, err := model.GetIter(path)
		if err != nil {
			return false
		}
		value, err := model.GetValue(iter, 2)
		if err != nil {
			return false
		}
		kind, _ := value.GetString()
		return kind != ""
	})
	app.historySelectionHandler = historySelection.Connect("changed", app.onHistorySelected)
	app.historyView.Connect("button-release-event", func(_ *gtk.TreeView, event *gdk.Event) bool {
		button := gdk.EventButtonNewFromEvent(event)
		if button.Button() != gdk.BUTTON_PRIMARY {
			return false
		}
		path, column, cellX, cellY, ok := app.historyView.GetPathAtPos(int(button.X()), int(button.Y()))
		if !ok || column == nil || column.GetTitle() != "History" || path == nil {
			return false
		}
		indices := path.GetIndices()
		if len(indices) == 1 && indices[0] < len(app.historyRows) {
			row := app.historyRows[indices[0]]
			branches, tags := referenceLists(row.refs)
			if row.kind != "commit" {
				return false
			}
			// TreeView markup has no child widgets to receive clicks. Recreate the
			// rendered prefix measurements to make only the inline overflow badge
			// interactive without placing long references in a separate column.
			prefix := ""
			measure := func(markup string) (int, int) {
				label := must(gtk.LabelNew(""))
				label.SetMarkup(markup)
				_, width := label.GetPreferredWidth()
				_, height := label.GetPreferredHeight()
				label.Destroy()
				return width, height
			}
			for _, part := range historyReferenceParts(row) {
				if !part.overflow {
					prefix += part.markup + "  "
					continue
				}
				start, height := measure(prefix)
				prefix += part.markup
				end, _ := measure(prefix)
				if cellY <= height && cellX >= start-3 && cellX <= end+3 {
					app.showReferences(branches, tags)
					return true
				}
				prefix += "  "
			}
		}
		return false
	})
	// Text and file searches query history independently, leaving the regular
	// graph and its topology intact until the user opens a result.
	app.historySearch = must(gtk.SearchEntryNew())
	app.searchSpinner = must(gtk.SpinnerNew())
	app.searchIconSpacer = must(gdk.PixbufNew(gdk.COLORSPACE_RGB, true, 8, 16, 16))
	app.searchIconSpacer.Fill(0)
	app.searchSpinner.SetHAlign(gtk.ALIGN_START)
	app.searchSpinner.SetVAlign(gtk.ALIGN_CENTER)
	app.searchSpinner.SetMarginStart(8)
	app.searchSpinner.SetNoShowAll(true)
	app.searchSpinner.Hide()
	setAccessibility(&app.searchSpinner.Widget, "Searching history", "Search is still running")
	searchOverlay := must(gtk.OverlayNew())
	searchOverlay.Add(app.historySearch)
	searchOverlay.AddOverlay(app.searchSpinner)
	searchOverlay.SetOverlayPassThrough(app.searchSpinner, true)
	app.historySearch.SetTooltipText("Case-insensitive: exact phrases rank above separate word matches")
	app.historySearch.Connect("changed", func() {
		app.searchViewingResult = false
		app.searchBack.Hide()
		app.searchLimit = initialHistoryLimit
		app.searchLoadFooter.setBusy(false)
		app.updateGraphSearch()
	})
	app.searchTextMode = must(gtk.RadioButtonNewWithLabel(nil, "Search commit text"))
	app.searchFileMode = must(gtk.RadioButtonNewWithLabelFromWidget(app.searchTextMode, "Search file or directory history"))
	app.searchTextMode.SetActive(app.repository.searchPath == "")
	app.searchFileMode.SetActive(app.repository.searchPath != "")
	app.searchMessages = must(gtk.CheckButtonNewWithLabel("Also match commit description"))
	app.searchMessages.SetActive(state.SearchCommitMessages)
	app.searchReferences = must(gtk.CheckButtonNewWithLabel("Also match branches and tags"))
	app.searchReferences.SetActive(state.SearchReferences)
	app.searchFollow = must(gtk.CheckButtonNewWithLabel("Follow file across renames"))
	app.searchFollow.SetActive(app.repository.follow)
	searchOptions := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 6))
	searchOptions.SetMarginStart(10)
	searchOptions.SetMarginEnd(10)
	searchOptions.SetMarginTop(10)
	searchOptions.SetMarginBottom(10)
	searchOptions.PackStart(app.searchTextMode, false, false, 0)
	searchOptions.PackStart(app.searchFileMode, false, false, 0)
	app.searchTextOptions = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 6))
	app.searchTextOptions.SetMarginStart(18)
	app.searchTextOptions.PackStart(app.searchMessages, false, false, 0)
	app.searchTextOptions.PackStart(app.searchReferences, false, false, 0)
	app.searchFileOptions = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 6))
	app.searchFileOptions.SetMarginStart(18)
	app.searchFileOptions.PackStart(app.searchFollow, false, false, 0)
	searchOptions.PackStart(app.searchTextOptions, false, false, 0)
	searchOptions.PackStart(app.searchFileOptions, false, false, 0)
	app.searchSettings = must(gtk.MenuButtonNew())
	setAccessibility(&app.searchSettings.Widget, "Search options", "Choose text or file history search and its options")
	app.searchSettings.SetImage(must(gtk.ImageNewFromIconName("preferences-system-symbolic", gtk.ICON_SIZE_BUTTON)))
	app.searchSettings.SetTooltipText("Search options")
	app.searchSettings.SetRelief(gtk.RELIEF_NONE)
	searchSettingsContext, _ := app.searchSettings.GetStyleContext()
	searchSettingsContext.AddClass("giti-flat-button")
	searchPopover := must(gtk.PopoverNew(app.searchSettings))
	searchPopover.Add(searchOptions)
	searchOptions.ShowAll()
	app.searchSettings.SetPopover(searchPopover)
	app.searchMessages.Connect("toggled", func() {
		active := app.searchMessages.GetActive()
		_ = patchUIState(app.statePath, func(state *uiState) { state.SearchCommitMessages = active })
		app.updateGraphSearch()
	})
	app.searchReferences.Connect("toggled", func() {
		active := app.searchReferences.GetActive()
		_ = patchUIState(app.statePath, func(state *uiState) { state.SearchReferences = active })
		app.updateGraphSearch()
	})
	app.searchFollow.Connect("toggled", func() {
		app.searchLimit = initialHistoryLimit
		app.updateGraphSearch()
	})
	app.searchTextMode.Connect("toggled", app.updateSearchMode)
	app.searchResults = must(gtk.ListBoxNew())
	app.searchResults.SetActivateOnSingleClick(true)
	app.searchPlaceholder = must(gtk.LabelNew("No loaded commits match this search."))
	setAccessibilityRoleAlert(&app.searchPlaceholder.Widget)
	app.searchResults.SetPlaceholder(app.searchPlaceholder)
	setAccessibility(&app.searchResults.Widget, "Search results", "Commits matching the current search")
	app.searchResults.Connect("row-activated", func(_ *gtk.ListBox, result *gtk.ListBoxRow) {
		app.openSearchResult(result.GetIndex())
	})
	app.historyStack = must(gtk.StackNew())
	app.historyScroller = scroller(app.historyView)
	app.historyStack.AddNamed(app.historyScroller, "graph")
	searchPage := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
	searchPage.PackStart(scroller(app.searchResults), true, true, 0)
	app.searchLoadFooter = newLoadFooter("Load 100 more results", "Loading more results…", "Load more search results", "Load the next 100 file history search results")
	app.searchLoadButton = app.searchLoadFooter.button
	app.searchLoadButton.Connect("clicked", func() {
		app.searchLoadFooter.setBusy(true)
		app.searchLimit += 100
		app.updateGraphSearch()
	})
	searchPage.PackStart(app.searchLoadFooter.box, false, false, 0)
	app.historyStack.AddNamed(searchPage, "search")
	app.searchBack = must(gtk.ButtonNewFromIconName("go-previous-symbolic", gtk.ICON_SIZE_BUTTON))
	setAccessibility(&app.searchBack.Widget, "Back to search results", "Return from the selected commit to the current search results")
	app.searchBack.SetTooltipText("Back to search results")
	app.searchBack.SetRelief(gtk.RELIEF_NONE)
	searchBackContext, _ := app.searchBack.GetStyleContext()
	searchBackContext.AddClass("giti-flat-button")
	app.searchBack.Connect("clicked", func() {
		app.searchViewingResult = false
		app.historyStack.SetVisibleChildName("search")
		app.searchBack.Hide()
		app.loadFooter.setVisible(false)
		if result := app.searchResults.GetSelectedRow(); result != nil {
			result.GrabFocus()
		} else if result = app.searchResults.GetRowAtIndex(0); result != nil {
			result.GrabFocus()
		}
	})
	app.loadFooter = newLoadFooter("Load 100 more commits", "Loading older commits…", "Load more commits", "Load the next 100 older commits")
	app.loadButton = app.loadFooter.button
	app.loadButton.Connect("clicked", func() {
		app.loadFooter.setBusy(true)
		app.historyLimit += 100
		app.loadHistoryTo("", false, true)
	})
	graphBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
	searchBox := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	searchBox.PackStart(app.searchBack, false, false, 0)
	searchBox.PackStart(searchOverlay, true, true, 0)
	searchBox.PackStart(app.searchSettings, false, false, 0)
	graphBox.PackStart(searchBox, false, false, 0)
	graphBox.PackStart(app.historyStack, true, true, 0)
	graphBox.PackStart(app.loadFooter.box, false, false, 0)
	return graphBox
}
