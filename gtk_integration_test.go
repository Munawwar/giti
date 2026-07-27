package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"
)

func iterateGTKUntil(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !condition() {
		t.Fatal("timed out waiting for GTK state")
	}
}

func newGTKIntegrationApp(t *testing.T) (string, *giti) {
	t.Helper()
	if os.Getenv("GITI_GTK_TEST") == "" {
		t.Skip("set GITI_GTK_TEST=1 to run the display integration test")
	}
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := testRepository(t)
	if err := os.WriteFile(filepath.Join(path, "first.txt"), []byte("one"+strings.Repeat("x", 400)+"\n"+strings.Repeat("more\n", 200)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", path, "commit", "--amend", "-m", "commit 11", "-m", "Explain the orbital cache architecture.").CombinedOutput(); err != nil {
		t.Fatalf("amend commit message: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", path, "tag", strings.Repeat("long-release-name-", 12)).CombinedOutput(); err != nil {
		t.Fatalf("create long tag: %v: %s", err, output)
	}
	repo, err := newRepository(path, historySpec{Revision: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	gtk.Init(nil)
	app, err := newGiti(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		app.clearRepositoryView()
		app.window.Destroy()
	})
	iterateGTKUntil(t, 3*time.Second, func() bool { return app.historyCancel == nil && len(app.historyRows) > 0 && app.panesReady })
	return path, app
}

func TestGTKApplicationMenu(t *testing.T) {
	if os.Getenv("GITI_GTK_TEST") == "" {
		t.Skip("set GITI_GTK_TEST=1 to run the display integration test")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := testRepository(t)
	repo, err := newRepository(path, historySpec{Revision: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	gtk.Init(nil)
	application, err := gtk.ApplicationNew(applicationID+".menu-test", glib.APPLICATION_NON_UNIQUE)
	if err != nil {
		t.Fatal(err)
	}
	var app *giti
	var appErr error
	application.Connect("activate", func() {
		app, appErr = newGiti(repo, false, application)
		addMainSource(0, func() bool {
			application.Quit()
			return false
		})
	})
	application.Run([]string{"giti-menu-test"})
	if appErr != nil {
		t.Fatal(appErr)
	}
	defer func() {
		app.clearRepositoryView()
		app.window.Destroy()
	}()
	iterateGTKUntil(t, 3*time.Second, func() bool { return app.historyCancel == nil })
	refresh := application.LookupAction("refresh")
	findDiff := application.LookupAction("find-diff")
	if application.GetAppMenu() == nil || application.GetAppMenu().GetNItems() != 2 || application.GetMenubar() == nil || application.GetMenubar().GetNItems() != 1 || refresh == nil || !refresh.GetEnabled() || findDiff == nil || !findDiff.GetEnabled() {
		t.Fatal("refresh and find menu actions were not installed")
	}
	findDiff.Activate(nil)
	iterateGTKUntil(t, time.Second, func() bool { return app.diffFindBox.GetVisible() && app.diffFind.IsFocus() })
	app.closeDiffFind()
	if err = os.WriteFile(filepath.Join(path, "refresh.txt"), []byte("refresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refresh.Activate(nil)
	iterateGTKUntil(t, 3*time.Second, func() bool {
		return app.historyCancel == nil && len(app.historyRows) > 0 && app.historyRows[0].kind == "unstaged"
	})
	if app.historyRows[0].kind != "unstaged" {
		t.Fatal("refresh action did not reload working-tree changes")
	}
}

func TestGTKParentNavigationLoadsAndRevealsOlderCommit(t *testing.T) {
	if os.Getenv("GITI_GTK_TEST") == "" {
		t.Skip("set GITI_GTK_TEST=1 to run the display integration test")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveUIState(uiStatePath(), uiState{MainPanePosition: 410, RepositoryPanePosition: 300, SearchCommitMessages: true, SearchReferences: true}); err != nil {
		t.Fatal(err)
	}
	repo, err := newRepository(testRepository(t), historySpec{Revision: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	gtk.Init(nil)
	app, err := newGiti(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		app.clearRepositoryView()
		app.window.Destroy()
	}()
	iterateGTKUntil(t, 3*time.Second, func() bool { return app.historyCancel == nil && len(app.historyRows) > 0 && app.panesReady })
	app.historyLimit = 1
	app.loadHistory(false)
	iterateGTKUntil(t, 3*time.Second, func() bool { return app.historyCancel == nil && len(app.historyRows) == 1 })
	target, err := repo.run("rev-parse", "HEAD~11")
	if err != nil {
		t.Fatal(err)
	}
	target = strings.TrimSpace(target)
	app.revealHistoryRevision(target)
	iterateGTKUntil(t, 3*time.Second, func() bool {
		return app.historyCancel == nil && app.currentRow != nil && app.currentRow.revision == target
	})
	app.revealHistoryRevision(strings.Repeat("f", 40))
	iterateGTKUntil(t, 3*time.Second, func() bool { return app.historyCancel == nil && app.notification.GetVisible() })
	mainPosition, repositoryPosition := app.mainPane.GetPosition(), app.repositoryPane.GetPosition()
	if app.historyLimit < 12 || app.currentRow == nil || app.currentRow.revision != target || mainPosition != 410 || repositoryPosition != 300 || !app.searchMessages.GetActive() || !app.searchReferences.GetActive() {
		t.Fatalf("older commit was not loaded and revealed: limit=%d row=%#v scroll=%v panes=%d/%d", app.historyLimit, app.currentRow, app.historyScroller.GetVAdjustment().GetValue(), mainPosition, repositoryPosition)
	}
}

func TestGTKInitialLayoutAndHeaderControls(t *testing.T) {
	_, app := newGTKIntegrationApp(t)
	nestedRuns := 0
	var nestedSource func() bool
	nestedSource = func() bool {
		nestedRuns++
		if nestedRuns < 1000 {
			addMainSource(0, nestedSource)
		}
		return false
	}
	addMainSource(0, nestedSource)
	deadline := time.Now().Add(2 * time.Second)
	for nestedRuns < 1000 && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
	}
	if nestedRuns != 1000 {
		t.Fatalf("nested main-context scheduling stopped after %d runs", nestedRuns)
	}
	icon, iconErr := app.window.GetIcon()
	if iconErr != nil || icon == nil {
		t.Fatalf("window icon is missing: icon=%v err=%v", icon, iconErr)
	}
	splitDeadline := time.Now().Add(2 * time.Second)
	for app.repositoryPane.GetAllocatedHeight() <= 1 && time.Now().Before(splitDeadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	splitHeight, splitPosition := app.repositoryPane.GetAllocatedHeight(), app.repositoryPane.GetPosition()
	messageOption, _ := app.searchMessages.GetLabel()
	referenceOption, _ := app.searchReferences.GetLabel()
	if app.historyLimit != initialHistoryLimit || !app.mainPane.GetWideHandle() || !app.repositoryPane.GetWideHandle() || app.loadButton.GetVisible() || app.fileView.GetTooltipColumn() != 0 || splitHeight <= 1 || splitPosition*2 < splitHeight-2 || splitPosition*2 > splitHeight+2 || app.searchSettings.GetPopover() == nil || !app.searchMessages.GetVisible() || !app.searchReferences.GetVisible() || app.searchMessages.GetActive() || app.searchReferences.GetActive() {
		t.Fatalf("bad initial graph layout: limit=%d dividers=%v/%v load-more=%v tooltip=%d split=%d/%d", app.historyLimit, app.mainPane.GetWideHandle(), app.repositoryPane.GetWideHandle(), app.loadButton.GetVisible(), app.fileView.GetTooltipColumn(), splitPosition, splitHeight)
	}
	if messageOption != "Also match commit description" || referenceOption != "Also match branches and tags" {
		t.Fatalf("unexpected search options %q / %q", messageOption, referenceOption)
	}
	app.mainPane.SetPosition(app.mainPane.GetAllocatedWidth() / 3)
	app.repositoryPane.SetPosition(splitHeight * 2 / 3)
	app.persistUIState()
	savedState := loadUIState(app.statePath)
	if savedState.MainPanePosition != app.mainPane.GetPosition() || savedState.RepositoryPanePosition != app.repositoryPane.GetPosition() {
		t.Fatalf("pane positions were not persisted: %#v", savedState)
	}
	longBranch := "feature/" + strings.Repeat("copyable-reference-", 8)
	details := commitDetails{sha: strings.Repeat("a", 40), subject: "subject", branches: []string{longBranch}, additions: 12, deletions: 3, untracked: 4, statistics: true}
	app.setCommitHeader(details)
	detailChildren := app.headerDetails.GetChildren()
	compactDetails := detailChildren.Length()
	detailChildren.Free()
	referenceButton := app.headerReferenceButtons[0]
	referenceWidget, referenceErr := referenceButton.GetChild()
	referenceLabel, labelOK := referenceWidget.(*gtk.Label)
	referenceValue, textErr := "", referenceErr
	if labelOK {
		referenceValue, textErr = referenceLabel.GetText()
	}
	contentWidth, contentHeight := app.mainPane.GetAllocatedWidth(), app.mainPane.GetAllocatedHeight()
	referenceButton.Clicked()
	for gtk.EventsPending() {
		gtk.MainIteration()
	}
	copiedReference, copyReferenceErr := must(gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD)).WaitForText()
	copyNotification, notificationErr := app.notificationLabel.GetText()
	details.body = "A longer description\n\nwith multiple lines."
	app.setCommitHeader(details)
	detailChildren = app.headerDetails.GetChildren()
	expandedDetails := detailChildren.Length()
	headerChildren := app.headerTitleRow.GetChildren()
	headerTitle, titleErr := gtk.WidgetToLabel(headerChildren.NthData(0).(*gtk.Widget))
	headerMeta, metaErr := gtk.WidgetToLabel(detailChildren.NthData(0).(*gtk.Widget))
	commitChildren := app.headerCommit.GetChildren()
	shaLabel, shaErr := gtk.WidgetToLabel(commitChildren.NthData(1).(*gtk.Widget))
	statLabels := make([]string, 0, 3)
	for index := uint(1); index < headerChildren.Length(); index++ {
		label, labelErr := gtk.WidgetToLabel(headerChildren.NthData(index).(*gtk.Widget))
		if labelErr == nil {
			text, _ := label.GetText()
			statLabels = append(statLabels, text)
		}
	}
	headerChildren.Free()
	commitChildren.Free()
	detailChildren.Free()
	fullFileLabel, _ := app.fullFileToggle.GetLabel()
	whitespaceLabel, _ := app.whitespaceToggle.GetLabel()
	fullMergeLabel, _ := app.fullMergeToggle.GetLabel()
	shaTooltip, shaTooltipErr := shaLabel.GetTooltipText()
	referenceValid := labelOK && textErr == nil && !referenceLabel.GetSelectable() && referenceLabel.GetEllipsize() == pango.ELLIPSIZE_END && strings.Contains(referenceValue, longBranch) && referenceButton.GetCanFocus() && copiedReference == longBranch && copyReferenceErr == nil && app.notification.GetVisible() && copyNotification == "Copied branch to clipboard." && notificationErr == nil && app.mainPane.GetAllocatedWidth() == contentWidth && app.mainPane.GetAllocatedHeight() == contentHeight
	statisticsValid := strings.Join(statLabels, ",") == "+12,−3,4 untracked"
	controlsValid := fullFileLabel == "Show full file" && whitespaceLabel == "Whitespace changes" && app.whitespaceToggle.GetActive() && fullMergeLabel == "Full merge" && shaErr == nil && shaTooltipErr == nil && shaLabel.GetEllipsize() == pango.ELLIPSIZE_MIDDLE && shaTooltip == details.sha
	if expandedDetails != compactDetails+1 || titleErr != nil || metaErr != nil || referenceErr != nil || !referenceValid || !statisticsValid || !controlsValid || !headerTitle.GetSelectable() || !headerMeta.GetSelectable() || !shaLabel.GetSelectable() {
		t.Fatalf("commit header copy control is incomplete: compact=%d body=%d title=%v meta=%v sha=%v controls=%q/%q/%q reference=%q/%v/%v/%v copied=%q/%v notification=%q/%v content=%dx%d/%dx%d", compactDetails, expandedDetails, titleErr, metaErr, shaErr, fullFileLabel, whitespaceLabel, fullMergeLabel, referenceValue, referenceErr, labelOK, textErr, copiedReference, copyReferenceErr, copyNotification, notificationErr, contentWidth, contentHeight, app.mainPane.GetAllocatedWidth(), app.mainPane.GetAllocatedHeight())
	}
	syncedDetails := commitDetails{
		sha:       strings.Repeat("b", 40),
		subject:   "synchronized branches",
		branches:  []string{"main" + headRefSuffix, remoteRefPrefix + "origin/main"},
		upstreams: map[string]string{"main": remoteRefPrefix + "origin/main"},
	}
	app.setCommitHeader(syncedDetails)
	if len(app.headerReferenceButtons) != 2 {
		t.Fatalf("synchronized branch did not create two copy targets: %d", len(app.headerReferenceButtons))
	}
	wantLabels, wantCopies := []string{"origin/", "main ← HEAD"}, []string{"origin/main", "main"}
	for index, button := range app.headerReferenceButtons {
		child, childErr := button.GetChild()
		label, labelOK := child.(*gtk.Label)
		text := ""
		if labelOK {
			text, _ = label.GetText()
		}
		button.Clicked()
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		copied, copyErr := must(gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD)).WaitForText()
		if childErr != nil || !labelOK || strings.TrimSpace(text) != wantLabels[index] || copyErr != nil || copied != wantCopies[index] || button.GetAllocatedWidth() != label.GetAllocatedWidth() {
			t.Fatalf("synchronized branch segment %d is incomplete: label=%q child=%v type=%v copied=%q/%v widths=%d/%d", index, text, childErr, labelOK, copied, copyErr, button.GetAllocatedWidth(), label.GetAllocatedWidth())
		}
	}
}

func TestGTKGraphRenderingAndReferences(t *testing.T) {
	_, app := newGTKIntegrationApp(t)
	loaded := len(app.historyRows)
	for range 3 {
		app.loadHistory(false)
	}
	iterateGTKUntil(t, 3*time.Second, func() bool { return app.historyCancel == nil })
	if len(app.historyRows) != loaded || app.historyStore.IterNChildren(nil) != loaded {
		t.Fatalf("reloading duplicated history: rows=%d model=%d want=%d", len(app.historyRows), app.historyStore.IterNChildren(nil), loaded)
	}
	iter, ok := app.historyStore.GetIterFirst()
	if !ok {
		t.Fatal("rendered history is empty")
	}
	value, err := app.historyStore.GetValue(iter, 0)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := value.GoValue()
	pixbuf, pixbufOK := rendered.(*gdk.Pixbuf)
	if err != nil || !pixbufOK || pixbuf.GetWidth() < 48 || pixbuf.GetHeight() < graphRowHeight {
		t.Fatalf("history graph is not a rendered pixbuf: value=%T err=%v", rendered, err)
	}
	graphScroll := app.historyScroller.GetHAdjustment()
	scrollDeadline := time.Now().Add(2 * time.Second)
	for graphScroll.GetUpper() <= graphScroll.GetPageSize() && time.Now().Before(scrollDeadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	graphScroll.SetValue(graphScroll.GetUpper() - graphScroll.GetPageSize())
	if app.graphColumn.GetFixedWidth() != app.graphWidth || graphScroll.GetUpper() <= graphScroll.GetPageSize() || graphScroll.GetValue() == 0 {
		t.Fatalf("graph is not horizontally scrollable: column=%d/%d range=%v/%v value=%v", app.graphColumn.GetFixedWidth(), app.graphWidth, graphScroll.GetUpper(), graphScroll.GetPageSize(), graphScroll.GetValue())
	}
	graphScroll.SetValue(0)
	app.showReferences([]string{"main", "origin/main", "feature"}, []string{"v1", "v2", "v3"})
	referenceBuffer := must(app.referencesView.GetBuffer())
	referenceStart, referenceEnd := referenceBuffer.GetBounds()
	referenceText, _ := referenceBuffer.GetText(referenceStart, referenceEnd, true)
	referenceBuffer.SelectRange(referenceStart, referenceEnd)
	referenceClipboard := must(gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD))
	referenceBuffer.CopyClipboard(referenceClipboard)
	copiedReferences, copyErr := referenceClipboard.WaitForText()
	if app.diffStack.GetVisibleChildName() != "references" || !strings.Contains(referenceText, "origin/main") || !strings.Contains(referenceText, "v3") || copiedReferences != referenceText || copyErr != nil {
		t.Fatalf("references page was not shown as continuous copyable text: page=%q text=%q copied=%q err=%v", app.diffStack.GetVisibleChildName(), referenceText, copiedReferences, copyErr)
	}
	app.showDiffPage()
	if app.diffStack.GetVisibleChildName() != "diff" {
		t.Fatalf("diff page did not restore after references page")
	}
	for index, row := range app.historyRows {
		if row.refs == "" {
			continue
		}
		iter, err = app.historyStore.GetIter(must(gtk.TreePathNewFromIndicesv([]int{index})))
		if err == nil {
			value, err = app.historyStore.GetValue(iter, 0)
		}
		if err == nil {
			rendered, err = value.GoValue()
			pixbuf, pixbufOK = rendered.(*gdk.Pixbuf)
		}
		cellHeight := app.historyView.GetCellArea(must(gtk.TreePathNewFromIndicesv([]int{index})), app.graphColumn).GetHeight()
		if err != nil || !pixbufOK || pixbuf.GetHeight() != cellHeight {
			height := 0
			if pixbufOK {
				height = pixbuf.GetHeight()
			}
			t.Fatalf("ref row graph does not fill its row: value=%T height=%d cell=%d err=%v", rendered, height, cellHeight, err)
		}
		break
	}
	theme, themeErr := must(gtk.SettingsGetDefault()).GetProperty("gtk-theme-name")
	themeName, isThemeName := theme.(string)
	if themeErr != nil || !isThemeName || strings.HasSuffix(themeName, "-dark") {
		t.Fatalf("Giti did not force a light GTK theme: theme=%q err=%v", theme, themeErr)
	}
}

func TestGTKDiffFindShortcutNavigationAndFileSwitch(t *testing.T) {
	_, app := newGTKIntegrationApp(t)
	iterateGTKUntil(t, 3*time.Second, func() bool {
		return app.diffLoaded && app.currentFile != nil && app.currentFile.path == "first.txt"
	})
	app.window.ShowAll()
	if app.diffFindBox.GetVisible() {
		t.Fatal("repository-opening ShowAll revealed the diff search")
	}
	app.diffView.GrabFocus()
	if !app.handleDiffFindKey(gdk.KEY_f, gdk.CONTROL_MASK) {
		t.Fatal("Ctrl+F was not handled in the diff")
	}
	iterateGTKUntil(t, time.Second, func() bool { return app.diffFindBox.GetVisible() && app.diffFind.IsFocus() })
	app.diffFind.SetText("MORE")
	iterateGTKUntil(t, 2*time.Second, func() bool { return len(app.diffFindMatches) > 1 })
	count, _ := app.diffFindCount.GetText()
	if app.diffFindIndex != 0 || count != fmt.Sprintf("1 / %d", len(app.diffFindMatches)) {
		t.Fatalf("initial diff match was not selected: index=%d count=%q", app.diffFindIndex, count)
	}
	currentTag := must(must(app.diffBuffer.GetTagTable()).Lookup(diffFindCurrentTag))
	if !app.diffBuffer.GetIterAtOffset(app.diffFindMatches[0].start).HasTag(currentTag) {
		t.Fatal("current diff match was not highlighted")
	}
	app.diffFindNext.Clicked()
	if app.diffFindIndex != 1 {
		t.Fatalf("next diff match selected index %d", app.diffFindIndex)
	}
	if !app.handleDiffFindKey(gdk.KEY_Return, gdk.SHIFT_MASK) || app.diffFindIndex != 0 {
		t.Fatalf("Shift+Enter did not select the previous diff match: index=%d", app.diffFindIndex)
	}
	app.diffFindPrevious.Clicked()
	if app.diffFindIndex != len(app.diffFindMatches)-1 {
		t.Fatalf("previous diff match did not wrap: index=%d", app.diffFindIndex)
	}
	app.diffFindNext.Clicked()

	app.diffFind.SetText("second")
	iterateGTKUntil(t, time.Second, func() bool {
		count, _ = app.diffFindCount.GetText()
		return count == "0 / 0"
	})
	if app.diffFindNext.GetSensitive() || app.diffFindPrevious.GetSensitive() {
		t.Fatal("navigation remained enabled without diff matches")
	}
	selection, _ := app.fileView.GetSelection()
	selection.SelectPath(must(gtk.TreePathNewFromIndicesv([]int{1})))
	iterateGTKUntil(t, 3*time.Second, func() bool {
		return app.diffLoaded && app.currentFile != nil && app.currentFile.path == "second.txt" && len(app.diffFindMatches) == 1
	})
	query, _ := app.diffFind.GetText()
	if !app.diffFindBox.GetVisible() || query != "second" || app.diffFindIndex != 0 {
		t.Fatalf("file switch did not rerun the open diff search: visible=%v query=%q index=%d", app.diffFindBox.GetVisible(), query, app.diffFindIndex)
	}
	if !app.handleDiffFindKey(gdk.KEY_Escape, 0) {
		t.Fatal("Escape was not handled by the diff search")
	}
	iterateGTKUntil(t, time.Second, func() bool { return !app.diffFindBox.GetVisible() })
	if len(app.diffFindMatches) != 0 {
		t.Fatalf("closing diff search retained %d matches", len(app.diffFindMatches))
	}
}

func TestGTKDiffInteraction(t *testing.T) {
	_, app := newGTKIntegrationApp(t)
	deadline := time.Now().Add(2 * time.Second)
	for (!app.window.IsMaximized() || app.currentFile == nil || app.diffBuffer.GetCharCount() == 0) && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !app.window.IsMaximized() || app.currentRow == nil || app.currentRow.kind != "unstaged" {
		t.Fatalf("bad initial selection: maximized=%v row=%#v", app.window.IsMaximized(), app.currentRow)
	}
	if app.currentFile == nil || app.currentFile.path != "first.txt" || len(app.files) != 2 {
		t.Fatalf("bad initial file selection: %#v from %#v", app.currentFile, app.files)
	}
	summary, summaryErr := app.fileSummary.GetText()
	iter, hasFile := app.fileStore.GetIterFirst()
	statMarkup := ""
	if hasFile {
		if value, valueErr := app.fileStore.GetValue(iter, 1); valueErr == nil {
			statMarkup, _ = value.GetString()
		}
	}
	if summaryErr != nil || summary != "2 files · 0 added · 0 deleted · 0 updated · 2 untracked" || !strings.Contains(statMarkup, "Untracked") {
		t.Fatalf("untracked file summary is wrong: summary=%q err=%v stat=%q", summary, summaryErr, statMarkup)
	}
	start, end := app.diffBuffer.GetBounds()
	compact, _ := app.diffBuffer.GetText(start, end, true)
	hunkTag := must(must(app.diffBuffer.GetTagTable()).Lookup("hunk"))
	hunkOffset := strings.Index(compact, "@@")
	if !strings.Contains(compact, "+one") || strings.Contains(compact, "second") || strings.Contains(compact, "diff --git") || hunkOffset < 0 || !app.diffBuffer.GetIterAtOffset(hunkOffset).HasTag(hunkTag) || app.diffGutter.GetVisible() {
		t.Fatalf("single-file rendered diff is wrong: %q", compact)
	}
	app.diffBuffer.SelectRange(start, end)
	clipboard := must(gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD))
	app.diffBuffer.CopyClipboard(clipboard)
	copied, err := clipboard.WaitForText()
	if err != nil || copied != compact {
		t.Fatalf("diff selection was not copyable: %q: %v", copied, err)
	}
	app.fullFileToggle.SetActive(true)
	iterateGTKUntil(t, 2*time.Second, func() bool {
		return app.diffLoaded && app.diffGutter.GetVisible() && app.diffGutter.GetAllocatedWidth() > 1 && app.diffOverviewReveal.GetRevealChild() && app.diffOverview.GetAllocatedWidth() >= 24 && app.diffOverview.GetAllocatedHeight() > 1
	})
	fullStart, fullEnd := app.diffBuffer.GetBounds()
	fullText, _ := app.diffBuffer.GetText(fullStart, fullEnd, true)
	fullHunkOffset := strings.Index(fullText, "@@")
	if !app.fullFileToggle.GetActive() || len(app.diffLineNumbers) != app.overviewLines || !app.diffOverviewReveal.GetRevealChild() || app.diffOverview.GetAllocatedWidth() < 24 || app.diffOverview.GetAllocatedHeight() <= 1 || len(app.overviewMarkers) == 0 || fullHunkOffset < 0 || app.diffBuffer.GetIterAtOffset(fullHunkOffset).HasTag(hunkTag) {
		t.Fatalf("full-file navigation was not shown: full=%v gutter=%d lines=%d overview=%dx%d markers=%d", app.fullFileToggle.GetActive(), len(app.diffLineNumbers), app.overviewLines, app.diffOverview.GetAllocatedWidth(), app.diffOverview.GetAllocatedHeight(), len(app.overviewMarkers))
	}
	numberedAddition := false
	for _, numbers := range app.diffLineNumbers {
		numberedAddition = numberedAddition || numbers.kind == diffLineAdded && numbers.old == 0 && numbers.new > 0
	}
	if !numberedAddition {
		t.Fatalf("full-file gutter did not number an added line: %#v", app.diffLineNumbers)
	}
	for gtk.EventsPending() {
		gtk.MainIteration()
	}
	fullFileScroll := app.diffScroller.GetVAdjustment()
	fullFileScroll.SetValue(0)
	app.scrollDiffOverview(float64(app.diffOverview.GetAllocatedHeight()))
	iterateGTKUntil(t, time.Second, func() bool { return fullFileScroll.GetValue() > 0 })
	if app.overviewLines < 100 || fullFileScroll.GetValue() == 0 {
		t.Fatalf("full-file overview did not expose or navigate its changes: lines=%d markers=%#v scroll=%v", app.overviewLines, app.overviewMarkers, fullFileScroll.GetValue())
	}
	app.fileView.GrabFocus()
	app.fileView.SetCursor(must(gtk.TreePathNewFromIndicesv([]int{1})), nil, false)
	deadline = time.Now().Add(2 * time.Second)
	for (app.currentFile == nil || app.currentFile.path != "second.txt" || app.diffBuffer.GetCharCount() == 0) && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	start, end = app.diffBuffer.GetBounds()
	second, _ := app.diffBuffer.GetText(start, end, true)
	if !app.fileView.IsFocus() || !app.fullFileToggle.GetActive() || !app.fullFilePreferred || !app.diffGutter.GetVisible() || !app.diffOverviewReveal.GetRevealChild() || app.currentFile.path != "second.txt" || !strings.Contains(second, "+second") || strings.Contains(second, "+one") {
		t.Fatalf("file switch retained state or content: full=%v file=%#v diff=%q", app.fullFileToggle.GetActive(), app.currentFile, second)
	}
	app.fileView.SetCursor(must(gtk.TreePathNewFromIndicesv([]int{0})), nil, false)
	deadline = time.Now().Add(2 * time.Second)
	for (app.currentFile == nil || app.currentFile.path != "first.txt" || app.diffBuffer.GetCharCount() == 0) && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	vertical, horizontal := app.diffScroller.GetVAdjustment(), app.diffScroller.GetHAdjustment()
	vertical.SetValue(180)
	horizontal.SetValue(120)
	wanted := scrollPosition{horizontal.GetValue(), vertical.GetValue()}
	if wanted.horizontal == 0 || wanted.vertical == 0 {
		t.Fatalf("fixture did not create a two-axis scrollable diff: %#v", wanted)
	}
	switchStarted := time.Now()
	for _, index := range []int{1, 0, 1, 0, 1, 0} {
		app.fileView.SetCursor(must(gtk.TreePathNewFromIndicesv([]int{index})), nil, false)
	}
	if elapsed := time.Since(switchStarted); elapsed > 100*time.Millisecond {
		t.Fatalf("file selection blocked GTK for %v", elapsed)
	}
	deadline = time.Now().Add(2 * time.Second)
	for (app.currentFile == nil || app.currentFile.path != "first.txt" || horizontal.GetValue() < wanted.horizontal-1 || vertical.GetValue() < wanted.vertical-1) && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	restored := scrollPosition{horizontal.GetValue(), vertical.GetValue()}
	if app.currentFile == nil || app.currentFile.path != "first.txt" || restored.horizontal < wanted.horizontal-1 || restored.horizontal > wanted.horizontal+1 || restored.vertical < wanted.vertical-1 || restored.vertical > wanted.vertical+1 {
		t.Fatalf("file scroll was not restored: got=%#v want=%#v", restored, wanted)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	wrapper := "#!/bin/sh\ncase \" $* \" in *\" diff --name-status \"*) sleep .3;; esac\nexec " + realGit + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(binDir, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	app.historyView.GrabFocus()
	selectionStarted := time.Now()
	for _, index := range []int{1, 2, 3} {
		app.historyView.SetCursor(must(gtk.TreePathNewFromIndicesv([]int{index})), nil, false)
	}
	if elapsed := time.Since(selectionStarted); elapsed > 100*time.Millisecond {
		t.Fatalf("graph selection blocked GTK for %v", elapsed)
	}
	deadline = time.Now().Add(2 * time.Second)
	for (app.currentFile == nil || app.diffBuffer.GetCharCount() == 0) && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !app.historyView.IsFocus() || app.currentRow == nil || app.currentRow.subject != "commit 9" || app.currentFile == nil {
		t.Fatalf("rapid graph selection applied stale state: row=%#v file=%#v", app.currentRow, app.currentFile)
	}
	for _, action := range fileCopyActions(app.repository.path, app.currentFile.path) {
		app.copyToClipboard(action.text, action.message)
		copiedPath, copyErr := must(gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD)).WaitForText()
		message, messageErr := app.notificationLabel.GetText()
		if copyErr != nil || messageErr != nil || copiedPath != action.text || message != action.message {
			t.Fatalf("file copy action %q copied %q/%v and notified %q/%v", action.label, copiedPath, copyErr, message, messageErr)
		}
	}
}

func TestGTKSearchOptionsAndSelection(t *testing.T) {
	_, app := newGTKIntegrationApp(t)
	previousSearch, previousCancel := context.WithCancel(context.Background())
	app.searchCancel = previousCancel
	previousGeneration := app.searchGeneration
	app.historySearch.SetText("superseded query")
	if previousSearch.Err() != context.Canceled || app.searchGeneration <= previousGeneration {
		t.Fatal("changing the query did not cancel the previous search")
	}
	app.historySearch.SetText("")
	time.Sleep(200 * time.Millisecond)
	for gtk.EventsPending() {
		gtk.MainIteration()
	}
	if app.searchCancel != nil || len(app.searchMatches) != 0 || app.historyStack.GetVisibleChildName() != "graph" || app.historySearch.GetProgressFraction() != 0 {
		t.Fatal("clearing a replacement query allowed stale search work to continue")
	}
	app.historySearch.SetText("orbital cache")
	if len(app.searchMatches) != 0 {
		t.Fatalf("default search included a long commit message: %#v", app.searchMatches)
	}
	app.searchMessages.SetActive(true)
	iterateGTKUntil(t, 3*time.Second, func() bool { return app.searchCancel == nil && len(app.searchMatches) == 1 })
	if len(app.searchMatches) != 1 || app.searchMatches[0].subject != "commit 11" {
		t.Fatalf("enabled long-message search missed its commit: %#v", app.searchMatches)
	}
	app.historySearch.SetText("long-release-name")
	if len(app.searchMatches) != 0 {
		t.Fatalf("default reference search included a tag: %#v", app.searchMatches)
	}
	app.searchReferences.SetActive(true)
	iterateGTKUntil(t, time.Second, func() bool { return app.searchCancel == nil && len(app.searchMatches) == 1 })
	if len(app.searchMatches) != 1 || app.searchMatches[0].subject != "commit 11" {
		t.Fatalf("enabled reference search missed its tag: %#v", app.searchMatches)
	}
	app.searchSettings.SetActive(true)
	for gtk.EventsPending() {
		gtk.MainIteration()
	}
	if !app.searchMessages.GetMapped() || !app.searchReferences.GetMapped() {
		t.Fatal("search options were not mapped when their popover opened")
	}
	app.searchSettings.SetActive(false)
	if state := loadUIState(app.statePath); !state.SearchCommitMessages || !state.SearchReferences {
		t.Fatalf("search settings were not persisted: %#v", state)
	}
	app.historySearch.SetText("")
	app.historyLimit = 1
	app.loadHistory(false)
	iterateGTKUntil(t, 3*time.Second, func() bool { return app.historyCancel == nil })
	app.historySearch.SetText("CoMmIt 8")
	iterateGTKUntil(t, time.Second, func() bool { return app.searchCancel == nil && len(app.searchMatches) > 0 })
	if app.historyStack.GetVisibleChildName() != "search" || len(app.searchMatches) == 0 || app.searchMatches[0].subject != "commit 8" || app.historyLimit != 1 || app.historySearch.GetProgressFraction() != 0 {
		t.Fatalf("background search depended on loaded graph rows: stack=%q limit=%d matches=%#v", app.historyStack.GetVisibleChildName(), app.historyLimit, app.searchMatches)
	}
	app.openSearchResult(0)
	deadline := time.Now().Add(2 * time.Second)
	for (app.currentRow == nil || app.currentRow.subject != "commit 8") && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	query, _ := app.historySearch.GetText()
	if query != "CoMmIt 8" || app.historyStack.GetVisibleChildName() != "graph" || !app.searchBack.GetVisible() || app.currentRow == nil || app.currentRow.subject != "commit 8" || !app.historyView.IsFocus() || app.historyLimit == 1 {
		t.Fatalf("search result did not preserve the search while revealing its graph row: query=%q stack=%q row=%#v", query, app.historyStack.GetVisibleChildName(), app.currentRow)
	}
	app.searchBack.Clicked()
	if app.historyStack.GetVisibleChildName() != "search" || app.searchBack.GetVisible() || len(app.searchMatches) == 0 || !app.searchResults.GetRowAtIndex(0).IsFocus() {
		t.Fatalf("search results could not be restored: stack=%q matches=%d", app.historyStack.GetVisibleChildName(), len(app.searchMatches))
	}

	graphRows := len(app.historyRows)
	app.searchFileMode.SetActive(true)
	app.historySearch.SetText("history.txt")
	iterateGTKUntil(t, 3*time.Second, func() bool { return app.searchCancel == nil && len(app.searchMatches) == 12 })
	emptyMessage, _ := app.searchPlaceholder.GetText()
	if !app.searchFileMode.GetActive() || app.searchTextOptions.GetVisible() || !app.searchFileOptions.GetVisible() || len(app.historyRows) != graphRows || emptyMessage != "No commits touch this path." {
		t.Fatalf("file mode changed the regular graph or its options: rows=%d matches=%d", len(app.historyRows), len(app.searchMatches))
	}
	searchGeneration := app.searchGeneration
	app.loadHistory(false)
	iterateGTKUntil(t, 3*time.Second, func() bool { return app.historyCancel == nil })
	if app.searchGeneration != searchGeneration {
		t.Fatal("internal graph reload unnecessarily reran file search")
	}
	app.loadHistory(true)
	iterateGTKUntil(t, 3*time.Second, func() bool {
		return app.historyCancel == nil && app.searchCancel == nil && app.searchGeneration > searchGeneration
	})
	app.searchLimit = 1
	app.updateGraphSearch()
	iterateGTKUntil(t, 3*time.Second, func() bool {
		return app.searchCancel == nil && len(app.searchMatches) == 1 && app.searchLoadButton.GetVisible()
	})
	app.searchLoadButton.GrabFocus()
	app.searchLoadButton.Clicked()
	iterateGTKUntil(t, 3*time.Second, func() bool {
		return app.searchCancel == nil && len(app.searchMatches) == 12 && !app.searchLoadButton.GetVisible() && app.searchResults.GetRowAtIndex(0).IsFocus()
	})
	app.openSearchResult(7)
	iterateGTKUntil(t, 3*time.Second, func() bool { return app.currentRow != nil && app.currentRow.revision == app.searchMatches[7].revision })
	query, _ = app.historySearch.GetText()
	if query != "history.txt" || app.historyStack.GetVisibleChildName() != "graph" || !app.searchBack.GetVisible() {
		t.Fatalf("file result did not reveal the regular graph reversibly: query=%q stack=%q", query, app.historyStack.GetVisibleChildName())
	}
	app.historySearch.SetText("../outside")
	iterateGTKUntil(t, 3*time.Second, func() bool {
		message, _ := app.searchPlaceholder.GetText()
		return app.searchCancel == nil && strings.HasPrefix(message, "Could not search:")
	})
	if app.historyStack.GetVisibleChildName() != "search" || len(app.searchMatches) != 0 {
		t.Fatalf("file search error was not shown inline: stack=%q matches=%d", app.historyStack.GetVisibleChildName(), len(app.searchMatches))
	}
}

func TestGTKResidentLifecycle(t *testing.T) {
	path, app := newGTKIntegrationApp(t)
	iterateGTKUntil(t, 2*time.Second, func() bool {
		return app.currentRow != nil && app.currentFile != nil && app.diffBuffer.GetCharCount() > 0
	})
	app.historySearch.SetText("commit")
	iterateGTKUntil(t, time.Second, func() bool { return len(app.searchMatches) == 12 })
	app.resident = true
	app.window.Close()
	for gtk.EventsPending() {
		gtk.MainIteration()
	}
	start, end := app.diffBuffer.GetBounds()
	cleared, _ := app.diffBuffer.GetText(start, end, true)
	retainedResults := false
	if children := app.searchResults.GetChildren(); children != nil {
		children.Foreach(func(any) { retainedResults = true })
		children.Free()
	}
	if app.window.GetVisible() || app.currentRow != nil || app.currentFile != nil || app.historyRows != nil || app.files != nil || retainedResults || app.fullFilePreferred || app.fullFileToggle.GetActive() || cleared != "" {
		t.Fatalf("hidden view retained repository data: row=%#v file=%#v rows=%d files=%d diff=%q", app.currentRow, app.currentFile, len(app.historyRows), len(app.files), cleared)
	}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	app.server = newResidentServer(app)
	if err := app.server.start(); err != nil {
		t.Fatal(err)
	}
	defer app.server.stop()
	duplicate := newResidentServer(app)
	if err := duplicate.start(); err == nil {
		duplicate.stop()
		t.Fatal("second resident acquired the process lock")
	}
	connection, err := net.Dial("unix", app.server.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	json.NewEncoder(connection).Encode(openRequest{Path: path, History: historySpec{Revision: "HEAD", Path: "history.txt", Follow: true}})
	response := make([]byte, 3)
	connection.Read(response)
	connection.Close()
	reopenDeadline := time.Now().Add(2 * time.Second)
	for (app.currentRow == nil || app.searchCancel != nil || len(app.searchMatches) != 12) && time.Now().Before(reopenDeadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	query, _ := app.historySearch.GetText()
	if string(response) != "OK\n" || app.currentRow == nil || !app.busy || query != "history.txt" || !app.searchFileMode.GetActive() || !app.searchFollow.GetActive() || len(app.searchMatches) != 12 {
		t.Fatalf("warm file-search reopen failed: response=%q row=%#v query=%q matches=%d busy=%v", response, app.currentRow, query, len(app.searchMatches), app.busy)
	}
}

func TestGTKGraphTextScaling(t *testing.T) {
	if os.Getenv("GITI_GTK_SCALE_TEST") == "" {
		t.Skip("set GITI_GTK_SCALE_TEST=1 and increase GDK_DPI_SCALE")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gtk.Init(nil)
	repo, err := newRepository(testRepository(t), historySpec{Revision: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	app, err := newGiti(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	defer app.window.Destroy()
	iterateGTKUntil(t, 3*time.Second, func() bool { return app.historyCancel == nil && len(app.historyRows) > 0 })
	deadline := time.Now().Add(2 * time.Second)
	path := must(gtk.TreePathNewFromIndicesv([]int{0}))
	for app.historyView.GetCellArea(path, app.historyView.GetColumn(0)).GetHeight() <= graphRowHeight && time.Now().Before(deadline) {
		for gtk.EventsPending() {
			gtk.MainIteration()
		}
		time.Sleep(10 * time.Millisecond)
	}
	height := app.historyView.GetCellArea(path, app.historyView.GetColumn(0)).GetHeight()
	iter, err := app.historyStore.GetIter(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := app.historyStore.GetValue(iter, 0)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := value.GoValue()
	pixbuf, ok := rendered.(*gdk.Pixbuf)
	pixbufHeight := 0
	if ok {
		pixbufHeight = pixbuf.GetHeight()
	}
	if err != nil || !ok || height <= graphRowHeight || pixbufHeight != height {
		t.Fatalf("scaled graph does not fill its GTK row: cell=%d pixbuf=%T/%d err=%v", height, rendered, pixbufHeight, err)
	}
}
