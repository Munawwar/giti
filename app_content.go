package main

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"
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
	topology := "root commit"
	if len(row.parents) == 1 {
		topology = "1 parent"
	} else if len(row.parents) > 1 {
		topology = fmt.Sprintf("merge · %d parents", len(row.parents))
	}
	return fmt.Sprintf("%s<b>%s</b>\n<span foreground=\"#374151\"><tt>%s</tt>  ·  %s  ·  %s</span>", refs.String(), html.EscapeString(row.subject), html.EscapeString(row.revision[:7]), html.EscapeString(row.author), topology)
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
	app.historySearch.SetProgressFraction(0)
	app.searchMatches, app.searchDepths = nil, nil
	app.clearSearchResults()
	if strings.TrimSpace(query) == "" {
		app.searchViewingResult = false
		app.searchBack.Hide()
		app.searchLoadButton.Hide()
		app.historyStack.SetVisibleChildName("graph")
		app.loadButton.SetVisible(app.historyHasMore)
		return
	}
	if app.searchFileMode.GetActive() {
		app.searchPlaceholder.SetText("No commits touch this path.")
	} else {
		app.searchPlaceholder.SetText("Searching all commits…")
		app.searchDepths = make(map[string]int)
		app.historySearch.SetProgressPulseStep(.08)
		app.historySearch.ProgressPulse()
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
				if err != nil {
					app.searchPlaceholder.SetText("Could not search: " + err.Error())
					app.searchLoadButton.Hide()
					app.showSearchMatches(nil)
					return false
				}
				matches := make([]searchMatch, len(rows))
				for index, row := range rows {
					branches, tags := referenceLists(row.refs)
					matches[index] = searchMatch{row: row, index: index, branches: branches, tags: tags}
				}
				restoreFocus := !hasMore && app.searchLoadButton.IsFocus()
				app.searchLoadButton.SetVisible(hasMore)
				app.showSearchMatches(matches)
				if restoreFocus {
					if result := app.searchResults.GetRowAtIndex(0); result != nil {
						result.GrabFocus()
					}
				}
				return false
			})
		}()
		return
	}
	app.searchLoadButton.Hide()
	if !app.searchViewingResult {
		app.historyStack.SetVisibleChildName("search")
		app.loadButton.Hide()
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
					app.historySearch.ProgressPulse()
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
					app.historySearch.SetProgressFraction(0)
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

func (app *giti) showSearchMatches(matches []searchMatch) {
	app.clearSearchResults()
	app.searchMatches = make([]historyRow, len(matches))
	for index, match := range matches {
		app.searchMatches[index] = match.row
		app.searchResults.Insert(app.searchResultRow(match), -1)
	}
	if !app.searchViewingResult {
		app.historyStack.SetVisibleChildName("search")
		app.loadButton.Hide()
	}
	app.searchResults.ShowAll()
}

func (app *giti) searchResultRow(match searchMatch) *gtk.Box {
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
	return result
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
		app.loadHistoryTo(revision, false)
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
		app.loadButton.SetVisible(app.historyHasMore)
		app.revealHistoryRevision(revision)
	}
}

func (app *giti) setCommitHeader(details commitDetails) {
	// The title, commit identity, and detail regions change with selection. The
	// action row stays mounted so its toggles retain their state and callbacks.
	app.headerReferenceButtons = nil
	for _, box := range []*gtk.Box{app.headerTitleRow, app.headerCommit, app.headerDetails} {
		if children := box.GetChildren(); children != nil {
			children.Foreach(func(child any) { box.Remove(child.(gtk.IWidget)) })
			children.Free()
		}
	}
	title := must(gtk.LabelNew(""))
	title.SetXAlign(0)
	title.SetSelectable(true)
	title.SetEllipsize(pango.ELLIPSIZE_END)
	title.SetMarkup("<span size=\"large\" weight=\"bold\">" + html.EscapeString(details.subject) + "</span>")
	app.headerTitleRow.PackStart(title, true, true, 0)
	// Keep aggregate statistics outside the ellipsized title so they remain
	// visible even for unusually long subjects.
	if details.statistics {
		additions := must(gtk.LabelNew(fmt.Sprintf("+%d", details.additions)))
		deletions := must(gtk.LabelNew(fmt.Sprintf("−%d", details.deletions)))
		for _, badge := range []*gtk.Label{additions, deletions} {
			context, _ := badge.GetStyleContext()
			context.AddClass("giti-stat")
			badge.SetTooltipText("Line totals exclude binary and untracked files")
			app.headerTitleRow.PackStart(badge, false, false, 0)
		}
		additionContext, _ := additions.GetStyleContext()
		additionContext.AddClass("giti-additions")
		deletionContext, _ := deletions.GetStyleContext()
		deletionContext.AddClass("giti-deletions")
		if details.untracked > 0 {
			untracked := must(gtk.LabelNew(fmt.Sprintf("%d untracked", details.untracked)))
			context, _ := untracked.GetStyleContext()
			context.AddClass("giti-stat")
			context.AddClass("giti-untracked")
			untracked.SetTooltipText("Untracked files have no line counts")
			app.headerTitleRow.PackStart(untracked, false, false, 0)
		}
	}
	if details.sha == "" {
		app.commitHeader.ShowAll()
		return
	}
	commitLabel := must(gtk.LabelNew(""))
	commitLabel.SetMarkup("<span foreground=\"#4b5563\"><b>Commit</b></span>")
	shaLabel := must(gtk.LabelNew(""))
	shaLabel.SetXAlign(0)
	shaLabel.SetSelectable(true)
	shaLabel.SetEllipsize(pango.ELLIPSIZE_MIDDLE)
	shaLabel.SetTooltipText(details.sha)
	shaLabel.SetMarkup(fmt.Sprintf("<span foreground=\"#4b5563\"><tt>%s</tt></span>", html.EscapeString(details.sha)))
	app.headerCommit.PackStart(commitLabel, false, false, 0)
	app.headerCommit.PackStart(shaLabel, true, true, 0)
	app.headerCommit.PackStart(app.copySHAButton(details.sha), false, false, 0)
	meta := must(gtk.LabelNew(""))
	meta.SetXAlign(0)
	meta.SetLineWrap(true)
	meta.SetSelectable(true)
	meta.SetMarkup(fmt.Sprintf("<span foreground=\"#4b5563\"><b>Author</b> %s &lt;%s&gt;  ·  %s\n<b>Committer</b> %s &lt;%s&gt;  ·  %s</span>", html.EscapeString(details.author), html.EscapeString(details.authorEmail), html.EscapeString(details.authored), html.EscapeString(details.committer), html.EscapeString(details.committerEmail), html.EscapeString(details.committed)))
	app.headerDetails.PackStart(meta, false, false, 0)
	if len(details.branches) > 0 || len(details.tags) > 0 {
		refs := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 6))
		// Reference badges are buttons because their visible text may be shortened;
		// each closure retains the complete copy value and relationship description.
		addBadge := func(target *gtk.Box, value, kind, markup, description string) *gtk.Button {
			display, _, _, _ := referenceAppearance(value, kind)
			copyValue := display
			if kind == "branch" && strings.HasSuffix(value, headRefSuffix) {
				copyValue = strings.TrimSuffix(value, headRefSuffix)
			}
			label := must(gtk.LabelNew(""))
			label.SetXAlign(0)
			label.SetEllipsize(pango.ELLIPSIZE_END)
			label.SetMaxWidthChars(28)
			if markup == "" {
				markup = referenceBadge(value, kind)
			}
			label.SetMarkup(markup)
			button := must(gtk.ButtonNew())
			button.SetRelief(gtk.RELIEF_NONE)
			button.SetTooltipText("Copy " + kind + ": " + copyValue)
			if description == "" {
				description = "Copy the complete reference name to the clipboard"
			}
			setAccessibility(&button.Widget, "Copy "+kind+" "+copyValue, description)
			context, _ := button.GetStyleContext()
			context.AddClass("giti-ref-copy")
			button.Add(label)
			button.Connect("clicked", func() {
				app.copyToClipboard(copyValue, "Copied "+kind+" to clipboard.")
			})
			app.headerReferenceButtons = append(app.headerReferenceButtons, button)
			target.PackStart(button, false, false, 0)
			return button
		}
		// Configured upstream pairs render as joined visual segments while keeping
		// independent copy targets for the local and remote names.
		for _, part := range branchReferenceParts(details.branches, details.upstreams) {
			if part.overflow {
				more := must(gtk.ButtonNewWithLabel(part.label))
				more.SetRelief(gtk.RELIEF_NONE)
				more.SetTooltipText("Show all branches for this commit")
				setAccessibility(&more.Widget, "Show hidden branches", "Open the complete branch and tag list")
				more.Connect("clicked", func() { app.showReferences(details.branches, details.tags) })
				refs.PackStart(more, false, false, 0)
				continue
			}
			if !part.synced {
				addBadge(refs, part.branches[0], "branch", "", "")
				continue
			}
			group := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
			remote, local := part.branches[0], part.branches[1]
			remoteDisplay, _, _, _ := referenceAppearance(remote, "branch")
			localDisplay, _, _, _ := referenceAppearance(local, "branch")
			for _, button := range []*gtk.Button{
				addBadge(group, remote, "branch", part.segments[0], "Copy the remote branch "+remoteDisplay+"; it matches local branch "+localDisplay),
				addBadge(group, local, "branch", part.segments[1], "Copy the local branch "+localDisplay+"; it matches configured upstream "+remoteDisplay),
			} {
				context, _ := button.GetStyleContext()
				context.AddClass("giti-ref-joined")
			}
			refs.PackStart(group, false, false, 0)
		}
		for _, tag := range details.tags[:min(2, len(details.tags))] {
			addBadge(refs, tag, "tag", "", "")
		}
		if len(details.tags) > 2 {
			more := must(gtk.ButtonNewWithLabel("+ more tags"))
			more.SetRelief(gtk.RELIEF_NONE)
			more.SetTooltipText("Show all tags for this commit")
			more.Connect("clicked", func() { app.showReferences(details.branches, details.tags) })
			refs.PackStart(more, false, false, 0)
		}
		app.headerDetails.PackStart(refs, false, false, 0)
	}
	if len(details.parents) > 0 {
		parents := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 4))
		parentLabel := must(gtk.LabelNew("Parents:"))
		parentLabel.SetSelectable(true)
		parents.PackStart(parentLabel, false, false, 0)
		for _, parent := range details.parents {
			parent := parent
			button := must(gtk.ButtonNewWithLabel("↖ " + parent[:7]))
			button.SetRelief(gtk.RELIEF_NONE)
			button.SetTooltipText("Open parent " + parent)
			button.Connect("clicked", func() {
				app.historySearch.SetText("")
				app.revealHistoryRevision(parent)
			})
			parents.PackStart(button, false, false, 0)
		}
		app.headerDetails.PackStart(parents, false, false, 0)
	}
	if details.body != "" {
		expander := must(gtk.ExpanderNew("Commit description"))
		body := must(gtk.LabelNew(details.body))
		body.SetXAlign(0)
		body.SetYAlign(0)
		body.SetSelectable(true)
		body.SetLineWrap(true)
		body.SetLineWrapMode(pango.WRAP_WORD_CHAR)
		body.SetMarginStart(20)
		body.SetMarginEnd(8)
		message := scroller(body)
		message.SetPolicy(gtk.POLICY_NEVER, gtk.POLICY_AUTOMATIC)
		message.SetMinContentHeight(min(180, max(48, (strings.Count(details.body, "\n")+1)*20+12)))
		message.SetMaxContentHeight(180)
		message.SetPropagateNaturalHeight(true)
		expander.Add(message)
		app.headerDetails.PackStart(expander, false, false, 4)
	}
	app.commitHeader.ShowAll()
}

func (app *giti) showDiffPage() {
	if app.diffStack != nil {
		app.diffStack.SetVisibleChildName("diff")
	}
}

func (app *giti) showReferences(branches, tags []string) {
	if app.diffStack == nil || app.referencesPage == nil {
		return
	}
	if app.diffFindBox.GetVisible() {
		app.closeDiffFind()
	}
	branches, tags = sortedReferences(branches, tags)
	if children := app.referencesPage.GetChildren(); children != nil {
		children.Foreach(func(child any) { app.referencesPage.Remove(child.(gtk.IWidget)) })
		children.Free()
	}
	back := must(gtk.ButtonNewWithLabel("← Back to diff"))
	back.SetRelief(gtk.RELIEF_NONE)
	back.SetHAlign(gtk.ALIGN_START)
	back.Connect("clicked", app.showDiffPage)
	app.referencesPage.PackStart(back, false, false, 0)
	buffer := must(gtk.TextBufferNew(nil))
	buffer.CreateTag("title", map[string]any{"weight": int(pango.WEIGHT_BOLD), "scale": 1.2})
	buffer.CreateTag("section", map[string]any{"weight": int(pango.WEIGHT_BOLD)})
	insert := func(text, tag string) {
		iter := buffer.GetEndIter()
		if tag == "" {
			buffer.Insert(iter, text)
		} else {
			buffer.InsertWithTagByName(iter, text, tag)
		}
	}
	insert("Branches and tags", "title")
	createdTags := make(map[string]bool, 4)
	for _, section := range []struct {
		name, kind string
		values     []string
	}{{"Branches", "branch", branches}, {"Tags", "tag", tags}} {
		if len(section.values) == 0 {
			continue
		}
		insert("\n\n"+section.name, "section")
		for _, value := range section.values {
			display, tag, background, foreground := referenceAppearance(value, section.kind)
			if !createdTags[tag] {
				buffer.CreateTag(tag, map[string]any{"background": background, "foreground": foreground, "weight": int(pango.WEIGHT_BOLD), "pixels-above-lines": 3, "pixels-below-lines": 3})
				createdTags[tag] = true
			}
			insert("\n "+display+" ", tag)
		}
	}
	if len(branches) == 0 && len(tags) == 0 {
		insert("\n\nNo branches or tags.", "")
	}
	app.referencesView = must(gtk.TextViewNewWithBuffer(buffer))
	app.referencesView.SetEditable(false)
	app.referencesView.SetCursorVisible(false)
	app.referencesView.SetWrapMode(gtk.WRAP_CHAR)
	app.referencesView.SetAcceptsTab(false)
	context, _ := app.referencesView.GetStyleContext()
	context.AddClass("giti-references")
	app.referencesPage.PackStart(app.referencesView, true, true, 0)
	app.referencesPage.ShowAll()
	app.diffStack.SetVisibleChildName("references")
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
	if app.currentFile != nil {
		previousPath = app.currentFile.path
	}
	app.currentRow, app.currentFile, app.files, app.diffLoaded = &app.historyRows[index], nil, nil, false
	app.fileStore.Clear()
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
			target := 0
			for fileIndex, file := range files {
				iter := app.fileStore.Append()
				stat := fmt.Sprintf("<span foreground=\"#2e8c47\">+%d</span>  <span foreground=\"#c7404d\">−%d</span>", file.additions, file.deletions)
				if file.status == "??" {
					stat = "<span foreground=\"#6b7280\">Untracked</span>"
				} else if file.conflict != "" {
					color := "#c7404d"
					if file.status == "✓" {
						color = "#2e8c47"
					}
					stat = fmt.Sprintf("<span foreground=\"%s\">%s</span>", color, html.EscapeString(file.conflict))
				} else if mergeResolution {
					stat = "<span foreground=\"#6b7280\">Combined</span>"
				} else if file.binary {
					stat = "<span foreground=\"#6b7280\">Binary</span>"
				}
				app.fileStore.Set(iter, []int{0, 1}, []any{file.label(), stat})
				if file.path == previousPath {
					target = fileIndex
				}
			}
			if len(files) == 0 {
				return false
			}
			path := must(gtk.TreePathNewFromIndicesv([]int{target}))
			selection, _ := app.fileView.GetSelection()
			selection.SelectPath(path)
			return false
		})
	}()
}

func (app *giti) onFileSelected() {
	app.showDiffPage()
	selection, _ := app.fileView.GetSelection()
	_, iter, ok := selection.GetSelected()
	if !ok || app.currentRow == nil {
		return
	}
	path, err := app.fileStore.GetPath(iter)
	if err != nil || len(path.GetIndices()) == 0 {
		return
	}
	index := path.GetIndices()[0]
	if index >= len(app.files) {
		return
	}
	// File loads are independently cancelable from history loads; both generation
	// values must match before a patch can update the shared diff widgets.
	app.diffGeneration++
	generation, selectionGeneration := app.diffGeneration, app.selectionGeneration
	if app.diffCancel != nil {
		app.diffCancel()
	}
	file := &app.files[index]
	app.rememberDiffScroll()
	app.currentFile = file
	app.diffLoaded = false
	app.fullFileToggle.SetSensitive(false)
	app.clearDiff()
	app.resetDiffOverview()
	app.diffScroller.GetHAdjustment().SetValue(0)
	app.diffScroller.GetVAdjustment().SetValue(0)
	ctx, cancel := context.WithCancel(context.Background())
	app.diffCancel = cancel
	repo, row, selectedFile := app.repository, *app.currentRow, *file
	position := app.diffScroll[diffKey(row, selectedFile)]
	ignoreWhitespace, preferFullFile := !app.whitespaceToggle.GetActive(), app.fullFilePreferred
	mergeResolution := row.kind == "commit" && len(row.parents) > 1 && !app.fullMergeToggle.GetActive()
	go func() {
		size := repo.fileSizeContext(ctx, row, selectedFile)
		fullFile := preferFullFile && size <= fullFileLimit
		patch, loadErr := repo.diffForViewContext(ctx, row, selectedFile, ignoreWhitespace, fullFile, mergeResolution)
		addMainSource(0, func() bool {
			if ctx.Err() != nil || generation != app.diffGeneration || selectionGeneration != app.selectionGeneration || repo != app.repository {
				return false
			}
			app.diffCancel = nil
			if loadErr != nil {
				app.showError(loadErr)
				return false
			}
			allowed := size <= fullFileLimit
			app.fullFileToggle.HandlerBlock(app.fullFileHandler)
			app.fullFileToggle.SetSensitive(allowed)
			app.fullFileToggle.SetActive(fullFile)
			if !allowed {
				app.fullFileToggle.SetTooltipText("Disabled for files larger than 2 MiB")
			} else {
				app.fullFileToggle.SetTooltipText("Show unchanged lines from the complete file")
			}
			app.fullFileToggle.HandlerUnblock(app.fullFileHandler)
			app.setDiff(patch)
			app.diffLoaded = true
			addMainSource(0, func() bool {
				if generation == app.diffGeneration && selectionGeneration == app.selectionGeneration {
					horizontal, vertical := app.diffScroller.GetHAdjustment(), app.diffScroller.GetVAdjustment()
					horizontal.SetValue(min(position.horizontal, max(0, horizontal.GetUpper()-horizontal.GetPageSize())))
					vertical.SetValue(min(position.vertical, max(0, vertical.GetUpper()-vertical.GetPageSize())))
				}
				return false
			})
			return false
		})
	}()
}

func (app *giti) setDiff(patch string) {
	app.diffBuffer.SetText("")
	lines := displayLines(patch)
	app.overviewMarkers, app.overviewLines = app.overviewMarkers[:0], len(lines)
	if cap(app.diffLineNumbers) < len(lines) {
		app.diffLineNumbers = make([]diffLineNumber, len(lines))
	} else {
		app.diffLineNumbers = app.diffLineNumbers[:len(lines)]
	}
	maxLine := 0
	for index, line := range lines {
		kind := diffLineContext
		if line.tag == "added" {
			kind = diffLineAdded
		} else if line.tag == "removed" {
			kind = diffLineRemoved
		}
		app.diffLineNumbers[index] = diffLineNumber{uint32(line.old), uint32(line.new), kind}
		maxLine = max(maxLine, line.old, line.new)
		if line.tag == "added" || line.tag == "removed" {
			app.overviewMarkers = append(app.overviewMarkers, overviewMarker{line: index, added: line.tag == "added"})
		}
	}
	for start := 0; start < len(lines); {
		end, text := start+1, strings.Builder{}
		tag := lines[start].tag
		if tag == "hunk" && app.fullFileToggle.GetActive() {
			tag = ""
		}
		text.WriteString(lines[start].text)
		for end < len(lines) && lines[end].tag == lines[start].tag {
			text.WriteString(lines[end].text)
			end++
		}
		iter := app.diffBuffer.GetEndIter()
		if tag == "" {
			app.diffBuffer.Insert(iter, text.String())
		} else {
			app.diffBuffer.InsertWithTagByName(iter, text.String(), tag)
		}
		start = end
	}
	app.diffGutterDigits = max(2, len(strconv.Itoa(maxLine)))
	app.diffGutter.SetVisible(app.fullFileToggle.GetActive() && maxLine > 0)
	app.diffGutter.QueueDraw()
	app.diffOverviewReveal.SetRevealChild(app.fullFileToggle.GetActive() && len(app.overviewMarkers) > 0)
	app.diffOverview.QueueDraw()
	app.updateDiffFind()
}

func (app *giti) clearDiff() {
	app.diffBuffer.SetText("")
	app.diffLineNumbers = app.diffLineNumbers[:0]
	app.diffGutter.Hide()
	app.updateDiffFind()
}

type overviewMarker struct {
	line  int
	added bool
}

type displayLine struct {
	text, tag string
	old, new  int
}

func displayLines(patch string) []displayLine {
	lines := make([]displayLine, 0)
	inHeader, prefixWidth, oldLine, newLine := true, 1, 0, 0
	for _, line := range splitAfterLines(patch) {
		hunk := strings.HasPrefix(line, "@@")
		if hunk {
			inHeader = false
			// A normal @@ hunk has one prefix column; a combined @@@ hunk has
			// two, one per parent. A line is colored only when those columns agree.
			prefixWidth = max(1, len(line)-len(strings.TrimLeft(line, "@"))-1)
			oldLine, newLine = 0, 0
			oldSet := false
			for _, field := range strings.Fields(line) {
				if len(field) < 2 || field[0] != '-' && field[0] != '+' {
					continue
				}
				start, _ := strconv.Atoi(strings.SplitN(field[1:], ",", 2)[0])
				if field[0] == '-' && !oldSet {
					oldLine, oldSet = start, true // Combined diffs use the first parent's range.
				} else if field[0] == '+' {
					newLine = start
				}
			}
		} else if inHeader && (strings.HasPrefix(line, "diff --") || strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")) {
			continue
		}
		display := displayLine{text: line}
		if hunk {
			display.tag = "hunk"
		} else if !inHeader && len(line) >= prefixWidth {
			prefix := line[:prefixWidth]
			valid := true
			for _, marker := range prefix {
				valid = valid && (marker == ' ' || marker == '+' || marker == '-')
			}
			switch {
			case strings.Contains(prefix, "+") && !strings.Contains(prefix, "-"):
				display.tag = "added"
			case strings.Contains(prefix, "-") && !strings.Contains(prefix, "+"):
				display.tag = "removed"
			}
			if valid {
				// A deletion advances only the old side; an addition advances only
				// the result. Combined diffs map the old side to their first parent.
				inResult := !strings.Contains(prefix, "-")
				if prefix[0] == '-' || prefix[0] == ' ' && inResult {
					display.old, oldLine = oldLine, oldLine+1
				}
				if inResult {
					display.new, newLine = newLine, newLine+1
				}
			}
		}
		lines = append(lines, display)
	}
	return lines
}

func splitAfterLines(text string) []string {
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}

func (app *giti) onWhitespaceToggled() {
	if app.currentRow != nil {
		app.loadHistory(false)
	}
}

func (app *giti) resetDiffOverview() {
	app.overviewMarkers, app.overviewLines = app.overviewMarkers[:0], 0
	if app.diffOverviewReveal != nil {
		app.diffOverviewReveal.SetRevealChild(false)
		app.diffOverview.QueueDraw()
	}
}

func (app *giti) scrollDiffOverview(y float64) {
	height := app.diffOverview.GetAllocatedHeight()
	if height < 1 || app.overviewLines < 1 {
		return
	}
	adjustment := app.diffScroller.GetVAdjustment()
	ratio := max(0, min(y/float64(height), 1))
	adjustment.SetValue(adjustment.GetLower() + ratio*max(0, adjustment.GetUpper()-adjustment.GetLower()-adjustment.GetPageSize()))
}
