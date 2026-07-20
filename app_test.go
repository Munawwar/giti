package main

import (
	"os"
	"strings"
	"testing"
)

func TestDesktopEntryMatchesApplicationID(t *testing.T) {
	entry, err := os.ReadFile("data/" + applicationID + ".desktop")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Icon=" + applicationID, "StartupWMClass=" + applicationID} {
		if !strings.Contains(string(entry), want) {
			t.Fatalf("desktop entry missing %q", want)
		}
	}
}

func TestDisplayLinesHideRedundantHeaders(t *testing.T) {
	patch := "diff --git a/old.go b/new.go\nindex 123..456 100644\n--- a/old.go\n+++ b/new.go\n@@ -1 +1 @@\n-old\n+new\n"
	lines := displayLines(patch)
	var rendered strings.Builder
	for _, line := range lines {
		rendered.WriteString(line.text)
	}
	if strings.Contains(rendered.String(), "diff --git") || strings.Contains(rendered.String(), "index ") || strings.Contains(rendered.String(), "--- ") {
		t.Fatalf("redundant header retained: %s", rendered.String())
	}
	if len(lines) != 3 || lines[0].tag != "hunk" || lines[1].tag != "removed" || lines[1].old != 1 || lines[1].new != 0 || lines[2].tag != "added" || lines[2].old != 0 || lines[2].new != 1 {
		t.Fatalf("unexpected styles: %#v", lines)
	}
}

func TestDisplayLinesTrackOldAndNewNumbersAcrossDeletion(t *testing.T) {
	lines := displayLines("@@ -10,3 +20,2 @@\n context before\n-deleted\n context after\n")
	want := [][2]int{{0, 0}, {10, 20}, {11, 0}, {12, 21}}
	if len(lines) != len(want) {
		t.Fatalf("numbered lines = %#v", lines)
	}
	for index, numbers := range want {
		if lines[index].old != numbers[0] || lines[index].new != numbers[1] {
			t.Fatalf("line %d numbers = %d/%d, want %d/%d", index, lines[index].old, lines[index].new, numbers[0], numbers[1])
		}
	}
}

func TestDisplayLinesRetainRenameMetadata(t *testing.T) {
	patch := "diff --git a/old.go b/new.go\nsimilarity index 97%\nrename from old.go\nrename to new.go\n"
	lines := displayLines(patch)
	if len(lines) != 3 || !strings.Contains(lines[0].text, "97%") || !strings.Contains(lines[2].text, "rename to") {
		t.Fatalf("rename metadata missing: %#v", lines)
	}
}

func TestSearchHistoryRanksExactPhrasesAboveSeparateWords(t *testing.T) {
	rows := []historyRow{
		{kind: "commit", revision: "1111111", subject: "Fix parser crash"},
		{kind: "commit", revision: "2222222", subject: "Fix package parser config"},
		{kind: "commit", revision: "3333333", subject: "Parser cleanup"},
		{kind: "connector", subject: "ignored"},
	}
	matches := searchHistory(rows, "FIX PARSER", searchOptions{})
	if len(matches) != 3 || matches[0].row.revision != "1111111" || matches[1].row.revision != "2222222" || matches[2].row.revision != "3333333" {
		t.Fatalf("unexpected search ranking: %#v", matches)
	}
	if matches[0].score <= matches[1].score || matches[1].score <= matches[2].score {
		t.Fatalf("scores did not distinguish phrase and word matches: %#v", matches)
	}
}

func TestSearchHistoryMatchesFiveDigitHexSHAOnly(t *testing.T) {
	rows := []historyRow{
		{kind: "commit", revision: "e8c38dd123456789012345678901234567890123", subject: "Refactor"},
		{kind: "commit", revision: "edda1991234567890123456789012345678901234", subject: "Another"},
	}
	matches := searchHistory(rows, "e8c38", searchOptions{})
	if len(matches) != 1 || matches[0].row.revision != rows[0].revision || !matches[0].matchesSHA || !strings.Contains(searchResultMarkup(matches[0]), "matches commit SHA") {
		t.Fatalf("valid SHA prefix was not matched: %#v", matches)
	}
	for _, query := range []string{"e8c3", "edda1 1", "e8c3g"} {
		if isSHAQuery(query) {
			t.Fatalf("invalid SHA query was accepted: %q", query)
		}
	}
	if matches := searchHistory(rows, "e8c3", searchOptions{}); len(matches) != 0 {
		t.Fatalf("four-digit SHA prefix unexpectedly matched: %#v", matches)
	}
}

func TestSearchHistoryOptionsIncludeMessagesAndReferences(t *testing.T) {
	rows := []historyRow{
		{kind: "commit", revision: "1111111", subject: "Refactor internals", body: "Document the orbital cache architecture."},
		{kind: "commit", revision: "2222222", subject: "Release", refs: "main, tag: nebula-v2"},
	}
	if matches := searchHistory(rows, "orbital cache", searchOptions{}); len(matches) != 0 {
		t.Fatalf("default search unexpectedly included the long message: %#v", matches)
	}
	if matches := searchHistory(rows, "orbital cache", searchOptions{messages: true}); len(matches) != 1 || matches[0].row.revision != "1111111" {
		t.Fatalf("long message search missed its commit: %#v", matches)
	} else if markup := searchResultMarkup(matches[0]); !strings.Contains(markup, "matches commit description") {
		t.Fatalf("description match lacks its hint: %q", markup)
	}
	if matches := searchHistory(rows, "nebula-v2", searchOptions{}); len(matches) != 0 {
		t.Fatalf("default search unexpectedly included references: %#v", matches)
	}
	if matches := searchHistory(rows, "nebula-v2", searchOptions{references: true}); len(matches) != 1 || matches[0].row.revision != "2222222" || len(matches[0].tags) != 1 {
		t.Fatalf("reference search missed its commit: %#v", matches)
	} else if markup := searchResultMarkup(matches[0]); !strings.Contains(markup, "background=") || !strings.Contains(markup, "nebula-v2") {
		t.Fatalf("reference match lacks its tag badge: %q", markup)
	}
}

func TestSearchHistoryUsesNewestDateToBreakScoreTies(t *testing.T) {
	rows := []historyRow{
		{kind: "commit", revision: "1111111", subject: "Same exact text", timestamp: 100},
		{kind: "commit", revision: "2222222", subject: "Same exact text", timestamp: 300},
		{kind: "commit", revision: "3333333", subject: "Same exact text", timestamp: 200},
	}
	matches := searchHistory(rows, "same exact text", searchOptions{})
	if len(matches) != 3 || matches[0].row.revision != "2222222" || matches[1].row.revision != "3333333" || matches[2].row.revision != "1111111" {
		t.Fatalf("equal search scores were not ordered newest first: %#v", matches)
	}
}

func TestHistoryLabelDescribesMergeAndEscapesContent(t *testing.T) {
	row := historyRow{kind: "commit", revision: "123456789", subject: "merge <side>", refs: "HEAD -> refs/heads/main, refs/remotes/origin/main, tag: refs/tags/v1<&>, tag: refs/tags/v2, tag: refs/tags/v3, tag: refs/tags/v4, tag: refs/tags/v5", author: "A & B", parents: []string{"one", "two"}, upstreams: map[string]string{"main": remoteRefPrefix + "origin/main"}}
	label := historyLabel(row)
	for _, want := range []string{"merge &lt;side&gt;", "A &amp; B", "merge · 2 parents"} {
		if !strings.Contains(label, want) {
			t.Fatalf("history label %q does not contain %q", label, want)
		}
	}
	refs := label
	if strings.HasPrefix(label, "  ") {
		t.Fatalf("history references have unwanted leading margin: %q", label)
	}
	for _, want := range []string{"main ← HEAD", "origin/", "5 tags", `</span><span`} {
		if !strings.Contains(refs, want) {
			t.Fatalf("history references %q does not contain %q", refs, want)
		}
	}
	if strings.Count(refs, "background=") < 3 {
		t.Fatalf("history references lost their distinct badge backgrounds: %q", refs)
	}
	if strings.Contains(refs, "v1") || strings.Contains(refs, "v2") || strings.Contains(refs, "v3") || strings.Contains(refs, "v4") || strings.Contains(refs, "v5") || strings.Index(refs, "5 tags") > strings.Index(refs, "main ← HEAD") || strings.Index(refs, "main ← HEAD") > strings.Index(refs, "merge &lt;side&gt;") {
		t.Fatalf("history references were not summarized and ordered: %q", refs)
	}
	row.upstreams = nil
	row.refs = "main, refs/remotes/origin/main, feature, release, zeta, tag: v1, tag: v2, tag: v3"
	refs = historyLabel(row)
	if !strings.Contains(refs, "3 tags") || !strings.Contains(refs, "+1 more branches") || !(strings.Index(refs, "3 tags") < strings.Index(refs, "feature") && strings.Index(refs, "feature") < strings.Index(refs, "main") && strings.Index(refs, "main") < strings.Index(refs, "origin/main") && strings.Index(refs, "origin/main") < strings.Index(refs, "+1 more branches") && strings.Index(refs, "+1 more branches") < strings.Index(refs, "merge")) {
		t.Fatalf("overflow decorations were not ordered: %q", refs)
	}
	row.refs = "main, tag: v1"
	refs = historyLabel(row)
	if !strings.Contains(refs, "v1") || strings.Contains(refs, "1 tags") || strings.Index(refs, "v1") > strings.Index(refs, "merge") {
		t.Fatalf("single tags were not shown by name: %q", refs)
	}
}

func TestBranchReferencePartsCollapseUpstreamAndGroupMatchingRemotes(t *testing.T) {
	branches := []string{
		"main" + headRefSuffix,
		"feat1",
		"feat2",
		remoteRefPrefix + "origin/main",
		remoteRefPrefix + "upstream/main",
		remoteRefPrefix + "origin/feat1",
		remoteRefPrefix + "upstream/release",
		remoteRefPrefix + "origin/feat3",
	}
	parts := branchReferenceParts(branches, map[string]string{
		"main":  remoteRefPrefix + "origin/main",
		"feat2": remoteRefPrefix + "upstream/release",
	})
	if len(parts) != 5 || !parts[0].synced || strings.Join(parts[0].branches, ",") != remoteRefPrefix+"origin/main,main"+headRefSuffix || !strings.Contains(parts[0].markup, `> origin/</span><span`) {
		t.Fatalf("configured upstream was not compacted: %#v", parts)
	}
	if strings.Join(parts[1].branches, ",") != remoteRefPrefix+"upstream/main" || strings.Join(parts[2].branches, ",") != "feat1" || strings.Join(parts[3].branches, ",") != remoteRefPrefix+"origin/feat1" {
		t.Fatalf("same-name remotes were not adjacent to locals: %#v", parts)
	}
	if !parts[4].overflow || parts[4].label != "+3 more branches" || strings.Contains(parts[4].markup, "background=") {
		t.Fatalf("branch overflow was not combined and neutral: %#v", parts[4])
	}
}

func TestBranchReferenceOverflowCountsHiddenCollapsedRefs(t *testing.T) {
	branches := []string{"a", "b", "c", "d", "z", remoteRefPrefix + "origin/z"}
	parts := branchReferenceParts(branches, map[string]string{"z": remoteRefPrefix + "origin/z"})
	if len(parts) != 5 || parts[4].label != "+2 more branches" {
		t.Fatalf("hidden synchronized refs were not both counted: %#v", parts)
	}
}
