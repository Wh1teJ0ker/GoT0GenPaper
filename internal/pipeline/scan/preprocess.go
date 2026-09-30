package scan

import (
	"image"
	"math"
)

type grayImage struct {
	W, H int
	Pix  []uint8
}

func toGray(im image.Image) grayImage {
	b := im.Bounds()
	g := grayImage{W: b.Dx(), H: b.Dy(), Pix: make([]uint8, b.Dx()*b.Dy())}
	for y := 0; y < g.H; y++ {
		for x := 0; x < g.W; x++ {
			r, gg, bb, _ := im.At(b.Min.X+x, b.Min.Y+y).RGBA()
			// Rec. 601 is enough for black registration marks and pencil marks.
			g.Pix[y*g.W+x] = uint8((299*r + 587*gg + 114*bb) / 1000 >> 8)
		}
	}
	return g
}

func (g grayImage) at(x, y int) uint8 {
	if x < 0 {
		x = 0
	}
	if x >= g.W {
		x = g.W - 1
	}
	if y < 0 {
		y = 0
	}
	if y >= g.H {
		y = g.H - 1
	}
	return g.Pix[y*g.W+x]
}

func dark(v uint8) float64 { return 1 - float64(v)/255 }

func norm(v, max int) float64 {
	if max <= 1 {
		return 0
	}
	return float64(v) / float64(max-1)
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
