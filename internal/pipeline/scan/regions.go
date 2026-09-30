package scan

import (
	"fmt"
	"math"
	"sort"
)

// PDF page dimensions and the four registration-mark centers in normalized
// top-left image coordinates. These are only the target frame. The source
// image is measured on every scan, so DPI and perspective do not matter.
const (
	pageWidthPT  = 595.28
	pageHeightPT = 841.89
)

type point struct{ X, Y float64 }
type marker struct {
	point
	W, H, Area int
}

type homography struct{ m [3][3]float64 }

func identityHomography() homography {
	return homography{m: [3][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}}
}

func (h homography) mapPoint(p point) point {
	z := h.m[2][0]*p.X + h.m[2][1]*p.Y + h.m[2][2]
	if math.Abs(z) < 1e-9 {
		return point{}
	}
	return point{(h.m[0][0]*p.X + h.m[0][1]*p.Y + h.m[0][2]) / z, (h.m[1][0]*p.X + h.m[1][1]*p.Y + h.m[1][2]) / z}
}

func findMarkers(g grayImage) []marker {
	seen := make([]bool, len(g.Pix))
	var out []marker
	// Search the whole scan. A scanner or camera may leave white margins around
	// the sheet, so the page marks are not necessarily near the image edge.
	for y := 0; y < g.H; y++ {
		for x := 0; x < g.W; x++ {
			i := y*g.W + x
			if seen[i] || g.at(x, y) > 105 {
				continue
			}
			stack := []point{{float64(x), float64(y)}}
			seen[i] = true
			x0, x1, y0, y1, n := x, x, y, y, 0
			for len(stack) > 0 {
				p := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				xx, yy := int(p.X), int(p.Y)
				n++
				if xx < x0 {
					x0 = xx
				}
				if xx > x1 {
					x1 = xx
				}
				if yy < y0 {
					y0 = yy
				}
				if yy > y1 {
					y1 = yy
				}
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if dx == 0 && dy == 0 {
							continue
						}
						nx, ny := xx+dx, yy+dy
						if nx < 0 || nx >= g.W || ny < 0 || ny >= g.H {
							continue
						}
						j := ny*g.W + nx
						if !seen[j] && g.at(nx, ny) <= 105 {
							seen[j] = true
							stack = append(stack, point{float64(nx), float64(ny)})
						}
					}
				}
			}
			w, h := x1-x0+1, y1-y0+1
			aspect := float64(w) / float64(h)
			if aspect < 1 {
				aspect = 1 / aspect
			}
			fill := float64(n) / float64(w*h)
			// The template marker is a small, nearly solid rectangle. Accept
			// both axis directions: a 90-degree rotated scan turns the printed
			// horizontal mark into a vertical component. The page and layout
			// geometry below still have to validate the four-marker hypothesis.
			if n >= 100 && w >= 6 && h >= 6 && w <= int(.05*float64(g.W)) && h <= int(.05*float64(g.H)) && aspect >= 1.0 && aspect <= 3.2 && fill >= .35 {
				out = append(out, marker{point{float64(x0+x1) / 2, float64(y0+y1) / 2}, w, h, n})
			}
		}
	}
	return dedupeMarkers(out)
}

func dedupeMarkers(in []marker) []marker {
	sort.Slice(in, func(i, j int) bool { return in[i].Y < in[j].Y })
	var out []marker
	for _, m := range in {
		duplicate := false
		for _, n := range out {
			if math.Hypot(m.X-n.X, m.Y-n.Y) < float64(max(m.W, n.W))*1.2 {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, m)
		}
	}
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func registrationCorners(markers []marker, width, height int) (map[point]point, bool) {
	if len(markers) < 4 {
		return nil, false
	}
	// Match the four known page corners by geometry. Choosing the top-most
	// marker in each half is unsafe: title text or decorative blocks can be
	// finder-like and would corrupt the page transform.
	targets := []point{
		{14 / pageWidthPT, 45.4 / pageHeightPT},
		{574 / pageWidthPT, 45.4 / pageHeightPT},
		{14 / pageWidthPT, 807.4 / pageHeightPT},
		{574 / pageWidthPT, 807.4 / pageHeightPT},
	}
	type candidate struct {
		index int
		dist  float64
	}
	choices := make([][]candidate, len(targets))
	for i, target := range targets {
		for j, m := range markers {
			x, y := m.X/float64(width), m.Y/float64(height)
			dx, dy := x-target.X, y-target.Y
			choices[i] = append(choices[i], candidate{index: j, dist: dx*dx + dy*dy})
		}
		sort.Slice(choices[i], func(a, b int) bool { return choices[i][a].dist < choices[i][b].dist })
		if len(choices[i]) > 8 {
			choices[i] = choices[i][:8]
		}
	}
	bestCost := math.MaxFloat64
	best := [4]int{-1, -1, -1, -1}
	var assign func(int, [4]int, float64)
	assign = func(slot int, selected [4]int, cost float64) {
		if cost >= bestCost {
			return
		}
		if slot == len(targets) {
			bestCost, best = cost, selected
			return
		}
		for _, option := range choices[slot] {
			used := false
			for i := 0; i < slot; i++ {
				if selected[i] == option.index {
					used = true
					break
				}
			}
			if used {
				continue
			}
			selected[slot] = option.index
			assign(slot+1, selected, cost+option.dist)
		}
	}
	assign(0, [4]int{-1, -1, -1, -1}, 0)
	if best[0] < 0 || bestCost > 4*0.25*0.25 {
		return nil, false
	}
	return map[point]point{
		targets[0]: {markers[best[0]].X / float64(width), markers[best[0]].Y / float64(height)},
		targets[1]: {markers[best[1]].X / float64(width), markers[best[1]].Y / float64(height)},
		targets[2]: {markers[best[2]].X / float64(width), markers[best[2]].Y / float64(height)},
		targets[3]: {markers[best[3]].X / float64(width), markers[best[3]].Y / float64(height)},
	}, true
}

// registrationCornerCandidates returns the finite set of orientation
// hypotheses supported by the four detected page marks. The mark itself does
// not encode "top" or "left", so a rotated scan cannot be disambiguated from
// the marks alone. The scanner later selects a candidate only when the current
// page contains a valid choice-grid structure; otherwise it remains in review.
func registrationCornerCandidates(markers []marker, width, height int) []map[point]point {
	targets := []point{
		{14 / pageWidthPT, 45.4 / pageHeightPT},
		{574 / pageWidthPT, 45.4 / pageHeightPT},
		{14 / pageWidthPT, 807.4 / pageHeightPT},
		{574 / pageWidthPT, 807.4 / pageHeightPT},
	}

	// Keep the normal orientation as the fast path. If the expected corner
	// mapping is too far away (for example after a 180-degree rotation), use
	// the nearest detected marker in each image quadrant instead. This step is
	// relative to the current image bounds, not to a scan pixel template.
	var sourceSets [][4]point
	if base, ok := registrationCorners(markers, width, height); ok {
		var sources [4]point
		for i, target := range targets {
			sources[i] = base[target]
		}
		sourceSets = append(sourceSets, sources)
	}
	quadrantSources, ok := nearestCornerMarkers(markers, width, height)
	if !ok {
		return nil
	}
	sourceSets = append(sourceSets, quadrantSources)

	// Only perimeter-preserving rotations and reflections are geometrically
	// valid page orientations. Arbitrary permutations can create crossed or
	// singular quadrilaterals and add unnecessary scan work.
	permutations := [][4]int{
		{0, 1, 2, 3}, {3, 2, 1, 0}, {1, 0, 3, 2}, {2, 3, 0, 1},
		{0, 2, 1, 3}, {3, 1, 2, 0}, {2, 0, 3, 1}, {1, 3, 0, 2},
	}
	candidates := make([]map[point]point, 0, len(sourceSets)*len(permutations))
	for _, sources := range sourceSets {
		for _, permutation := range permutations {
			candidate := make(map[point]point, len(targets))
			for i, target := range targets {
				candidate[target] = sources[permutation[i]]
			}
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func nearestCornerMarkers(markers []marker, width, height int) ([4]point, bool) {
	var result [4]point
	if width <= 0 || height <= 0 {
		return result, false
	}
	// The generated sheet has additional side marks. Selecting the closest
	// mark to each image corner rejects those side marks while still working
	// when the whole page is inset inside a larger scan canvas.
	cornerTargets := [4]point{{0, 0}, {1, 0}, {0, 1}, {1, 1}}
	used := make(map[int]struct{}, 4)
	for corner, target := range cornerTargets {
		bestIndex := -1
		bestDistance := math.MaxFloat64
		for i, marker := range markers {
			if _, exists := used[i]; exists {
				continue
			}
			x := marker.X / float64(width)
			y := marker.Y / float64(height)
			dx, dy := x-target.X, y-target.Y
			distance := dx*dx + dy*dy
			if distance < bestDistance {
				bestIndex, bestDistance = i, distance
			}
		}
		if bestIndex < 0 {
			return result, false
		}
		used[bestIndex] = struct{}{}
		result[corner] = point{markers[bestIndex].X / float64(width), markers[bestIndex].Y / float64(height)}
	}
	return result, true
}

// fitHomography maps canonical normalized page coordinates to source pixels.
func fitHomography(src map[point]point) (homography, error) {
	if len(src) < 4 {
		return identityHomography(), fmt.Errorf("need four registration marks")
	}
	var a [8][9]float64
	row := 0
	for dst, s := range src {
		x, y := dst.X, dst.Y
		X, Y := s.X, s.Y
		a[row] = [9]float64{x, y, 1, 0, 0, 0, -x * X, -y * X, X}
		row++
		a[row] = [9]float64{0, 0, 0, x, y, 1, -x * Y, -y * Y, Y}
		row++
	}
	for i := 0; i < 8; i++ {
		p := i
		for r := i + 1; r < 8; r++ {
			if math.Abs(a[r][i]) > math.Abs(a[p][i]) {
				p = r
			}
		}
		if math.Abs(a[p][i]) < 1e-10 {
			return identityHomography(), fmt.Errorf("singular registration geometry")
		}
		a[i], a[p] = a[p], a[i]
		v := a[i][i]
		for c := i; c < 9; c++ {
			a[i][c] /= v
		}
		for r := 0; r < 8; r++ {
			if r == i {
				continue
			}
			f := a[r][i]
			for c := i; c < 9; c++ {
				a[r][c] -= f * a[i][c]
			}
		}
	}
	return homography{m: [3][3]float64{{a[0][8], a[1][8], a[2][8]}, {a[3][8], a[4][8], a[5][8]}, {a[6][8], a[7][8], 1}}}, nil
}

func canonicalTargetPoint(xPT, yTopPT float64) point {
	return point{xPT / pageWidthPT, yTopPT / pageHeightPT}
}
func sampleCanonical(g grayImage, h homography, xPT, yTopPT, rX, rY float64) float64 {
	// Average darkness inside a small elliptical window. Sampling through the
	// fitted homography absorbs scale, crop, rotation, and mild perspective.
	if rX <= 0 || rY <= 0 {
		return 0
	}
	sum := 0.0
	n := 0
	for iy := -4; iy <= 4; iy++ {
		for ix := -4; ix <= 4; ix++ {
			dx := float64(ix) / 4 * rX
			dy := float64(iy) / 4 * rY
			if dx*dx/(rX*rX)+dy*dy/(rY*rY) > 1 {
				continue
			}
			p := h.mapPoint(canonicalTargetPoint(xPT+dx, yTopPT+dy))
			xx := int(math.Round(p.X * float64(g.W)))
			yy := int(math.Round(p.Y * float64(g.H)))
			sum += dark(g.at(xx, yy))
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func choiceBubbleScore(g grayImage, h homography, x, y float64) float64 {
	return sampleCanonical(g, h, x, y, 3.8, 3.8)
}

func pageStatus(calibrated bool, markers int) (string, []string) {
	if calibrated {
		return StatusRecognized, nil
	}
	return StatusNeedsReview, []string{fmt.Sprintf("定位标记不足或无法拟合页面变换(%d个)", markers)}
}

func sortMarkers(ms []marker) { sort.Slice(ms, func(i, j int) bool { return ms[i].Y < ms[j].Y }) }
