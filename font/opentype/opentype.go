// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

// Package opentype provides the low level routines
// required to read and write Opentype font files, including collections.
//
// This package is designed to provide an efficient, lazy, reading API.
//
// For the parsing of the various tables, see package [tables].
package opentype

import (
	"math/bits"
	"slices"
)

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
	SegmentOpNone SegmentOp = 0

	SegmentOpMoveTo SegmentOp = 0b01
	SegmentOpLineTo           = 0b10
	SegmentOpQuadTo           = 0b1111
	SegmentOpCubeTo           = 0b111111

	SegmentOpMoveTo_At1 = SegmentOpMoveTo << 2
	SegmentOpLineTo_At1 = SegmentOpLineTo << 2
	SegmentOpQuadTo_At1 = SegmentOpQuadTo << 2

	SegmentOpMoveTo_At2 = SegmentOpMoveTo << 4
	SegmentOpLineTo_At2 = SegmentOpLineTo << 4

	SegmentOpMoveTo_MoveTo = SegmentOpMoveTo | SegmentOpMoveTo_At1
	SegmentOpMoveTo_LineTo = SegmentOpMoveTo | SegmentOpLineTo_At1
	SegmentOpLineTo_LineTo = SegmentOpLineTo | SegmentOpLineTo_At1
	SegmentOpLineTo_MoveTo = SegmentOpLineTo | SegmentOpMoveTo_At1

	SegmentOpMoveTo_QuadTo = SegmentOpMoveTo | SegmentOpQuadTo_At1
	SegmentOpLineTo_QuadTo = SegmentOpLineTo | SegmentOpQuadTo_At1

	SegmentOpQuadTo_MoveTo = SegmentOpQuadTo | SegmentOpMoveTo_At2
	SegmentOpQuadTo_LineTo = SegmentOpQuadTo | SegmentOpLineTo_At2

	SegmentOpMoveTo_MoveTo_MoveTo = SegmentOpMoveTo | SegmentOpMoveTo_At1 | SegmentOpMoveTo_At2
	SegmentOpMoveTo_LineTo_MoveTo = SegmentOpMoveTo | SegmentOpLineTo_At1 | SegmentOpMoveTo_At2
	SegmentOpLineTo_LineTo_MoveTo = SegmentOpLineTo | SegmentOpLineTo_At1 | SegmentOpMoveTo_At2
	SegmentOpLineTo_MoveTo_MoveTo = SegmentOpLineTo | SegmentOpMoveTo_At1 | SegmentOpMoveTo_At2

	SegmentOpMoveTo_MoveTo_LineTo = SegmentOpMoveTo | SegmentOpMoveTo_At1 | SegmentOpLineTo_At2
	SegmentOpMoveTo_LineTo_LineTo = SegmentOpMoveTo | SegmentOpLineTo_At1 | SegmentOpLineTo_At2
	SegmentOpLineTo_LineTo_LineTo = SegmentOpLineTo | SegmentOpLineTo_At1 | SegmentOpLineTo_At2
	SegmentOpLineTo_MoveTo_LineTo = SegmentOpLineTo | SegmentOpMoveTo_At1 | SegmentOpLineTo_At2
)

func (op SegmentOp) Used() int { return (bits.Len8(uint8(op)) + 1) / 2 }

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
	switch builder.tail.Op.Used() {
	case 0:
		builder.tail.Args[0] = p
		builder.tail.Op = SegmentOpMoveTo
	case 1:
		builder.tail.Args[1] = p
		builder.tail.Op |= SegmentOpMoveTo << 2
	case 2:
		builder.tail.Args[2] = p
		builder.tail.Op |= SegmentOpMoveTo << 4
		builder.complete = append(builder.complete, builder.tail)
		builder.tail.Op = 0
	}
}

func (builder *SegmentsBuilder) LineTo(p SegmentPoint) {
	switch builder.tail.Op.Used() {
	case 0:
		builder.tail.Args[0] = p
		builder.tail.Op = SegmentOpLineTo
	case 1:
		builder.tail.Args[1] = p
		builder.tail.Op |= SegmentOpLineTo << 2
	case 2:
		builder.tail.Args[2] = p
		builder.tail.Op |= SegmentOpLineTo << 4
		builder.complete = append(builder.complete, builder.tail)
		builder.tail.Op = 0
	}
}

func (builder *SegmentsBuilder) QuadTo(a, b SegmentPoint) {
	switch builder.tail.Op.Used() {
	default:
		builder.complete = append(builder.complete, builder.tail)
		fallthrough
	case 0:
		builder.tail.Op = SegmentOpQuadTo
		builder.tail.Args[0] = a
		builder.tail.Args[1] = b
	case 1:
		builder.tail.Op |= SegmentOpQuadTo << 2
		builder.tail.Args[1] = a
		builder.tail.Args[2] = b
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
