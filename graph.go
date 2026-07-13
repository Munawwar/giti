/*
 * This file includes an adaptation of gitg's lane layout.
 *
 * Copyright (C) 2012 Jesse van den Kieboom
 * Copyright (C) 2026 Munawwar
 *
 * Adapted from libgitg/gitg-lanes.vala at gitg commit
 * 28c4314f9a82850b4a84c1535e0e9dccbc2771b1:
 * https://gitlab.gnome.org/GNOME/gitg
 *
 * SPDX-License-Identifier: GPL-2.0-or-later
 */

package main

import (
	"math"

	"github.com/gotk3/gotk3/cairo"
	"github.com/gotk3/gotk3/gdk"
)

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

		row.graph.position = position
		row.graph.lanes = make([]graphLane, len(lanes))
		for index, container := range lanes {
			row.graph.lanes[index] = graphLane{color: container.lane.color, from: append([]int(nil), container.lane.from...)}
			container.lane.from = []int{index}
		}

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
	{196.0 / 255, 160.0 / 255, 0},
	{78.0 / 255, 154.0 / 255, 6.0 / 255},
	{206.0 / 255, 92.0 / 255, 0},
	{32.0 / 255, 74.0 / 255, 135.0 / 255},
	{108.0 / 255, 53.0 / 255, 102.0 / 255},
	{164.0 / 255, 0, 0},
	{138.0 / 255, 226.0 / 255, 52.0 / 255},
	{252.0 / 255, 175.0 / 255, 62.0 / 255},
	{114.0 / 255, 159.0 / 255, 207.0 / 255},
	{252.0 / 255, 233.0 / 255, 79.0 / 255},
	{136.0 / 255, 138.0 / 255, 133.0 / 255},
	{173.0 / 255, 127.0 / 255, 168.0 / 255},
	{233.0 / 255, 185.0 / 255, 110.0 / 255},
	{239.0 / 255, 41.0 / 255, 41.0 / 255},
}

// renderGraph turns the UI-neutral lane description into a GTK pixbuf. Both
// halves use the same curve geometry: the current layout above the node and
// the following commit's layout below it.
func renderGraph(row historyRow, width int) (*gdk.Pixbuf, error) {
	surface := cairo.CreateImageSurface(cairo.FORMAT_ARGB32, width, graphRowHeight)
	context := cairo.Create(surface)
	context.SetLineWidth(2)
	context.SetLineCap(cairo.LINE_CAP_ROUND)
	center := float64(graphRowHeight) / 2
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
						context.CurveTo(x1, graphRowHeight, x2, graphRowHeight, x2, graphRowHeight+center)
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
	return gdk.PixbufGetFromSurface(surface, 0, 0, width, graphRowHeight)
}
