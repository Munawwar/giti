/*
 * This file includes an adaptation of gitg's lane layout.
 *
 * Copyright (C) 2012 Jesse van den Kieboom
 * Copyright (C) 2026 Munawwar
 *
 * Adapted from libgitg/gitg-lanes.vala and libgitg/gitg-color.vala at gitg commit
 * 28c4314f9a82850b4a84c1535e0e9dccbc2771b1:
 * https://gitlab.gnome.org/GNOME/gitg
 *
 * SPDX-License-Identifier: GPL-2.0-or-later
 */

package main

import (
	"context"
	"fmt"
	"math"
	"runtime"

	"github.com/gotk3/gotk3/cairo"
	"github.com/gotk3/gotk3/gdk"
)

func renderGraphs(rows []historyRow, width, height int, canceled func() bool) ([]*gdk.Pixbuf, error) {
	graphs, cache := make([]*gdk.Pixbuf, len(rows)), make(map[string]*gdk.Pixbuf)
	// Pixbuf storage is native and invisible to Go's heap accounting. Release
	// incomplete renders eagerly instead of waiting for tiny wrappers to be GC'd.
	complete := false
	defer func() {
		if complete {
			return
		}
		for _, graph := range cache {
			runtime.SetFinalizer(graph, nil)
			graph.Unref()
		}
	}()
	for index, row := range rows {
		if canceled() {
			return nil, context.Canceled
		}
		key := fmt.Sprintf("%s:%d:%v:%v", row.kind, row.graph.position, row.graph.lanes, row.graph.next)
		if graph := cache[key]; graph != nil {
			graphs[index] = graph
			continue
		}
		graph, err := renderGraph(row, width, height)
		if err != nil {
			return nil, err
		}
		cache[key], graphs[index] = graph, graph
	}
	complete = true
	return graphs, nil
}

const (
	graphLaneWidth = 16
	graphRowHeight = 44
)

type graphLane struct {
	color int
	from  []int
}

type graphLayout struct {
	lanes, next []graphLane
	position    int
}

type laneContainer struct {
	lane     graphLane
	from, to string
}

// layoutGraph adapts gitg's streaming lane allocator. Each lane records the
// columns from which it arrived, allowing the renderer to draw merges and
// lane moves without relying on git log --graph's terminal characters.
func layoutGraph(rows []historyRow) {
	lanes, color := make([]*laneContainer, 0), 0
	for rowIndex := range rows {
		row := &rows[rowIndex]
		// Reuse the lane waiting for this revision, or allocate a new root lane.
		position := -1
		for index, container := range lanes {
			if container.to == row.revision {
				position = index
				break
			}
		}
		if position < 0 {
			lanes = append(lanes, &laneContainer{lane: graphLane{color: color}, from: row.revision})
			position, color = len(lanes)-1, color+1
		} else {
			lanes[position].from, lanes[position].to = row.revision, ""
		}

		// Snapshot incoming geometry before mutating the streaming lanes for the
		// next row; renderGraph needs both sides of every commit boundary.
		row.graph.position = position
		row.graph.lanes = make([]graphLane, len(lanes))
		for index, container := range lanes {
			row.graph.lanes[index] = graphLane{color: container.lane.color, from: append([]int(nil), container.lane.from...)}
			container.lane.from = []int{index}
		}

		// Route the first parent through the current lane when possible. Additional
		// parents join existing lanes or allocate new colored merge lanes.
		mine := lanes[position]
		for parentIndex, parent := range row.parents {
			parentPosition := -1
			for index, container := range lanes {
				if container.to == parent {
					parentPosition = index
					break
				}
			}
			if parentPosition >= 0 {
				parentLane := lanes[parentPosition]
				if parentIndex == 0 && position < parentPosition {
					mine.from, mine.to = row.revision, parent
					mine.lane.from = append(mine.lane.from, parentPosition)
					lanes = append(lanes[:parentPosition], lanes[parentPosition+1:]...)
				} else {
					parentLane.from = row.revision
					parentLane.lane.from = append(parentLane.lane.from, position)
				}
			} else if mine.to == "" {
				mine.to = parent
			} else {
				lanes = append(lanes, &laneContainer{lane: graphLane{color: color, from: []int{position}}, from: row.revision, to: parent})
				color++
			}
		}
		// A lane with no remaining parent terminates at this commit.
		if mine.to == "" {
			for index, container := range lanes {
				if container == mine {
					lanes = append(lanes[:index], lanes[index+1:]...)
					break
				}
			}
		}
	}
	for index := 0; index+1 < len(rows); index++ {
		rows[index].graph.next = rows[index+1].graph.lanes
	}
}

var graphColors = [][3]float64{
	// Gitg's lightest colors are darkened to at least APCA Lc 45 against
	// white and Giti's #ffe2d2 selected-row background (apca-w3 0.1.9).
	{166.0 / 255, 124.0 / 255, 0},
	{78.0 / 255, 154.0 / 255, 6.0 / 255},
	{206.0 / 255, 92.0 / 255, 0},
	{32.0 / 255, 74.0 / 255, 135.0 / 255},
	{108.0 / 255, 53.0 / 255, 102.0 / 255},
	{164.0 / 255, 0, 0},
	{58.0 / 255, 135.0 / 255, 53.0 / 255},
	{212.0 / 255, 119.0 / 255, 0},
	{82.0 / 255, 125.0 / 255, 171.0 / 255},
	{168.0 / 255, 139.0 / 255, 0},
	{136.0 / 255, 138.0 / 255, 133.0 / 255},
	{173.0 / 255, 127.0 / 255, 168.0 / 255},
	{189.0 / 255, 127.0 / 255, 36.0 / 255},
	{239.0 / 255, 41.0 / 255, 41.0 / 255},
}

// renderGraph turns the UI-neutral lane description into a GTK pixbuf. Both
// halves use the same curve geometry: the current layout above the node and
// the following commit's layout below it.
func renderGraph(row historyRow, width, height int) (*gdk.Pixbuf, error) {
	surface := cairo.CreateImageSurface(cairo.FORMAT_ARGB32, width, height)
	defer surface.Close()
	context := cairo.Create(surface)
	defer context.Close()
	context.SetLineWidth(2)
	context.SetLineCap(cairo.LINE_CAP_ROUND)
	center := float64(height) / 2
	if row.kind == "commit" {
		for half, lanes := range [][]graphLane{row.graph.lanes, row.graph.next} {
			for destination, lane := range lanes {
				color := graphColors[lane.color%len(graphColors)]
				context.SetSourceRGB(color[0], color[1], color[2])
				for _, source := range lane.from {
					x1 := float64(source*graphLaneWidth + graphLaneWidth/2)
					x2 := float64(destination*graphLaneWidth + graphLaneWidth/2)
					if half == 0 {
						context.MoveTo(x1, -center)
						context.CurveTo(x1, 0, x2, 0, x2, center)
					} else {
						context.MoveTo(x1, center)
						context.CurveTo(x1, float64(height), x2, float64(height), x2, float64(height)+center)
					}
					context.Stroke()
				}
			}
		}
		lane := row.graph.lanes[row.graph.position]
		color := graphColors[lane.color%len(graphColors)]
		context.Arc(float64(row.graph.position*graphLaneWidth+graphLaneWidth/2), center, 5, 0, 2*math.Pi)
		context.SetLineWidth(1)
		context.SetSourceRGB(0.12, 0.12, 0.12)
		context.StrokePreserve()
		context.SetSourceRGB(color[0], color[1], color[2])
		context.Fill()
	} else {
		context.Arc(float64(graphLaneWidth/2), center, 4, 0, 2*math.Pi)
		context.SetSourceRGB(0.42, 0.45, 0.49)
		context.Fill()
	}
	surface.Flush()
	return gdk.PixbufGetFromSurface(surface, 0, 0, width, height)
}
