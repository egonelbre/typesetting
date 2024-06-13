// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

// Package opentype provides the low level routines
// required to read and write Opentype font files, including collections.
//
// This package is designed to provide an efficient, lazy, reading API.
//
// For the parsing of the various tables, see package [tables].
package opentype

import "slices"

type Tag uint32

// NewTag returns the tag for <abcd>.
func NewTag(a, b, c, d byte) Tag {
	return Tag(uint32(d) | uint32(c)<<8 | uint32(b)<<16 | uint32(a)<<24)
}

// MustNewTag gives you the Tag corresponding to the acronym.
// This function will panic if the string passed in is not 4 bytes long.
func MustNewTag(str string) Tag {
	if len(str) != 4 {
		panic("invalid tag: must be exactly 4 bytes")
	}
	_ = str[3]
	return NewTag(str[0], str[1], str[2], str[3])
}

// String return the ASCII form of the tag.
func (t Tag) String() string {
	return string([]byte{
		byte(t >> 24),
		byte(t >> 16),
		byte(t >> 8),
		byte(t),
	})
}

type GID uint32

type GlyphExtents struct {
	XBearing float32 // Left side of glyph from origin
	YBearing float32 // Top side of glyph from origin
	Width    float32 // Distance from left to right side
	Height   float32 // Distance from top to bottom side
}

type SegmentOp uint8

const (
	SegmentOpNone SegmentOp = iota

	SegmentOpMoveTo
	SegmentOpLineTo
	SegmentOpQuadTo

	SegmentOpMoveTo_MoveTo
	SegmentOpMoveTo_LineTo
	SegmentOpLineTo_LineTo
	SegmentOpLineTo_MoveTo

	SegmentOpCubeTo

	SegmentOpMoveTo_QuadTo
	SegmentOpLineTo_QuadTo
	SegmentOpQuadTo_MoveTo
	SegmentOpQuadTo_LineTo

	SegmentOpMoveTo_MoveTo_MoveTo
	SegmentOpMoveTo_LineTo_MoveTo
	SegmentOpLineTo_LineTo_MoveTo
	SegmentOpLineTo_MoveTo_MoveTo

	SegmentOpMoveTo_MoveTo_LineTo
	SegmentOpMoveTo_LineTo_LineTo
	SegmentOpLineTo_LineTo_LineTo
	SegmentOpLineTo_MoveTo_LineTo
)

var segmentOpUsed = [256]byte{
	SegmentOpMoveTo: 1,
	SegmentOpLineTo: 1,
	SegmentOpQuadTo: 2,
	SegmentOpCubeTo: 3,

	SegmentOpMoveTo_MoveTo: 2,
	SegmentOpMoveTo_LineTo: 2,
	SegmentOpLineTo_LineTo: 2,
	SegmentOpLineTo_MoveTo: 2,

	SegmentOpMoveTo_QuadTo: 3,
	SegmentOpLineTo_QuadTo: 3,
	SegmentOpQuadTo_MoveTo: 3,
	SegmentOpQuadTo_LineTo: 3,

	SegmentOpMoveTo_MoveTo_MoveTo: 3,
	SegmentOpMoveTo_LineTo_MoveTo: 3,
	SegmentOpLineTo_LineTo_MoveTo: 3,
	SegmentOpLineTo_MoveTo_MoveTo: 3,
	SegmentOpMoveTo_MoveTo_LineTo: 3,
	SegmentOpMoveTo_LineTo_LineTo: 3,
	SegmentOpLineTo_LineTo_LineTo: 3,
	SegmentOpLineTo_MoveTo_LineTo: 3,
}

var transitionTable = [3][8]SegmentOp{
	SegmentOpMoveTo - 1: {
		SegmentOpNone: SegmentOpMoveTo | (0 << 5),

		SegmentOpMoveTo: SegmentOpMoveTo_MoveTo | (1 << 5),
		SegmentOpLineTo: SegmentOpLineTo_MoveTo | (1 << 5),

		SegmentOpQuadTo: SegmentOpQuadTo_MoveTo | (2 << 5),

		SegmentOpMoveTo_MoveTo: SegmentOpMoveTo_MoveTo_MoveTo | (2 << 5),
		SegmentOpMoveTo_LineTo: SegmentOpMoveTo_LineTo_MoveTo | (2 << 5),
		SegmentOpLineTo_LineTo: SegmentOpLineTo_LineTo_MoveTo | (2 << 5),
		SegmentOpLineTo_MoveTo: SegmentOpLineTo_MoveTo_MoveTo | (2 << 5),
	},
	SegmentOpLineTo - 1: {
		SegmentOpNone: SegmentOpLineTo | (0 << 5),

		SegmentOpMoveTo: SegmentOpMoveTo_LineTo | (1 << 5),
		SegmentOpLineTo: SegmentOpLineTo_LineTo | (1 << 5),

		SegmentOpQuadTo: SegmentOpQuadTo_LineTo | (2 << 5),

		SegmentOpMoveTo_MoveTo: SegmentOpMoveTo_MoveTo_LineTo | (2 << 5),
		SegmentOpMoveTo_LineTo: SegmentOpMoveTo_LineTo_LineTo | (2 << 5),
		SegmentOpLineTo_LineTo: SegmentOpLineTo_LineTo_LineTo | (2 << 5),
		SegmentOpLineTo_MoveTo: SegmentOpLineTo_MoveTo_LineTo | (2 << 5),
	},
	SegmentOpQuadTo - 1: {
		SegmentOpNone: SegmentOpQuadTo | (0 << 5),

		SegmentOpMoveTo: SegmentOpMoveTo_QuadTo | (1 << 5),
		SegmentOpLineTo: SegmentOpLineTo_QuadTo | (1 << 5),
	},
}

type SegmentPoint struct {
	X, Y float32 // expressed in fonts units
}

// Move translates the point.
func (pt *SegmentPoint) Move(dx, dy float32) {
	pt.X += dx
	pt.Y += dy
}

type Segment struct {
	Op SegmentOp
	// Args is up to three (x, y) coordinates, depending on the
	// operation.
	// The Y axis increases up.
	Args [3]SegmentPoint
}

// ArgsSlice returns the effective slice of points
// used (whose length is between 1 and 3).
func (s *Segment) ArgsSlice() []SegmentPoint {
	switch s.Op {
	case SegmentOpMoveTo, SegmentOpLineTo:
		return s.Args[0:1]
	case SegmentOpQuadTo:
		return s.Args[0:2]
	case SegmentOpCubeTo:
		return s.Args[0:3]
	default:
		panic("unreachable")
	}
}

type SegmentsBuilder struct {
	complete []Segment

	tail Segment
}

func (builder *SegmentsBuilder) Grow(n int) {
	builder.complete = slices.Grow(builder.complete, n)
}

func (builder *SegmentsBuilder) Finish() []Segment {
	if builder.tail.Op != 0 {
		builder.complete = append(builder.complete, builder.tail)
		builder.tail.Op = 0
	}
	return builder.complete
}

func (builder *SegmentsBuilder) MoveTo(p SegmentPoint) {
	transition := transitionTable[SegmentOpMoveTo-1][builder.tail.Op]
	to, at := transition&0b11111, transition>>5
	builder.tail.Op = to
	builder.tail.Args[at] = p
	if at == 2 {
		builder.complete = append(builder.complete, builder.tail)
		builder.tail.Op = 0
	}
}

func (builder *SegmentsBuilder) LineTo(p SegmentPoint) {
	transition := transitionTable[SegmentOpLineTo-1][builder.tail.Op]
	to, at := transition&0b11111, transition>>5
	builder.tail.Op = to
	builder.tail.Args[at] = p
	if at == 2 {
		builder.complete = append(builder.complete, builder.tail)
		builder.tail.Op = 0
	}
}

func (builder *SegmentsBuilder) QuadTo(a, b SegmentPoint) {
	transition := transitionTable[SegmentOpQuadTo-1][builder.tail.Op]
	var to, at SegmentOp
	if transition == 0 {
		builder.complete = append(builder.complete, builder.tail)
		builder.tail.Op = 0
		to, at = SegmentOpQuadTo, 0
	} else {
		to, at = transition&0b11111, transition>>5
	}

	builder.tail.Op = to
	builder.tail.Args[at] = a
	builder.tail.Args[at+1] = b

	if at == 1 {
		builder.complete = append(builder.complete, builder.tail)
		builder.tail.Op = 0
	}
}

func (builder *SegmentsBuilder) CubeTo(a, b, c SegmentPoint) {
	if builder.tail.Op != 0 {
		builder.complete = append(builder.complete, builder.tail)
		builder.tail.Op = 0
	}

	builder.complete = append(builder.complete, Segment{
		Op:   SegmentOpCubeTo,
		Args: [3]SegmentPoint{a, b, c},
	})
}
