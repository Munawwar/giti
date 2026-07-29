package main

import (
	"context"
	"errors"
	"testing"
)

func TestGraphLayout(t *testing.T) {
	{
		rows := []historyRow{
			{kind: "commit", revision: "c", parents: []string{"b"}},
			{kind: "commit", revision: "b", parents: []string{"a"}},
			{kind: "commit", revision: "a"},
		}
		layoutGraph(rows)
		for index, row := range rows {
			if row.graph.position != 0 || len(row.graph.lanes) != 1 {
				t.Fatalf("row %d left the main lane: %#v", index, row.graph)
			}
			if index > 0 && (len(row.graph.lanes[0].from) != 1 || row.graph.lanes[0].from[0] != 0) {
				t.Fatalf("row %d lost its vertical edge: %#v", index, row.graph.lanes)
			}
		}
		if len(rows[0].graph.next) != 1 || rows[0].graph.next[0].from[0] != 0 {
			t.Fatalf("next-row edge missing: %#v", rows[0].graph.next)
		}
	}
	{
		rows := []historyRow{
			{kind: "commit", revision: "merge", parents: []string{"left", "right"}},
			{kind: "commit", revision: "left", parents: []string{"root"}},
			{kind: "commit", revision: "right", parents: []string{"root"}},
			{kind: "commit", revision: "root"},
		}
		layoutGraph(rows)
		if rows[2].graph.position != 1 || len(rows[2].graph.lanes) != 2 {
			t.Fatalf("side commit is not on its own lane: %#v", rows[2].graph)
		}
		root := rows[3].graph
		if root.position != 0 || len(root.lanes) != 1 || len(root.lanes[0].from) != 2 || root.lanes[0].from[0] != 0 || root.lanes[0].from[1] != 1 {
			t.Fatalf("merge did not converge at root: %#v", root)
		}
	}
	{
		rows := []historyRow{
			{kind: "commit", revision: "octopus", parents: []string{"one", "two", "three"}},
			{kind: "commit", revision: "one", parents: []string{"root"}},
			{kind: "commit", revision: "two", parents: []string{"root"}},
			{kind: "commit", revision: "three", parents: []string{"root"}},
			{kind: "commit", revision: "root"},
			{kind: "commit", revision: "unrelated"},
		}
		layoutGraph(rows)
		if rows[3].graph.position != 1 || len(rows[3].graph.lanes) != 2 {
			t.Fatalf("third octopus parent is misplaced: %#v", rows[3].graph)
		}
		if got := rows[4].graph.lanes[0].from; len(rows[4].graph.lanes) != 1 || len(got) != 2 {
			t.Fatalf("octopus lanes did not converge: %#v", rows[4].graph)
		}
		if rows[5].graph.position != 0 || len(rows[5].graph.lanes) != 1 || len(rows[5].graph.lanes[0].from) != 0 {
			t.Fatalf("independent root inherited an edge: %#v", rows[5].graph)
		}
	}
	{
		rows := []historyRow{
			{kind: "commit", revision: "m1", parents: []string{"m2", "s1"}},
			{kind: "commit", revision: "m2", parents: []string{"m3", "s2"}},
			{kind: "commit", revision: "m3", parents: []string{"m4", "s3"}},
			{kind: "commit", revision: "m4", parents: []string{"m5", "s4"}},
			{kind: "commit", revision: "m5", parents: []string{"root", "s5"}},
			{kind: "commit", revision: "s1", parents: []string{"root"}},
			{kind: "commit", revision: "s2", parents: []string{"root"}},
			{kind: "commit", revision: "s3", parents: []string{"root"}},
			{kind: "commit", revision: "s4", parents: []string{"root"}},
			{kind: "commit", revision: "s5", parents: []string{"root"}},
			{kind: "commit", revision: "root"},
		}
		layoutGraph(rows)
		maximum := 0
		for index, row := range rows {
			maximum = max(maximum, len(row.graph.lanes))
			if index == 0 {
				continue
			}
			for _, lane := range row.graph.lanes {
				for _, source := range lane.from {
					if source < 0 || source >= len(rows[index-1].graph.lanes) {
						t.Fatalf("row %d has invalid source lane %d: %#v", index, source, row.graph)
					}
				}
			}
		}
		if maximum != 6 {
			t.Fatalf("maximum lane count is %d, want 6", maximum)
		}
	}
}

func TestRenderedGraphs(t *testing.T) {
	{
		rows := []historyRow{
			{kind: "commit", revision: "merge", parents: []string{"left", "right"}},
			{kind: "commit", revision: "left", parents: []string{"root"}},
			{kind: "commit", revision: "right", parents: []string{"root"}},
			{kind: "commit", revision: "root"},
		}
		layoutGraph(rows)
		upper, err := renderGraph(rows[0], 48, graphRowHeight)
		if err != nil {
			t.Fatal(err)
		}
		lower, err := renderGraph(rows[1], 48, graphRowHeight)
		if err != nil {
			t.Fatal(err)
		}
		for _, sample := range []struct {
			pixbuf      []byte
			row, stride int
		}{{upper.GetPixels(), graphRowHeight - 1, upper.GetRowstride()}, {lower.GetPixels(), 0, lower.GetRowstride()}} {
			alpha := sample.pixbuf[sample.row*sample.stride+graphLaneWidth*upper.GetNChannels()+3]
			if alpha == 0 {
				t.Fatal("shifted lane is transparent where neighboring curves meet")
			}
		}
	}
	{
		rows := []historyRow{
			{kind: "commit", revision: "four", parents: []string{"three"}},
			{kind: "commit", revision: "three", parents: []string{"two"}},
			{kind: "commit", revision: "two", parents: []string{"one"}},
			{kind: "commit", revision: "one"},
		}
		layoutGraph(rows)
		graphs, err := renderGraphs(rows, 48, graphRowHeight, func() bool { return false })
		if err != nil {
			t.Fatal(err)
		}
		if graphs[1] != graphs[2] {
			t.Fatal("identical linear lane rows retained separate pixbufs")
		}
	}
}

func TestCanceledGraphRenderReleasesPartialResult(t *testing.T) {
	checks := 0
	graphs, err := renderGraphs([]historyRow{{kind: "unstaged"}, {kind: "staged"}}, 48, graphRowHeight, func() bool {
		checks++
		return checks > 1
	})
	if graphs != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("partial graph render was not canceled: graphs=%v err=%v", graphs, err)
	}
}

func BenchmarkGraphLayout(b *testing.B) {
	fixture := make([]historyRow, 500)
	for index := range fixture {
		fixture[index] = historyRow{kind: "commit", revision: string(rune(index + 1))}
		if index+1 < len(fixture) {
			fixture[index].parents = []string{string(rune(index + 2))}
		}
		if index%20 == 0 && index+10 < len(fixture) {
			fixture[index].parents = append(fixture[index].parents, string(rune(index+11)))
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rows := make([]historyRow, len(fixture))
		copy(rows, fixture)
		layoutGraph(rows)
	}
}
