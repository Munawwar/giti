package main

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gotk3/gotk3/cairo"
	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"
)

func (app *giti) setCommitHeader(details commitDetails) {
	// The title, commit identity, and detail regions change with selection. The
	// action row stays mounted so its toggles retain their state and callbacks.
	app.headerReferenceButtons, app.headerParentButtons = nil, nil
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
	authored := formatCommitTime(details.authored, time.Local, "2 Jan 2006, 15:04 MST")
	committed := formatCommitTime(details.committed, time.Local, "2 Jan 2006, 15:04 MST")
	authoredUTC := formatCommitTime(details.authored, time.UTC, time.RFC3339)
	committedUTC := formatCommitTime(details.committed, time.UTC, time.RFC3339)
	if details.author == details.committer && details.authorEmail == details.committerEmail && details.authored == details.committed {
		meta.SetMarkup(fmt.Sprintf("<span foreground=\"#4b5563\"><b>Author &amp; committer</b> %s &lt;%s&gt;  ·  %s</span>", html.EscapeString(details.author), html.EscapeString(details.authorEmail), html.EscapeString(authored)))
		meta.SetTooltipText("Author & committer: " + authoredUTC)
	} else {
		meta.SetMarkup(fmt.Sprintf("<span foreground=\"#4b5563\"><b>Author</b> %s &lt;%s&gt;  ·  %s\n<b>Committer</b> %s &lt;%s&gt;  ·  %s</span>", html.EscapeString(details.author), html.EscapeString(details.authorEmail), html.EscapeString(authored), html.EscapeString(details.committer), html.EscapeString(details.committerEmail), html.EscapeString(committed)))
		meta.SetTooltipText(fmt.Sprintf("Author: %s\nCommitter: %s", authoredUTC, committedUTC))
	}
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
			button := must(gtk.ButtonNewWithLabel(parent[:min(7, len(parent))]))
			button.SetRelief(gtk.RELIEF_NONE)
			button.SetTooltipText("Open parent commit " + parent)
			setAccessibility(&button.Widget, "Open parent commit "+parent, "Navigate to the parent commit")
			context, _ := button.GetStyleContext()
			context.AddClass("giti-parent-button")
			button.Connect("clicked", func() {
				app.historySearch.SetText("")
				app.revealHistoryRevision(parent)
			})
			app.headerParentButtons = append(app.headerParentButtons, button)
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
	fullFile := app.fullFileToggle.GetActive()
	app.diffGutter.SetVisible((fullFile && app.fullLineNumbers || !fullFile && app.compactLineNumbers) && maxLine > 0)
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
		app.whitespacePreferred = app.whitespaceToggle.GetActive()
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

func (app *giti) buildDiffFind(diffPage gtk.IWidget) *gtk.Overlay {
	// Keep find controls inside the diff page: the stack naturally hides them
	// on the references page, while the overlay preserves diff viewport space.
	app.diffFind = must(gtk.SearchEntryNew())
	app.diffFind.SetPlaceholderText("Find in diff")
	app.diffFind.SetWidthChars(24)
	setAccessibility(&app.diffFind.Widget, "Find in diff", "Case-insensitive search within the displayed diff")
	app.diffFindCount = must(gtk.LabelNew(""))
	app.diffFindCount.SetWidthChars(12)
	setAccessibility(&app.diffFindCount.Widget, "Diff search match count", "Current match and total matches")
	iconButton := func(icon, name, tooltip string) *gtk.Button {
		button := must(gtk.ButtonNewFromIconName(icon, gtk.ICON_SIZE_BUTTON))
		button.SetRelief(gtk.RELIEF_NONE)
		button.SetTooltipText(tooltip)
		setAccessibility(&button.Widget, name, tooltip)
		return button
	}
	app.diffFindPrevious = iconButton("go-up-symbolic", "Previous diff match", "Previous match (Shift+Enter)")
	app.diffFindNext = iconButton("go-down-symbolic", "Next diff match", "Next match (Enter)")
	closeButton := iconButton("window-close-symbolic", "Close diff search", "Close diff search (Escape)")
	app.diffFindPrevious.SetSensitive(false)
	app.diffFindNext.SetSensitive(false)

	app.diffFindBox = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 2))
	app.diffFindBox.SetHAlign(gtk.ALIGN_END)
	app.diffFindBox.SetVAlign(gtk.ALIGN_START)
	app.diffFindBox.SetMarginTop(8)
	app.diffFindBox.SetMarginEnd(8)
	findContext, _ := app.diffFindBox.GetStyleContext()
	findContext.AddClass("giti-diff-find")
	app.diffFindBox.PackStart(app.diffFind, true, true, 0)
	app.diffFindBox.PackStart(app.diffFindCount, false, false, 2)
	app.diffFindBox.PackStart(app.diffFindPrevious, false, false, 0)
	app.diffFindBox.PackStart(app.diffFindNext, false, false, 0)
	app.diffFindBox.PackStart(closeButton, false, false, 0)
	// Keep the children ready to display, but exclude the floating bar itself
	// from repository-opening window.ShowAll calls until Ctrl+F explicitly opens it.
	app.diffFindBox.ShowAll()
	app.diffFindBox.Hide()
	app.diffFindBox.SetNoShowAll(true)

	// Clear stale highlights immediately, then debounce the potentially large
	// buffer scan. The generation makes superseded timers harmless.
	app.diffFind.Connect("changed", func() {
		app.diffFindGeneration++
		generation := app.diffFindGeneration
		app.clearDiffFindMatches()
		query, _ := app.diffFind.GetText()
		if query != "" {
			addMainSource(120*time.Millisecond, func() bool {
				if generation == app.diffFindGeneration && app.diffFindBox.GetVisible() {
					app.updateDiffFind()
				}
				return false
			})
		}
	})
	// Route entry and toplevel events through one handler so Ctrl+F works from
	// any pane while Enter, Shift+Enter, and Escape remain browser-like.
	app.diffFind.Connect("key-press-event", func(_ *gtk.SearchEntry, event *gdk.Event) bool {
		key := gdk.EventKeyNewFromEvent(event)
		return app.handleDiffFindKey(key.KeyVal(), gdk.ModifierType(key.State()))
	})
	app.diffFindPrevious.Connect("clicked", func() { app.moveDiffFind(-1) })
	app.diffFindNext.Connect("clicked", func() { app.moveDiffFind(1) })
	closeButton.Connect("clicked", app.closeDiffFind)
	app.diffFind.Connect("stop-search", app.closeDiffFind)
	app.window.Connect("key-press-event", func(_ any, event *gdk.Event) bool {
		key := gdk.EventKeyNewFromEvent(event)
		return app.handleDiffFindKey(key.KeyVal(), gdk.ModifierType(key.State()))
	})
	overlay := must(gtk.OverlayNew())
	overlay.Add(diffPage)
	overlay.AddOverlay(app.diffFindBox)
	return overlay
}

func (app *giti) handleDiffFindKey(key uint, modifiers gdk.ModifierType) bool {
	switch {
	case modifiers&gdk.CONTROL_MASK != 0 && modifiers&gdk.SHIFT_MASK == 0 && (key == gdk.KEY_f || key == gdk.KEY_F):
		if app.diffStack == nil || app.diffStack.GetVisibleChildName() != "diff" {
			return false
		}
		app.diffFindBox.Show()
		app.updateDiffFind()
		app.diffFind.GrabFocus()
		app.diffFind.SelectRegion(0, -1)
	case (key == gdk.KEY_Return || key == gdk.KEY_KP_Enter) && app.diffFind.IsFocus():
		direction := 1
		if modifiers&gdk.SHIFT_MASK != 0 {
			direction = -1
		}
		app.moveDiffFind(direction)
	case key == gdk.KEY_Escape && app.diffFindBox.GetVisible():
		app.closeDiffFind()
	default:
		return false
	}
	return true
}

func (app *giti) clearDiffFindMatches() {
	start, end := app.diffBuffer.GetBounds()
	app.diffBuffer.RemoveTagByName(diffFindTag, start, end)
	app.diffBuffer.RemoveTagByName(diffFindCurrentTag, start, end)
	app.diffFindMatches, app.diffFindIndex, app.diffFindLimited = app.diffFindMatches[:0], -1, false
	app.diffFindCount.SetText("")
	app.diffFindPrevious.SetSensitive(false)
	app.diffFindNext.SetSensitive(false)
}

func (app *giti) updateDiffFind() {
	app.diffFindGeneration++
	app.clearDiffFindMatches()
	query, _ := app.diffFind.GetText()
	if query == "" || !app.diffFindBox.GetVisible() {
		return
	}
	flags := gtk.TEXT_SEARCH_CASE_INSENSITIVE | gtk.TEXT_SEARCH_VISIBLE_ONLY | gtk.TEXT_SEARCH_TEXT_ONLY
	for cursor := app.diffBuffer.GetStartIter(); ; {
		matchStart, matchEnd, found := cursor.ForwardSearch(query, flags, nil)
		if !found {
			break
		}
		if len(app.diffFindMatches) == maxDiffFindMatches {
			app.diffFindLimited = true
			break
		}
		// Applying millions of tags can freeze GTK for common one-character
		// queries; report the bounded result as 5000+ and keep navigation usable.
		app.diffFindMatches = append(app.diffFindMatches, diffFindMatch{matchStart.GetOffset(), matchEnd.GetOffset()})
		app.diffBuffer.ApplyTagByName(diffFindTag, matchStart, matchEnd)
		cursor = matchEnd
	}
	if len(app.diffFindMatches) == 0 {
		app.diffFindCount.SetText("0 / 0")
		return
	}
	app.diffFindPrevious.SetSensitive(true)
	app.diffFindNext.SetSensitive(true)
	app.selectDiffFindMatch(0)
}

func (app *giti) selectDiffFindMatch(index int) {
	if len(app.diffFindMatches) == 0 {
		return
	}
	start, end := app.diffBuffer.GetBounds()
	app.diffBuffer.RemoveTagByName(diffFindCurrentTag, start, end)
	app.diffFindIndex = (index%len(app.diffFindMatches) + len(app.diffFindMatches)) % len(app.diffFindMatches)
	match := app.diffFindMatches[app.diffFindIndex]
	start, end = app.diffBuffer.GetIterAtOffset(match.start), app.diffBuffer.GetIterAtOffset(match.end)
	app.diffBuffer.ApplyTagByName(diffFindCurrentTag, start, end)
	limited := ""
	if app.diffFindLimited {
		limited = "+"
	}
	app.diffFindCount.SetText(fmt.Sprintf("%d / %d%s", app.diffFindIndex+1, len(app.diffFindMatches), limited))
	app.diffView.ScrollToIter(start, .2, true, .5, .35)
}

func (app *giti) moveDiffFind(direction int) {
	if len(app.diffFindMatches) > 0 {
		app.selectDiffFindMatch(app.diffFindIndex + direction)
	}
}

func (app *giti) closeDiffFind() {
	app.diffFindGeneration++
	app.diffFindBox.Hide()
	app.clearDiffFindMatches()
	app.diffView.GrabFocus()
}

func (app *giti) buildDiffGutter() {
	app.diffGutter = must(gtk.DrawingAreaNew())
	app.diffGutterDigits, app.diffGutterWidth = 2, 80
	app.diffGutter.SetSizeRequest(app.diffGutterWidth, -1)
	app.diffGutter.SetVAlign(gtk.ALIGN_FILL)
	app.diffGutter.SetVExpand(true)
	app.diffGutter.SetTooltipText("Old and new file line numbers")
	setAccessibility(&app.diffGutter.Widget, "Diff line numbers", "Old or first-parent line numbers followed by resulting-file line numbers")
	app.diffGutter.Connect("draw", func(_ *gtk.DrawingArea, context *cairo.Context) bool {
		width, height := app.diffGutter.GetAllocatedWidth(), app.diffGutter.GetAllocatedHeight()
		context.SetSourceRGB(.965, .97, .975)
		context.Paint()
		if len(app.diffLineNumbers) == 0 || height < 1 {
			return false
		}
		// TextView reports line positions in buffer coordinates; subtract its
		// visible origin so the independent gutter tracks vertical scrolling.
		// Wrap is disabled, so every logical line has the same measured height.
		visible := app.diffView.GetVisibleRect()
		first := app.diffBuffer.GetStartIter()
		firstY, lineHeight := app.diffView.GetLineYrange(first)
		if lineHeight < 1 {
			return false
		}
		line := max(0, (visible.GetY()-firstY)/lineHeight)
		context.SelectFontFace("monospace", cairo.FONT_SLANT_NORMAL, cairo.FONT_WEIGHT_NORMAL)
		context.SetFontSize(max(8, float64(lineHeight)*.68))
		digitWidth := context.TextExtents("0").XAdvance
		// Resize only when the file's digit count or scaled font changes, keeping
		// the two right-aligned columns compact without clipping large files.
		columnWidth := int(math.Ceil(digitWidth*float64(app.diffGutterDigits) + 12))
		requiredWidth := columnWidth*2 + 1
		if requiredWidth != app.diffGutterWidth {
			app.diffGutterWidth = requiredWidth
			app.diffGutter.SetSizeRequest(requiredWidth, -1)
			return false
		}
		font := context.FontExtents()
		for line < len(app.diffLineNumbers) {
			iter := app.diffBuffer.GetIterAtLine(line)
			bufferY, rowHeight := app.diffView.GetLineYrange(iter)
			y := bufferY - visible.GetY()
			if y >= height {
				break
			}
			numbers := app.diffLineNumbers[line]
			switch numbers.kind {
			case diffLineAdded:
				context.SetSourceRGB(.843, .961, .867)
			case diffLineRemoved:
				context.SetSourceRGB(.976, .843, .851)
			default:
				context.SetSourceRGB(.965, .97, .975)
			}
			context.Rectangle(0, float64(y), float64(width), float64(rowHeight))
			context.Fill()
			switch numbers.kind {
			case diffLineAdded:
				context.SetSourceRGB(.09, .30, .13)
			case diffLineRemoved:
				context.SetSourceRGB(.41, .13, .15)
			default:
				context.SetSourceRGB(.42, .45, .50)
			}
			baseline := float64(y) + (float64(rowHeight)-font.Ascent-font.Descent)/2 + font.Ascent
			for column, number := range []uint32{numbers.old, numbers.new} {
				if number == 0 {
					continue
				}
				text := strconv.FormatUint(uint64(number), 10)
				context.MoveTo(float64((column+1)*columnWidth-6)-context.TextExtents(text).XAdvance, baseline)
				context.ShowText(text)
			}
			line++
		}
		context.SetSourceRGB(.83, .85, .87)
		context.SetLineWidth(1)
		for _, x := range []float64{float64(columnWidth) + .5, float64(width) - .5} {
			context.MoveTo(x, 0)
			context.LineTo(x, float64(height))
		}
		context.Stroke()
		return false
	})
}

func (app *giti) buildDiffOverview() {
	const targetWidth, trackWidth = 24, 10
	app.diffOverview = must(gtk.DrawingAreaNew())
	app.diffOverview.SetSizeRequest(targetWidth, -1)
	app.diffOverview.SetVAlign(gtk.ALIGN_FILL)
	app.diffOverview.SetVExpand(true)
	app.diffOverview.SetTooltipText("Change overview — click or drag to scroll")
	app.diffOverview.AddEvents(int(gdk.BUTTON_PRESS_MASK | gdk.BUTTON1_MOTION_MASK))
	setAccessibility(&app.diffOverview.Widget, "Diff change overview", "Added and removed lines across the full file; click or drag to scroll")
	app.diffOverview.Connect("draw", func(_ *gtk.DrawingArea, context *cairo.Context) bool {
		width, height := float64(app.diffOverview.GetAllocatedWidth()), float64(app.diffOverview.GetAllocatedHeight())
		trackX := (width - trackWidth) / 2
		context.SetSourceRGB(.97, .97, .97)
		context.Paint()
		context.SetSourceRGB(.94, .95, .96)
		context.Rectangle(trackX, 0, trackWidth, height)
		context.Fill()
		if app.overviewLines < 1 || height < 1 {
			return false
		}
		markerHeight, denominator := math.Max(2, math.Min(5, height/float64(app.overviewLines))), float64(max(1, app.overviewLines-1))
		for _, marker := range app.overviewMarkers {
			x, markerWidth := trackX, trackWidth/2.0
			if marker.added {
				x = markerWidth
				context.SetSourceRGB(.18, .55, .28)
			} else {
				context.SetSourceRGB(.78, .25, .30)
			}
			context.Rectangle(x, float64(marker.line)/denominator*(height-markerHeight), markerWidth, markerHeight)
			context.Fill()
		}
		return false
	})
	app.diffOverview.Connect("button-press-event", func(_ *gtk.DrawingArea, event *gdk.Event) bool {
		button := gdk.EventButtonNewFromEvent(event)
		if button.Button() == gdk.BUTTON_PRIMARY {
			app.scrollDiffOverview(button.Y())
			return true
		}
		return false
	})
	app.diffOverview.Connect("motion-notify-event", func(_ *gtk.DrawingArea, event *gdk.Event) bool {
		motion := gdk.EventMotionNewFromEvent(event)
		if motion.State()&gdk.BUTTON1_MASK != 0 {
			_, y := motion.MotionVal()
			app.scrollDiffOverview(y)
			return true
		}
		return false
	})
}

func scroller(child gtk.IWidget) *gtk.ScrolledWindow {
	scroll := must(gtk.ScrolledWindowNew(nil, nil))
	scroll.SetPolicy(gtk.POLICY_AUTOMATIC, gtk.POLICY_AUTOMATIC)
	scroll.Add(child)
	return scroll
}

func (app *giti) copySHAButton(sha string) *gtk.Button {
	button := must(gtk.ButtonNewFromIconName("edit-copy-symbolic", gtk.ICON_SIZE_BUTTON))
	setAccessibility(&button.Widget, "Copy commit SHA", "Copy "+sha+" to the clipboard")
	button.SetRelief(gtk.RELIEF_NONE)
	context, _ := button.GetStyleContext()
	context.AddClass("giti-flat-button")
	button.SetTooltipText("Copy SHA: " + sha)
	button.Connect("clicked", func() {
		app.copyToClipboard(sha, "Copied commit ID to clipboard.")
	})
	return button
}

func (app *giti) rememberDiffScroll() {
	if app.diffLoaded && app.currentRow != nil && app.currentFile != nil {
		app.diffScroll[diffKey(*app.currentRow, *app.currentFile)] = scrollPosition{app.diffScroller.GetHAdjustment().GetValue(), app.diffScroller.GetVAdjustment().GetValue()}
	}
}

// Search results are independent of graph pagination and selection-driven loads;
// only an explicit repository refresh needs to query them for new commits.

func (app *giti) buildContentPane() *gtk.Box {
	// The diff pane owns both the text rendering and the optional full-file
	// overview; selection changes update them as a single unit.
	app.diffBuffer = must(gtk.TextBufferNew(nil))
	app.diffBuffer.CreateTag("added", map[string]any{"background": "#d7f5dd", "foreground": "#174d22"})
	app.diffBuffer.CreateTag("removed", map[string]any{"background": "#f9d7d9", "foreground": "#682126"})
	app.diffBuffer.CreateTag("hunk", map[string]any{"paragraph-background": "#eef0f2", "foreground": "#4b5563"})
	app.diffBuffer.CreateTag(diffFindTag, map[string]any{"background": "#fff2a8"})
	app.diffBuffer.CreateTag(diffFindCurrentTag, map[string]any{"background": "#f6bd4f", "foreground": "#2d2100"})
	app.diffView = must(gtk.TextViewNewWithBuffer(app.diffBuffer))
	setAccessibility(&app.diffView.Widget, "Commit diff", "Patch for the selected file; additions begin with plus and removals with minus")
	app.diffView.SetEditable(false)
	app.diffView.SetCursorVisible(false)
	app.diffView.SetMonospace(true)
	app.diffView.SetWrapMode(gtk.WRAP_NONE)
	app.diffContextLine = -1
	app.diffView.AddEvents(int(gdk.BUTTON_PRESS_MASK))
	app.diffView.Connect("button-press-event", func(_ *gtk.TextView, event *gdk.Event) bool {
		button := gdk.EventButtonNewFromEvent(event)
		if button.Type() != gdk.EVENT_BUTTON_PRESS || button.Button() != gdk.BUTTON_SECONDARY {
			return false
		}
		_, y := app.diffView.WindowToBufferCoords(gtk.TEXT_WINDOW_WIDGET, int(button.X()), int(button.Y()))
		iter := app.diffView.GetIterAtLocation(0, y)
		lineY, lineHeight := app.diffView.GetLineYrange(iter)
		app.diffContextLine = iter.GetLine()
		if y < lineY || y >= lineY+lineHeight {
			app.diffContextLine = -1
		}
		return false
	})
	app.diffView.Connect("populate-popup", func(_ *gtk.TextView, menuWidget *gtk.Widget) { app.populateDiffMenu(menuWidget) })
	app.buildDiffOverview()
	app.buildDiffGutter()

	app.whitespaceToggle = must(gtk.CheckButtonNewWithLabel("Whitespace changes"))
	app.whitespaceToggle.SetTooltipText("Include whitespace-only changes in file lists and diffs")
	app.whitespaceHandler = app.whitespaceToggle.Connect("toggled", app.onWhitespaceToggled)
	app.fullFileToggle = must(gtk.CheckButtonNewWithLabel("Show full file"))
	app.fullFileToggle.SetTooltipText("Show unchanged lines from the complete file")
	app.fullFileHandler = app.fullFileToggle.Connect("toggled", func() {
		app.fullFilePreferred = app.fullFileToggle.GetActive()
		if app.currentFile != nil {
			app.onFileSelected()
		}
	})
	app.fullMergeToggle = must(gtk.CheckButtonNewWithLabel("Full merge"))
	app.fullMergeToggle.SetTooltipText("Off: show the compact combined merge-resolution diff; on: compare the merge with its first parent")
	app.fullMergeToggle.SetNoShowAll(true)
	app.fullMergeToggle.SetVisible(false)
	app.fullMergeHandler = app.fullMergeToggle.Connect("toggled", func() {
		if app.currentRow != nil && app.currentRow.kind == "commit" && len(app.currentRow.parents) > 1 {
			app.onHistorySelected()
		}
	})
	app.commitHeader = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 2))
	app.commitHeader.SetMarginStart(12)
	app.commitHeader.SetMarginEnd(12)
	app.commitHeader.SetMarginTop(8)
	app.commitHeader.SetMarginBottom(8)
	app.headerTitleRow = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	headerActionRow := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	app.headerCommit = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	app.headerDetails = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 2))
	headerActionRow.PackStart(app.headerCommit, false, false, 0)
	headerActionRow.PackEnd(app.whitespaceToggle, false, false, 8)
	headerActionRow.PackEnd(app.fullFileToggle, false, false, 0)
	headerActionRow.PackEnd(app.fullMergeToggle, false, false, 8)
	app.commitHeader.PackStart(app.headerTitleRow, false, false, 0)
	app.commitHeader.PackStart(headerActionRow, false, false, 0)
	app.commitHeader.PackStart(app.headerDetails, false, false, 0)
	diffBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
	diffBox.PackStart(app.commitHeader, false, false, 0)
	app.diffScroller = scroller(app.diffView)
	app.diffScroller.GetVAdjustment().Connect("value-changed", app.diffGutter.QueueDraw)
	app.diffOverviewReveal = must(gtk.RevealerNew())
	app.diffOverviewReveal.SetTransitionDuration(0)
	app.diffOverviewReveal.Add(app.diffOverview)
	diffPage := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	diffPage.PackStart(app.diffGutter, false, true, 0)
	diffPage.PackStart(app.diffScroller, true, true, 0)
	diffPage.PackStart(app.diffOverviewReveal, false, true, 0)
	app.referencesPage = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 8))
	app.referencesPage.SetMarginStart(16)
	app.referencesPage.SetMarginEnd(16)
	app.referencesPage.SetMarginTop(16)
	app.referencesPage.SetMarginBottom(16)
	app.diffStack = must(gtk.StackNew())
	app.diffStack.AddNamed(app.buildDiffFind(diffPage), "diff")
	app.diffStack.AddNamed(scroller(app.referencesPage), "references")
	diffBox.PackStart(app.diffStack, true, true, 0)
	return diffBox
}

func (app *giti) populateDiffMenu(menuWidget *gtk.Widget) (*gtk.MenuItem, *gtk.CheckMenuItem) {
	if app.currentFile == nil || !app.diffLoaded {
		return nil, nil
	}
	menu := &gtk.Menu{MenuShell: gtk.MenuShell{Container: gtk.Container{Widget: *menuWidget}}}
	children := menu.GetChildren()
	children.Foreach(func(item any) { item.(*gtk.Widget).Destroy() })
	children.Free()
	clipboard := must(gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD))
	copyItem := must(gtk.MenuItemNewWithLabel("Copy"))
	_, _, selected := app.diffBuffer.GetSelectionBounds()
	copyItem.SetSensitive(selected)
	copyItem.Connect("activate", func() { app.diffBuffer.CopyClipboard(clipboard) })
	menu.Append(copyItem)
	copyPath := must(gtk.MenuItemNewWithLabel("Copy Path"))
	line := app.diffContextLine
	if line < 0 || line >= len(app.diffLineNumbers) || app.diffLineNumbers[line].new == 0 {
		copyPath.SetSensitive(false)
	} else {
		target := fmt.Sprintf("%s:%d:0", app.currentFile.path, app.diffLineNumbers[line].new)
		copyPath.Connect("activate", func() { app.copyToClipboard(target, "Copied line path to clipboard.") })
	}
	menu.Append(copyPath)
	selectAll := must(gtk.MenuItemNewWithLabel("Select All"))
	selectAll.Connect("activate", func() { app.diffBuffer.SelectRange(app.diffBuffer.GetBounds()) })
	menu.Append(selectAll)
	menu.Append(must(gtk.SeparatorMenuItemNew()))
	lineNumbers := must(gtk.CheckMenuItemNewWithLabel("Show line numbers"))
	fullFile := app.fullFileToggle.GetActive()
	lineNumbers.SetActive(fullFile && app.fullLineNumbers || !fullFile && app.compactLineNumbers)
	lineNumbers.Connect("toggled", func() {
		if fullFile {
			app.fullLineNumbers = lineNumbers.GetActive()
		} else {
			app.compactLineNumbers = lineNumbers.GetActive()
			_ = patchUIState(app.statePath, func(state *uiState) { state.CompactLineNumbers = app.compactLineNumbers })
		}
		hasNumbers := false
		for _, numbers := range app.diffLineNumbers {
			hasNumbers = hasNumbers || numbers.old > 0 || numbers.new > 0
		}
		app.diffGutter.SetVisible(lineNumbers.GetActive() && hasNumbers)
		app.diffGutter.QueueDraw()
	})
	menu.Append(lineNumbers)
	menu.ShowAll()
	return copyPath, lineNumbers
}
