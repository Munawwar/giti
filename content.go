package main

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gotk3/gotk3/cairo"
	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"
)

func (app *giti) setCommitHeader(details commitDetails) {
	// The title, identity, and message regions change with selection. The rows
	// and their actions stay mounted so toggles keep their state and callbacks.
	app.headerReferenceButtons, app.headerIdentity = nil, details
	for _, box := range []*gtk.Box{app.headerTitleRow, app.headerStatistics, app.headerSHA, app.headerCommit, app.headerDetails} {
		if children := box.GetChildren(); children != nil {
			children.Foreach(func(child any) { box.Remove(child.(gtk.IWidget)) })
			children.Free()
		}
	}
	// The title stays selectable for copying; the chevron after it opens the full
	// message, which also holds any part of the subject the ellipsis hides.
	title := must(gtk.LabelNew(""))
	title.SetXAlign(0)
	title.SetSelectable(true)
	title.SetEllipsize(pango.ELLIPSIZE_END)
	title.SetMarginStart(4)
	title.SetMarkup("<span size=\"large\" weight=\"bold\">" + html.EscapeString(details.subject) + "</span>")
	title.Connect("size-allocate", func() {
		_, natural := title.GetPreferredWidth()
		title.SetTooltipText(map[bool]string{false: "", true: details.subject}[title.GetAllocatedWidth() < natural])
	})
	app.headerTitleRow.PackStart(title, false, true, 0)
	// Keep aggregate statistics outside the ellipsized title so they remain
	// visible even for unusually long subjects.
	app.headerStatistics.SetVisible(details.statistics)
	if details.statistics {
		additions := must(gtk.LabelNew(fmt.Sprintf("+%d", details.additions)))
		deletions := must(gtk.LabelNew(fmt.Sprintf("−%d", details.deletions)))
		for _, badge := range []*gtk.Label{additions, deletions} {
			context, _ := badge.GetStyleContext()
			context.AddClass("giti-stat")
			badge.SetTooltipText("Line totals exclude binary and untracked files")
			app.headerStatistics.PackStart(badge, false, false, 0)
			badge.Show()
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
			app.headerStatistics.PackStart(untracked, false, false, 0)
			untracked.Show()
		}
	}
	commit := details.sha != ""
	app.headerIdentityButton.SetVisible(commit)
	if !commit {
		// Working-tree views have no identity, message, or parents: one commit row.
		app.headerAuthor.SetText("")
		app.commitHeader.ShowAll()
		app.headerMetaRow.Hide()
		app.updateReviewUI()
		return
	}
	sameIdentity := details.author == details.committer && details.authorEmail == details.committerEmail
	app.headerAuthor.SetMarkup(fmt.Sprintf("<span foreground=\"#4b5563\"><b>%s</b>  ·  %s</span>", html.EscapeString(details.author), html.EscapeString(formatCommitTime(details.authored, time.Local, "2 Jan 2006, 15:04 MST"))))
	app.headerIdentityButton.SetTooltipText("Author details")
	if !sameIdentity {
		app.headerIdentityButton.SetTooltipText("Committed by " + details.committer + " — show details")
	}
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
		// Clip long reference lists rather than pushing the view toggles away;
		// the overflow chips open the complete list.
		clip := must(gtk.ScrolledWindowNew(nil, nil))
		clip.SetPolicy(gtk.POLICY_EXTERNAL, gtk.POLICY_NEVER)
		clip.SetPropagateNaturalHeight(true)
		clip.SetVAlign(gtk.ALIGN_CENTER)
		clip.Add(refs)
		app.headerCommit.PackStart(clip, true, true, 0)
	}
	shaMarkup := "<span foreground=\"#4b5563\" font_features=\"tnum\">" + html.EscapeString(details.sha[:min(7, len(details.sha))]) + "</span>"
	app.headerSHA.PackStart(app.copyButton(shaMarkup, details.sha, "Copy full SHA: "+details.sha, "Copied commit ID to clipboard."), false, false, 0)
	message := strings.TrimSpace(details.subject + "\n\n" + details.body)
	icon := chevron(map[bool]string{false: "pan-down-symbolic", true: "pan-up-symbolic"}[app.headerMessageOpen], 12)
	toggle := must(gtk.ButtonNew())
	toggle.SetImage(icon)
	toggle.SetVAlign(gtk.ALIGN_CENTER)
	toggle.SetTooltipText("Show the full commit message")
	setAccessibility(&toggle.Widget, "Commit message", "Show or hide the complete commit message")
	squareButton(toggle)
	app.headerTitleRow.PackStart(toggle, false, false, 0)
	body := must(gtk.LabelNew(message))
	body.SetXAlign(0)
	body.SetYAlign(0)
	body.SetSelectable(true)
	body.SetLineWrap(true)
	body.SetLineWrapMode(pango.WRAP_WORD_CHAR)
	text := scroller(body)
	text.SetPolicy(gtk.POLICY_NEVER, gtk.POLICY_AUTOMATIC)
	text.SetMaxContentHeight(180)
	text.SetPropagateNaturalHeight(true)
	panel := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 0))
	panelContext, _ := panel.GetStyleContext()
	panelContext.AddClass("giti-message-panel")
	panel.PackStart(text, true, true, 0)
	reveal := must(gtk.RevealerNew())
	reveal.SetTransitionDuration(0)
	reveal.Add(panel)
	reveal.SetRevealChild(app.headerMessageOpen)
	toggle.Connect("clicked", func() {
		app.headerMessageOpen = !app.headerMessageOpen
		reveal.SetRevealChild(app.headerMessageOpen)
		icon.SetFromIconName(map[bool]string{false: "pan-down-symbolic", true: "pan-up-symbolic"}[app.headerMessageOpen], gtk.ICON_SIZE_MENU)
	})
	app.headerDetails.PackStart(reveal, false, false, 0)
	app.commitHeader.ShowAll()
	app.updateReviewUI()
}

func (app *giti) showDiffPage() {
	if app.diffStack != nil {
		app.diffStack.SetVisibleChildName("diff")
		if app.reviewFileButton != nil {
			app.updateReviewFileActions()
		}
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
	app.updateReviewFileActions()
}

func (app *giti) setDiff(patch string) {
	app.setDiffLines(displayLines(patch))
}

func (app *giti) setDiffLines(lines []displayLine) {
	app.diffBuffer.SetText("")
	app.reviewLines = lines
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
	for index, line := range lines {
		if line.change[1] > line.change[0] {
			app.diffBuffer.ApplyTagByName(line.tag+"-change", app.diffBuffer.GetIterAtLineOffset(index, line.change[0]), app.diffBuffer.GetIterAtLineOffset(index, line.change[1]))
		}
	}
	app.diffGutterDigits = max(2, len(strconv.Itoa(maxLine)))
	fullFile := app.fullFileToggle.GetActive() || app.plainFile != ""
	app.diffGutter.SetVisible((fullFile && app.fullLineNumbers || !fullFile && app.compactLineNumbers) && maxLine > 0)
	app.diffGutter.QueueDraw()
	app.diffOverviewReveal.SetRevealChild(len(app.overviewMarkers) > 0)
	app.diffOverview.QueueDraw()
	app.updateDiffFind()
	app.updateReviewUI()
}

func (app *giti) clearDiff() {
	app.diffBuffer.SetText("")
	app.reviewLines = nil
	app.diffContextLine = -1
	app.diffLineNumbers = app.diffLineNumbers[:0]
	app.diffGutter.Hide()
	app.updateDiffFind()
	app.updateReviewUI()
}

type overviewMarker struct {
	line  int
	added bool
}

type displayLine struct {
	text, tag string
	old, new  int
	change    [2]int // character range that differs from the paired line, if any
}

func displayLines(patch string) []displayLine {
	lines := make([]displayLine, 0)
	inHeader, prefixWidth, oldLine, newLine := true, 1, 0, 0
	// Like Git's contrib diff-highlight, a run of removed lines followed by an equally
	// long run of added lines is paired in order, and each pair marks the characters
	// between their common prefix and suffix. Spans over 1000 characters or half the
	// line are left plain: the line was rewritten rather than edited.
	removed, added := -1, -1
	pairChanges := func() {
		for index := removed; added >= 0 && added-removed == len(lines)-added && index < added; index++ {
			before, after := &lines[index], &lines[index+added-removed]
			if !utf8.ValidString(before.text) || !utf8.ValidString(after.text) {
				continue
			}
			old, new := []rune(strings.TrimSuffix(before.text, "\n")), []rune(strings.TrimSuffix(after.text, "\n"))
			start, end := 1, 0
			for start < min(len(old), len(new)) && old[start] == new[start] {
				start++
			}
			for end < min(len(old), len(new))-start && old[len(old)-1-end] == new[len(new)-1-end] {
				end++
			}
			oldSpan, newSpan := len(old)-end-start, len(new)-end-start
			if max(oldSpan, newSpan) <= 1000 && 2*oldSpan <= len(old)-1 && 2*newSpan <= len(new)-1 {
				if oldSpan > 0 {
					before.change = [2]int{start, start + oldSpan}
				}
				if newSpan > 0 {
					after.change = [2]int{start, start + newSpan}
				}
			}
		}
		removed, added = -1, -1
	}
	for _, line := range splitAfterLines(patch) {
		if strings.HasPrefix(line, "diff --") {
			inHeader, prefixWidth, oldLine, newLine = true, 1, 0, 0
			continue
		}
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
		} else if inHeader && (strings.HasPrefix(line, "index ") ||
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
		switch {
		case display.tag == "removed" && prefixWidth == 1:
			if added >= 0 {
				pairChanges()
			}
			if removed < 0 {
				removed = len(lines)
			}
		case display.tag == "added" && prefixWidth == 1 && removed >= 0:
			if added < 0 {
				added = len(lines)
			}
		default:
			pairChanges()
		}
		lines = append(lines, display)
	}
	pairChanges()
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
	case modifiers&primaryMask != 0 && modifiers&gdk.SHIFT_MASK == 0 && (key == gdk.KEY_f || key == gdk.KEY_F):
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
		// Wrap is disabled, so lines differ in height only by comment card space.
		visible := app.diffView.GetVisibleRect()
		first := app.diffView.GetIterAtLocation(0, visible.GetY())
		line := first.GetLine()
		_, lineHeight := app.diffView.GetLineYrange(first)
		if lineHeight -= app.reviewGaps[line]; line == 0 {
			lineHeight -= app.reviewGaps[-1]
		}
		if lineHeight < 1 {
			return false
		}
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
			bufferY, _ := app.diffView.GetLineYrange(iter)
			if line == 0 {
				bufferY += app.reviewGaps[-1]
			}
			y, rowHeight := bufferY-visible.GetY(), lineHeight
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
	app.diffOverview.SetTooltipText("Changes and comments in this file — click or drag to scroll")
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
		// Place markers by laid-out pixel position, so comment cards and hunks keep
		// the strip in step with what scrolling there will show.
		markerHeight, upper := math.Max(2, math.Min(5, height/float64(app.overviewLines))), math.Max(1, app.diffScroller.GetVAdjustment().GetUpper())
		position := func(line int) float64 {
			y, _ := app.diffView.GetLineYrange(app.diffBuffer.GetIterAtLine(line))
			return float64(y) / upper * (height - markerHeight)
		}
		for _, marker := range app.overviewMarkers {
			x, markerWidth := trackX, trackWidth/2.0
			if marker.added {
				x = markerWidth
				context.SetSourceRGB(.18, .55, .28)
			} else {
				context.SetSourceRGB(.78, .25, .30)
			}
			context.Rectangle(x, position(marker.line), markerWidth, markerHeight)
			context.Fill()
		}
		context.SetSourceRGB(.15, .39, .92)
		for _, line := range app.overviewComments {
			context.Rectangle(trackX, position(line), trackWidth, markerHeight)
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

// chevron is the one arrow style used by menus, disclosures, and file navigation.
func chevron(name string, size int) *gtk.Image {
	image := must(gtk.ImageNewFromIconName(name, gtk.ICON_SIZE_MENU))
	image.SetPixelSize(size)
	return image
}

// squareButton styles a flat icon button so every chevron shares one size.
func squareButton(button *gtk.Button) {
	button.SetRelief(gtk.RELIEF_NONE)
	context, _ := button.GetStyleContext()
	context.AddClass("giti-glyph-button")
	context.AddClass("giti-square-button")
}

// copyButton is the app's compact copy control: optional visible text followed by a ⧉ glyph.
func (app *giti) copyButton(markup, value, tooltip, message string) *gtk.Button {
	label := must(gtk.LabelNew(""))
	label.SetMarkup(strings.TrimSpace(markup + " ⧉"))
	button := must(gtk.ButtonNew())
	button.Add(label)
	button.SetRelief(gtk.RELIEF_NONE)
	button.SetTooltipText(tooltip)
	setAccessibility(&button.Widget, tooltip, "Copy to the clipboard")
	context, _ := button.GetStyleContext()
	context.AddClass("giti-glyph-button")
	button.Connect("clicked", func() { app.copyToClipboard(value, message) })
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
	app.diffBuffer.CreateTag("added-change", map[string]any{"background": "#a8e6b6"})
	app.diffBuffer.CreateTag("removed-change", map[string]any{"background": "#f2b1b6"})
	app.diffBuffer.CreateTag("hunk", map[string]any{"paragraph-background": "#eef0f2", "foreground": "#4b5563"})
	app.diffBuffer.CreateTag("reviewed-added", map[string]any{"background": "#e7ece8", "paragraph-background": "#e7ece8"})
	app.diffBuffer.CreateTag("reviewed-removed", map[string]any{"background": "#eee7e8", "paragraph-background": "#eee7e8"})
	app.diffBuffer.CreateTag("reviewed-context", map[string]any{"paragraph-background": "#edf0ef"})
	app.diffBuffer.CreateTag("review-comment", map[string]any{"background": "#dce8fb", "paragraph-background": "#dce8fb"})
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
		if button.Type() == gdk.EVENT_BUTTON_PRESS && button.Button() == gdk.BUTTON_PRIMARY {
			app.diffSelecting = true
			app.placeReviewFloaters()
		}
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

	app.whitespaceToggle = must(gtk.CheckButtonNewWithLabel("Whitespace"))
	app.whitespaceToggle.SetTooltipText("Include whitespace-only changes in file lists and diffs")
	app.whitespaceHandler = app.whitespaceToggle.Connect("toggled", app.onWhitespaceToggled)
	app.fullFileToggle = must(gtk.CheckButtonNewWithLabel("Full file"))
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
	app.headerStatistics = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	app.headerStatistics.SetNoShowAll(true)
	app.headerStatistics.SetVAlign(gtk.ALIGN_CENTER)
	app.buildReviewControls()
	// Commit section, then a hairline, then the file row. The right-hand columns of
	// the title row and the file row share widths, so review buttons, diff counts,
	// and the ⋮ / › icons line up vertically.
	titleRow := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	titleRow.PackStart(app.headerTitleRow, true, true, 0)
	for _, widget := range []gtk.IWidget{app.reviewMenuButton, app.headerStatistics, app.reviewModeButton, app.reviewFilesLabel} {
		titleRow.PackEnd(widget, false, false, 0)
	}
	// Author details sit behind one popover so names stay short in the header.
	app.headerAuthor = must(gtk.LabelNew(""))
	app.headerAuthor.SetXAlign(0)
	app.headerAuthor.SetEllipsize(pango.ELLIPSIZE_END)
	app.headerAuthor.SetSelectable(true)
	app.headerAuthor.SetMarginStart(4) // Line up with the text inside the compact buttons above and below.
	app.headerIdentityButton = must(gtk.ButtonNewFromIconName("dialog-information-symbolic", gtk.ICON_SIZE_MENU))
	app.headerIdentityButton.SetRelief(gtk.RELIEF_NONE)
	app.headerIdentityButton.SetVAlign(gtk.ALIGN_CENTER)
	app.headerIdentityButton.SetNoShowAll(true)
	identityContext, _ := app.headerIdentityButton.GetStyleContext()
	identityContext.AddClass("giti-glyph-button")
	setAccessibility(&app.headerIdentityButton.Widget, "Author and committer", "Show author and committer names, emails, and dates")
	app.headerIdentityButton.Connect("clicked", func() {
		details := app.headerIdentity
		grid := must(gtk.GridNew())
		grid.SetColumnSpacing(12)
		grid.SetRowSpacing(6)
		for _, margin := range []func(int){grid.SetMarginStart, grid.SetMarginEnd, grid.SetMarginTop, grid.SetMarginBottom} {
			margin(10)
		}
		when := func(date string) string { return formatCommitTime(date, time.Local, "2 Jan 2006, 15:04 MST") }
		identities := [][4]string{{"Author", details.author, details.authorEmail, when(details.authored)}, {"Committer", details.committer, details.committerEmail, when(details.committed)}}
		if details.author == details.committer && details.authorEmail == details.committerEmail {
			identities[0][0], identities = "Author & committer", identities[:1]
			if details.authored != details.committed {
				identities[0][3] += "  ·  committed " + when(details.committed)
			}
		}
		for row, identity := range identities {
			person := identity[1] + " <" + identity[2] + ">"
			for column, markup := range []string{"<b>" + html.EscapeString(identity[0]) + "</b>", html.EscapeString(person), "<span foreground=\"#4b5563\">" + html.EscapeString(identity[3]) + "</span>"} {
				label := must(gtk.LabelNew(""))
				label.SetMarkup(markup)
				label.SetXAlign(0)
				label.SetSelectable(column == 1)
				grid.Attach(label, column, row, 1, 1)
			}
			grid.Attach(app.copyButton("", person, "Copy "+person, "Copied "+strings.ToLower(identity[0])+" to clipboard."), 3, row, 1, 1)
		}
		popover := must(gtk.PopoverNew(app.headerIdentityButton))
		popover.Add(grid)
		popover.Connect("closed", popover.Destroy)
		grid.ShowAll()
		popover.Popup()
	})
	app.headerMetaRow = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 2))
	app.headerMetaRow.PackStart(app.headerAuthor, false, true, 0)
	app.headerMetaRow.PackStart(app.headerIdentityButton, false, false, 0)
	app.headerSHA = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	app.headerSHA.SetMarginStart(6)
	app.headerMetaRow.PackStart(app.headerSHA, false, false, 0)
	app.headerCommit = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	app.headerMetaRow.PackEnd(app.headerCommit, true, true, 0)
	app.headerDetails = must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 0))
	divider := must(gtk.SeparatorNew(gtk.ORIENTATION_HORIZONTAL))
	dividerContext, _ := divider.GetStyleContext()
	dividerContext.AddClass("giti-hairline")
	// Equal air on both sides lets the hairline separate the sections without weight.
	divider.SetMarginTop(6)
	divider.SetMarginBottom(6)
	app.headerFileName = must(gtk.LabelNew(""))
	app.headerFileName.SetEllipsize(pango.ELLIPSIZE_MIDDLE)
	app.headerFileName.SetMarginStart(4)
	fileRow := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	fileRow.PackStart(app.headerFileName, false, true, 0)
	for _, toggle := range []*gtk.CheckButton{app.fullFileToggle, app.whitespaceToggle, app.fullMergeToggle} {
		fileRow.PackStart(toggle, false, false, 0)
	}
	app.fileStatistics = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 8))
	app.fileStatistics.SetVAlign(gtk.ALIGN_CENTER)

	app.fileStatistics.SetNoShowAll(true)
	// The shared column takes the commit counts' width; packing from the end keeps
	// the file counts against the › so both rows' bubbles end together.
	for index, class := range []string{"giti-deletions", "giti-additions"} {
		badge := must(gtk.LabelNew(""))
		context, _ := badge.GetStyleContext()
		context.AddClass("giti-stat")
		context.AddClass(class)
		app.fileStatistics.PackEnd(badge, false, false, 0)
		app.fileStatisticLabels[1-index] = badge
	}
	for _, widget := range []gtk.IWidget{app.reviewNextButton, app.fileStatistics, app.reviewFileButton, app.reviewLinesLabel} {
		fileRow.PackEnd(widget, false, false, 0)
	}
	for _, column := range [][2]gtk.IWidget{{app.reviewMenuButton, app.reviewNextButton}, {app.headerStatistics, app.fileStatistics}, {app.reviewModeButton, app.reviewFileButton}, {app.reviewFilesLabel, app.reviewLinesLabel}} {
		group := must(gtk.SizeGroupNew(gtk.SIZE_GROUP_HORIZONTAL))
		group.AddWidget(column[0])
		group.AddWidget(column[1])
	}
	for _, row := range []gtk.IWidget{titleRow, app.headerDetails, app.headerMetaRow, divider, fileRow} {
		app.commitHeader.PackStart(row, false, false, 0)
	}
	diffBox := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 4))
	diffBox.PackStart(app.commitHeader, false, false, 0)
	diffBox.PackStart(app.reviewProgressBar, false, false, 0)
	app.diffScroller = scroller(app.diffView)
	app.diffScroller.GetVAdjustment().Connect("value-changed", app.diffGutter.QueueDraw)
	// Card positions follow line layout, which GTK validates incrementally.
	app.diffScroller.GetVAdjustment().Connect("changed", func() {
		app.positionReviewCards()
		app.diffOverview.QueueDraw()
	})
	app.diffScroller.GetVAdjustment().Connect("value-changed", app.placeReviewFloaters)
	app.diffBuffer.Connect("mark-set", app.placeReviewFloaters)
	// Hovering a line in the text or the gutter offers the + for that line, as on GitHub.
	hover := func(y float64) {
		_, bufferY := app.diffView.WindowToBufferCoords(gtk.TEXT_WINDOW_WIDGET, 0, int(y))
		iter := app.diffView.GetIterAtLocation(0, bufferY)
		lineY, lineHeight := app.diffView.GetLineYrange(iter)
		line := iter.GetLine()
		if bufferY >= lineY+lineHeight-app.reviewGaps[line] {
			line = -1 // Over a comment card's reserved space.
		}
		if line != app.reviewHoverLine {
			app.reviewHoverLine = line
			app.placeReviewFloaters()
		}
	}
	unhover := func() bool {
		// Moving onto the + itself leaves the diff; keep it while the pointer is over it.
		addMainSource(150*time.Millisecond, func() bool {
			if app.reviewAddButton.GetStateFlags()&gtk.STATE_FLAG_PRELIGHT == 0 {
				app.reviewHoverLine = -1
				app.placeReviewFloaters()
			}
			return false
		})
		return false
	}
	for _, widget := range []*gtk.Widget{&app.diffView.Widget, &app.diffGutter.Widget} {
		widget.AddEvents(int(gdk.POINTER_MOTION_MASK | gdk.LEAVE_NOTIFY_MASK))
		widget.Connect("motion-notify-event", func(_ any, event *gdk.Event) bool {
			_, y := gdk.EventMotionNewFromEvent(event).MotionVal()
			hover(y)
			return false
		})
		widget.Connect("leave-notify-event", unhover)
	}
	// The selection toolbar waits until a drag ends, so it never chases the pointer.
	app.diffView.Connect("button-release-event", func() bool {
		app.diffSelecting = false
		app.placeReviewFloaters()
		return false
	})
	app.diffView.Connect("key-press-event", func(_ *gtk.TextView, event *gdk.Event) bool {
		key := gdk.EventKeyNewFromEvent(event)
		_, _, selected := app.diffBuffer.GetSelectionBounds()
		if !app.reviewActive || !selected || key.State()&uint(gdk.CONTROL_MASK|gdk.MOD1_MASK|primaryMask) != 0 {
			return false
		}
		switch key.KeyVal() {
		case gdk.KEY_c, gdk.KEY_C:
			app.actOnReviewSelection(true)
		case gdk.KEY_m, gdk.KEY_M:
			app.actOnReviewSelection(false)
		default:
			return false
		}
		return true
	})
	app.diffScroller.GetHAdjustment().Connect("value-changed", app.positionReviewCards)
	app.diffView.Connect("size-allocate", func() {
		if len(app.reviewCards) > 0 && app.reviewCardWidth != max(240, app.diffView.GetVisibleRect().GetWidth()-24) {
			addMainSource(0, func() bool { app.layoutReviewCards(); return false })
		}
	})
	// Lines are laid out lazily, so the floaters follow the painted layout;
	// unchanged margins are no-ops, so this settles after one frame.
	app.diffView.ConnectAfter("draw", func() bool { app.placeReviewFloaters(); return false })
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
	// While reviewing, a + floats at the gutter edge of the hovered line (as on
	// GitHub), and a toolbar appears under a finished selection.
	app.diffOverlay = app.buildDiffFind(diffPage)
	app.reviewHoverLine = -1
	app.reviewAddButton = must(gtk.ButtonNewWithLabel("+"))
	app.reviewAddButton.SetTooltipText("Comment on this line")
	setAccessibility(&app.reviewAddButton.Widget, "Comment on this line", "Open the inline comment editor for the hovered line")
	addContext, _ := app.reviewAddButton.GetStyleContext()
	addContext.AddClass("giti-comment-add")
	app.reviewAddButton.Connect("clicked", func() {
		line := app.reviewHoverLine
		if app.currentFile == nil || line < 0 {
			return
		}
		app.startReviewDraft(app.currentFile.path, reviewComment{Lines: reviewAnchors(app.reviewLines, line, line+1)})
	})
	app.reviewToolbar = must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	toolbarContext, _ := app.reviewToolbar.GetStyleContext()
	toolbarContext.AddClass("giti-selection-toolbar")
	// Colors follow the rest of review: blue comments, green reviewed; the dim
	// letters are the keyboard shortcuts.
	for _, action := range []struct {
		button  **gtk.Button
		label   **gtk.Label
		tooltip string
		comment bool
	}{{&app.reviewCommentAction, &app.reviewCommentLabel, "Comment on the selected lines (C)", true}, {&app.reviewMarkAction, &app.reviewMarkLabel, "Toggle the selected lines reviewed (M)", false}} {
		if !action.comment {
			app.reviewToolbar.PackStart(must(gtk.SeparatorNew(gtk.ORIENTATION_VERTICAL)), false, false, 0)
		}
		*action.label = must(gtk.LabelNew(""))
		*action.button = must(gtk.ButtonNew())
		(*action.button).Add(*action.label)
		(*action.button).SetRelief(gtk.RELIEF_NONE)
		(*action.button).SetTooltipText(action.tooltip)
		(*action.button).Connect("clicked", func() { app.actOnReviewSelection(action.comment) })
		app.reviewToolbar.PackStart(*action.button, false, false, 0)
	}
	app.reviewCommentLabel.SetMarkup(reviewActionMarkup("Comment", "#1d4ed8", "C"))
	for _, floater := range []*gtk.Widget{&app.reviewAddButton.Widget, &app.reviewToolbar.Widget} {
		floater.SetHAlign(gtk.ALIGN_START)
		floater.SetVAlign(gtk.ALIGN_START)
		floater.ShowAll()
		floater.Hide()
		floater.SetNoShowAll(true)
		app.diffOverlay.AddOverlay(floater)
	}
	app.diffStack.AddNamed(app.diffOverlay, "diff")
	app.diffStack.AddNamed(scroller(app.referencesPage), "references")
	preview := must(gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 0))
	previewToolbar := must(gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 0))
	previewToolbar.SetMarginStart(16)
	previewToolbar.SetMarginEnd(16)
	previewToolbar.SetMarginTop(8)
	// A back chevron on the left, like the search results, and the review's copy action on the right.
	app.reviewPreviewBack = must(gtk.ButtonNewFromIconName("go-previous-symbolic", gtk.ICON_SIZE_BUTTON))
	app.reviewPreviewBack.SetTooltipText("Back to the diff")
	setAccessibility(&app.reviewPreviewBack.Widget, "Back to the diff", "Close the review preview")
	app.reviewPreviewBack.Connect("clicked", app.showDiffPage)
	copyReview := must(gtk.ButtonNewWithLabel("Copy as Markdown"))
	copyReview.Connect("clicked", func() { app.reviewCopyItem.Activate() })
	for _, button := range []*gtk.Button{app.reviewPreviewBack, copyReview} {
		button.SetRelief(gtk.RELIEF_NONE)
		context, _ := button.GetStyleContext()
		context.AddClass("giti-flat-button")
	}
	previewToolbar.PackStart(app.reviewPreviewBack, false, false, 0)
	previewToolbar.PackEnd(copyReview, false, false, 0)
	preview.PackStart(previewToolbar, false, false, 0)
	preview.PackStart(scroller(app.reviewDocument), true, true, 0)
	app.diffStack.AddNamed(preview, "review-preview")
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
	app.populateReviewMenu(menu)
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
