package main

import (
	"strings"
	"testing"
)

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

func TestArguments(t *testing.T) {
	resident, path, revision, err := arguments([]string{"gitskim", "--resident", "/repo", "main"})
	if err != nil || !resident || path != "/repo" || revision != "main" {
		t.Fatalf("unexpected internal arguments: %v %q %q %v", resident, path, revision, err)
	}
	resident, path, revision, err = arguments([]string{"gitskim", "v1"})
	if err != nil || resident || path != "." || revision != "v1" {
		t.Fatalf("unexpected public arguments: %v %q %q %v", resident, path, revision, err)
	}
}
