// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package font

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"

	ot "github.com/go-text/typesetting/font/opentype"
)

var (
	errEmptySbixTable   = errors.New("empty 'sbix' table")
	errEmptyBitmapTable = errors.New("empty bitmap table")
)

type (
	Segment      = ot.Segment
	SegmentPoint = ot.SegmentPoint
)

// GlyphData describe how to draw a glyph.
// It is either an GlyphOutline, GlyphSVG or GlyphBitmap.
type GlyphData interface {
	isGlyphData()
}

func (GlyphOutline) isGlyphData() {}
func (GlyphSVG) isGlyphData()     {}
func (GlyphBitmap) isGlyphData()  {}

// GlyphOutline exposes the path to draw for
// vector glyph.
// Coordinates are expressed in fonts units.
type GlyphOutline struct {
	Segments []Segment
}

// Sideways updates the coordinates of the outline by applying
// a 90° clockwise rotation, and adding [yOffset] afterwards.
//
// When used for vertical text, pass
// -Glyph.YOffset, converted in font units, as [yOffset]
// (a positive value to lift the glyph up).
func (o GlyphOutline) Sideways(yOffset float32) {
	for i := range o.Segments {
		target := o.Segments[i].Args[:]
		target[0].X, target[0].Y = target[0].Y, -target[0].X+yOffset
		target[1].X, target[1].Y = target[1].Y, -target[1].X+yOffset
		target[2].X, target[2].Y = target[2].Y, -target[2].X+yOffset
	}
}

// GlyphSVG is an SVG description for the glyph,
// as found in Opentype SVG table.
type GlyphSVG struct {
	// The SVG image content, decompressed if needed.
	// The actual glyph description is an SVG element
	// with id="glyph<GID>" (as in id="glyph12"),
	// and several glyphs may share the same Source
	Source []byte

	// According to the specification, a fallback outline
	// should be specified for each SVG glyphs
	Outline GlyphOutline
}

type GlyphBitmap struct {
	// The actual image content, whose interpretation depends
	// on the Format field.
	Data          []byte
	Format        BitmapFormat
	Width, Height int // number of columns and rows

	// Outline may be specified to be drawn with bitmap
	Outline *GlyphOutline
}

// BitmapFormat identifies the format on the glyph
// raw data. Across the various font files, many formats
// may be encountered : black and white bitmaps, PNG, TIFF, JPG.
type BitmapFormat uint8

const (
	_ BitmapFormat = iota
	// The [GlyphBitmap.Data] slice stores a black or white (0/1)
	// bit image, whose length L satisfies
	// L * 8 >= [GlyphBitmap.Width] * [GlyphBitmap.Height]
	BlackAndWhite
	// The [GlyphBitmap.Data] slice stores a PNG encoded image
	PNG
	// The [GlyphBitmap.Data] slice stores a JPG encoded image
	JPG
	// The [GlyphBitmap.Data] slice stores a TIFF encoded image
	TIFF
)

// BitmapSize expose the size of bitmap glyphs.
// One font may contain several sizes.
type BitmapSize struct {
	Height, Width uint16
	XPpem, YPpem  uint16
}

// GlyphData returns the glyph content for [gid], or nil if
// not found.
func (f *Face) GlyphData(gid GID) GlyphData {
	// since outline may be specified for SVG and bitmaps, check it at the end
	outB, err := f.sbix.glyphData(gID(gid), f.xPpem, f.yPpem)
	if err == nil {
		outline, ok := f.outlineGlyphData(gID(gid))
		if ok {
			outB.Outline = &outline
		}
		return outB
	}

	outB, err = f.bitmap.glyphData(gID(gid), f.xPpem, f.yPpem)
	if err == nil {
		outline, ok := f.outlineGlyphData(gID(gid))
		if ok {
			outB.Outline = &outline
		}
		return outB
	}

	outS, ok := f.svg.glyphData(gID(gid))
	if ok {
		// Spec :
		// For every SVG glyph description, there must be a corresponding TrueType,
		// CFF or CFF2 glyph description in the font.
		outS.Outline, _ = f.outlineGlyphData(gID(gid))
		return outS
	}

	if out, ok := f.outlineGlyphData(gID(gid)); ok {
		return out
	}

	return nil
}

func (sb sbix) glyphData(gid gID, xPpem, yPpem uint16) (GlyphBitmap, error) {
	st := sb.chooseStrike(xPpem, yPpem)
	if st == nil {
		return GlyphBitmap{}, errEmptySbixTable
	}

	glyph := strikeGlyph(st, gid, 0)
	if glyph.GraphicType == 0 {
		return GlyphBitmap{}, fmt.Errorf("no glyph %d in 'sbix' table for resolution (%d, %d)", gid, xPpem, yPpem)
	}

	out := GlyphBitmap{Data: glyph.Data}
	var err error
	out.Width, out.Height, out.Format, err = decodeBitmapConfig(glyph)

	return out, err
}

func (bt bitmap) glyphData(gid gID, xPpem, yPpem uint16) (GlyphBitmap, error) {
	st := bt.chooseStrike(xPpem, yPpem)
	if st == nil || st.ppemX == 0 || st.ppemY == 0 {
		return GlyphBitmap{}, errEmptyBitmapTable
	}

	subtable := st.findTable(gid)
	if subtable == nil {
		return GlyphBitmap{}, fmt.Errorf("no glyph %d in bitmap table for resolution (%d, %d)", gid, xPpem, yPpem)
	}

	glyph := subtable.image(gid)
	if glyph == nil {
		return GlyphBitmap{}, fmt.Errorf("no glyph %d in bitmap table for resolution (%d, %d)", gid, xPpem, yPpem)
	}

	out := GlyphBitmap{
		Data:   glyph.image,
		Width:  int(glyph.metrics.Width),
		Height: int(glyph.metrics.Height),
	}
	switch subtable.imageFormat {
	case 17, 18, 19: // PNG
		out.Format = PNG
	case 2, 5:
		out.Format = BlackAndWhite
		// ensure data length
		L := out.Width * out.Height // in bits
		if len(out.Data)*8 < L {
			return GlyphBitmap{}, fmt.Errorf("EOF in glyph bitmap: expected %d, got %d", L, len(out.Data)*8)
		}
	default:
		return GlyphBitmap{}, fmt.Errorf("unsupported format %d in bitmap table", subtable.imageFormat)
	}

	return out, nil
}

// look for data in 'glyf', 'CFF ' and 'CFF2' tables
func (f *Face) outlineGlyphData(gid gID) (GlyphOutline, bool) {
	out, err := f.glyphDataFromCFF1(gid)
	if err == nil {
		return out, true
	}

	out, err = f.glyphDataFromCFF2(gid)
	if err == nil {
		return out, true
	}

	out, err = f.glyphDataFromGlyf(gid)
	if err == nil {
		return out, true
	}

	return GlyphOutline{}, false
}

func (s svg) glyphData(gid gID) (GlyphSVG, bool) {
	data, ok := s.rawGlyphData(gid)
	if !ok {
		return GlyphSVG{}, false
	}

	// un-compress if needed
	if r, err := gzip.NewReader(bytes.NewReader(data)); err == nil {
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, r); err == nil {
			data = buf.Bytes()
		}
	}

	return GlyphSVG{Source: data}, true
}

// this file converts from font format for glyph outlines to
// segments that rasterizer will consume
//
// adapted from snft/truetype.go

func midPoint(p, q SegmentPoint) SegmentPoint {
	return SegmentPoint{
		X: (p.X + q.X) / 2,
		Y: (p.Y + q.Y) / 2,
	}
}

// build the segments from the resolved contour points
func buildSegments(points []contourPoint) []Segment {
	if len(points) == 0 {
		return nil
	}

	var (
		firstOnCurveValid, firstOffCurveValid, lastOffCurveValid bool
		firstOnCurve, firstOffCurve, lastOffCurve                SegmentPoint
	)

	complete := make([]Segment, 0, len(points)/2+3)
	var tail Segment
	var flush bool

	// TODO: there might be some bug below

	for _, point := range points {
		if flush {
			complete = append(complete, tail)
			tail.Op = 0
			flush = false
		}

		p := point.SegmentPoint
		if !firstOnCurveValid {
			if point.isOnCurve {
				firstOnCurve = p
				firstOnCurveValid = true

				transition := ot.Transitions[ot.SegmentOpMoveTo-1][tail.Op]
				to, at := transition&0b11111, transition>>5
				tail.Op, tail.Args[at] = to, firstOnCurve
				flush = at == 2

			} else if !firstOffCurveValid {
				firstOffCurve = p
				firstOffCurveValid = true

				if !point.isEndPoint {
					continue
				}
			} else {
				firstOnCurve = midPoint(firstOffCurve, p)
				firstOnCurveValid = true
				lastOffCurve = p
				lastOffCurveValid = true

				transition := ot.Transitions[ot.SegmentOpMoveTo-1][tail.Op]
				to, at := transition&0b11111, transition>>5
				tail.Op, tail.Args[at] = to, firstOnCurve
				flush = at == 2
			}
		} else if !lastOffCurveValid {
			if !point.isOnCurve {
				lastOffCurve = p
				lastOffCurveValid = true

				if !point.isEndPoint {
					continue
				}
			} else {
				transition := ot.Transitions[ot.SegmentOpLineTo-1][tail.Op]
				to, at := transition&0b11111, transition>>5
				tail.Op, tail.Args[at] = to, p
				flush = at == 2
			}
		} else {
			var at ot.SegmentOp
			transition := ot.Transitions[ot.SegmentOpQuadTo-1][tail.Op]
			if transition == 0 {
				complete = append(complete, tail)
				tail.Op, at = ot.SegmentOpQuadTo, 0
			} else {
				tail.Op, at = transition&0b11111, transition>>5
			}

			if !point.isOnCurve {
				tail.Args[at], tail.Args[at+1] = lastOffCurve, midPoint(lastOffCurve, p)
				lastOffCurve = p
				lastOffCurveValid = true
			} else {
				tail.Args[at], tail.Args[at+1] = lastOffCurve, p
				lastOffCurveValid = false
			}

			flush = at == 1
		}

		if point.isEndPoint {
			if flush {
				complete = append(complete, tail)
				tail.Op = 0
				flush = false
			}

			// closing the contour
			if !firstOffCurveValid && !lastOffCurveValid {
				transition := ot.Transitions[ot.SegmentOpLineTo-1][tail.Op]
				to, at := transition&0b11111, transition>>5
				tail.Op, tail.Args[at] = to, firstOnCurve
				flush = at == 2
			} else if firstOffCurveValid != lastOffCurveValid {

				var at ot.SegmentOp
				transition := ot.Transitions[ot.SegmentOpQuadTo-1][tail.Op]
				if transition == 0 {
					if tail.Op != 0 {
						complete = append(complete, tail)
					}
					tail.Op, at = ot.SegmentOpQuadTo, 0
				} else {
					tail.Op, at = transition&0b11111, transition>>5
				}

				if !firstOffCurveValid {
					tail.Args[at], tail.Args[at+1] = lastOffCurve, firstOnCurve
				} else {
					tail.Args[at], tail.Args[at+1] = firstOffCurve, firstOnCurve
				}

				flush = at == 1
			} else {
				var at ot.SegmentOp
				transition := ot.Transitions[ot.SegmentOpQuadTo-1][tail.Op]
				if transition == 0 {
					complete = append(complete, tail)
					tail.Op, at = ot.SegmentOpQuadTo, 0
				} else {
					tail.Op, at = transition&0b11111, transition>>5
				}

				tail.Args[at], tail.Args[at+1] = lastOffCurve, midPoint(lastOffCurve, firstOffCurve)
				complete = append(complete, tail)

				tail.Op, tail.Args[at], tail.Args[at+1] = ot.SegmentOpQuadTo, firstOffCurve, firstOnCurve
			}

			firstOnCurveValid = false
			firstOffCurveValid = false
			lastOffCurveValid = false
		}
	}

	if tail.Op != 0 {
		complete = append(complete, tail)
	}
	return complete
}

type errGlyphOutOfRange int

func (e errGlyphOutOfRange) Error() string {
	return fmt.Sprintf("out of range glyph %d", e)
}

// apply variation when needed
func (f *Face) glyphDataFromGlyf(glyph gID) (GlyphOutline, error) {
	if int(glyph) >= len(f.glyf) {
		return GlyphOutline{}, errGlyphOutOfRange(glyph)
	}
	var points []contourPoint
	f.getPointsForGlyph(glyph, 0, &points)
	segments := buildSegments(points[:len(points)-phantomCount])
	return GlyphOutline{Segments: segments}, nil
}

var (
	errNoCFFTable  error = errors.New("no CFF table")
	errNoCFF2Table error = errors.New("no CFF2 table")
)

func (f *Font) glyphDataFromCFF1(glyph gID) (GlyphOutline, error) {
	if f.cff == nil {
		return GlyphOutline{}, errNoCFFTable
	}
	segments, _, err := f.cff.LoadGlyph(glyph)
	if err != nil {
		return GlyphOutline{}, err
	}
	return GlyphOutline{Segments: segments}, nil
}

func (f *Face) glyphDataFromCFF2(glyph gID) (GlyphOutline, error) {
	if f.cff2 == nil {
		return GlyphOutline{}, errNoCFF2Table
	}
	segments, _, err := f.cff2.LoadGlyph(glyph, f.coords)
	if err != nil {
		return GlyphOutline{}, err
	}
	return GlyphOutline{Segments: segments}, nil
}

// BitmapSizes returns the size of bitmap glyphs present in the font.
func (font *Font) BitmapSizes() []BitmapSize {
	upem := font.head.UnitsPerEm

	avgWidth := font.os2.xAvgCharWidth

	// handle invalid head/os2 tables
	if upem == 0 || font.os2.version == 0xFFFF {
		avgWidth = 1
		upem = 1
	}

	// adapted from freetype tt_face_load_sbit
	if font.bitmap != nil {
		return font.bitmap.availableSizes(avgWidth, upem)
	}

	if hori := font.hhea; hori != nil {
		return font.sbix.availableSizes(hori, avgWidth, upem)
	}

	return nil
}
