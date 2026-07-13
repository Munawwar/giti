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
	if len(lines) != 3 || lines[0].tag != "" || lines[1].tag != "removed" || lines[2].tag != "added" {
		t.Fatalf("unexpected styles: %#v", lines)
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
	matches := searchHistory(rows, "FIX PARSER")
	if len(matches) != 3 || matches[0].row.revision != "1111111" || matches[1].row.revision != "2222222" || matches[2].row.revision != "3333333" {
		t.Fatalf("unexpected search ranking: %#v", matches)
	}
	if matches[0].score <= matches[1].score || matches[1].score <= matches[2].score {
		t.Fatalf("scores did not distinguish phrase and word matches: %#v", matches)
	}
}

func TestHistoryLabelDescribesMergeAndEscapesContent(t *testing.T) {
	label := historyLabel(historyRow{kind: "commit", revision: "123456789", subject: "merge <side>", refs: "HEAD -> main, tag: v1<&>, tag: v2, tag: v3, tag: v4, tag: v5, origin/main", author: "A & B", parents: []string{"one", "two"}})
	for _, want := range []string{"merge &lt;side&gt;", `background="#d8f0dd"`, "HEAD → main", "origin/main", `background="#f8e7a3"`, "v1&lt;&amp;&gt;", "v2", "v3", "+ more", "A &amp; B", "merge  ·  2 parents"} {
		if !strings.Contains(label, want) {
			t.Fatalf("history label %q does not contain %q", label, want)
		}
	}
	if strings.Contains(label, "v4") || strings.Contains(label, "v5") || strings.Count(label, `background="#f8e7a3"`) != 4 || !(strings.Index(label, "v1") < strings.Index(label, "+ more") && strings.Index(label, "+ more") < strings.Index(label, "HEAD")) {
		t.Fatalf("history tags were not capped ahead of branches: %q", label)
	}
}
