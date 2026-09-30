package scan

import (
	"math"
	"sort"
)

// choiceLayout is recovered from machine-readable anchors printed next to the
// choice grid. Its points are canonical PDF points, but every point is
// measured from the current scan after page registration.
type choiceLayout struct {
	Centers    []point
	RowAnchors []point
	Confidence float64
}

type layoutAnchor struct {
	point
	W, H  float64
	Score float64
}

type anchorGroup struct {
	RowX      float64
	Columns   []float64
	HeaderY   float64
	RowYs     []float64
	ColumnGap float64
	RowGap    float64
	Score     float64
}

func canonicalDarkAt(g grayImage, h homography, x, y float64) float64 {
	p := h.mapPoint(canonicalTargetPoint(x, y))
	return dark(g.at(int(math.Round(p.X*float64(g.W))), int(math.Round(p.Y*float64(g.H)))))
}

// detectChoiceLayout recovers the printed layout from dedicated square
// anchors. The page homography handles scale, crop, rotation, and perspective;
// the anchors handle changes in the actual printed table geometry.
func detectChoiceLayout(g grayImage, h homography, questionCount int) (choiceLayout, bool) {
	if questionCount <= 0 {
		return choiceLayout{}, false
	}
	anchors := findLayoutAnchors(g, h)
	if len(anchors) < 5 {
		return choiceLayout{}, false
	}

	expectedGroups := (questionCount + 9) / 10
	groups := recoverAnchorGroups(anchors, expectedGroups, questionCount)
	if len(groups) != expectedGroups {
		return choiceLayout{}, false
	}

	centers := make([]point, 0, questionCount*len(optionLabels))
	rowAnchors := make([]point, 0, questionCount)
	for groupIndex, group := range groups {
		needRows := questionCount - groupIndex*10
		if needRows > 10 {
			needRows = 10
		}
		if len(group.RowYs) != needRows {
			return choiceLayout{}, false
		}
		for _, y := range group.RowYs {
			rowAnchors = append(rowAnchors, point{X: group.RowX, Y: y})
			for _, x := range group.Columns {
				centers = append(centers, point{X: x, Y: y})
			}
		}
	}

	needed := questionCount * len(optionLabels)
	if len(centers) != needed {
		return choiceLayout{}, false
	}
	if !validateRecoveredGroups(groups, questionCount) {
		return choiceLayout{}, false
	}
	confidence := 0.0
	for _, group := range groups {
		// group.Score is the sum of the measured anchor scores minus the
		// geometric fit penalties. Normalize it by the number of anchors so a
		// ten-row group is not automatically more confident than a short group.
		anchorCount := 5 + len(group.RowYs)
		anchorQuality := clamp(group.Score/float64(anchorCount), 0, 1)
		columnQuality := clamp(1-spread(diffValues(group.Columns))/4.5, 0, 1)
		rowQuality := clamp(1-spread(diffValues(group.RowYs))/3.5, 0, 1)
		confidence += 0.55*anchorQuality + 0.25*columnQuality + 0.20*rowQuality
	}
	confidence = clamp(confidence/float64(len(groups)), 0, 1)
	return choiceLayout{
		Centers:    centers,
		RowAnchors: rowAnchors,
		Confidence: confidence,
	}, true
}

// findLayoutAnchors finds the printed finder patterns in canonical page space.
// A finder is deliberately recognized by its local black/white/black profile,
// rather than by a connected component. This keeps nearby glyphs, table rules,
// and filled bubbles from becoming layout anchors after rasterization.
func findLayoutAnchors(g grayImage, h homography) []layoutAnchor {
	const (
		minX = 20.0
		maxX = 575.0
		minY = 120.0
		maxY = 780.0
		step = 1.0
	)

	anchors := make([]layoutAnchor, 0, 64)
	for y := minY; y <= maxY; y += step {
		for x := minX; x <= maxX; x += step {
			score, ring, inner, center, outside := finderScore(g, h, x, y)
			solid := solidAnchorScore(g, h, x, y)
			// The current LaTeX template emits a hollow square with a center
			// dot, while older sheets and some scanners reproduce the same
			// anchor as a small solid square. Accept both local signatures;
			// recoverAnchorGroups below still requires a complete, regular
			// header/row geometry before any point can drive OMR.
			ringAnchor := score >= 0.57 && ring >= 0.32 && center >= 0.18 && inner <= 0.70 && outside <= 0.72
			solidAnchor := solid >= 0.62 && outside <= 0.35
			if (!ringAnchor && !solidAnchor) || (ringAnchor && !isFinderMaximum(g, h, x, y, score)) {
				continue
			}
			if solidAnchor && !isSolidAnchorMaximum(g, h, x, y, solid) {
				continue
			}
			if solidAnchor {
				score = math.Max(score, 0.70+0.30*solid)
			}
			anchors = append(anchors, layoutAnchor{point: point{X: x, Y: y}, W: 7.0, H: 7.0, Score: score})
		}
	}
	return dedupeLayoutAnchors(anchors)
}

func isSolidAnchorMaximum(g grayImage, h homography, x, y, score float64) bool {
	for dy := -2.0; dy <= 2.0; dy += 1.0 {
		for dx := -2.0; dx <= 2.0; dx += 1.0 {
			if dx == 0 && dy == 0 {
				continue
			}
			neighbor := solidAnchorScore(g, h, x+dx, y+dy)
			_, _, _, _, outside := finderScore(g, h, x+dx, y+dy)
			if neighbor >= 0.62 && outside <= 0.35 && neighbor > score+0.015 {
				return false
			}
		}
	}
	return true
}

// solidAnchorScore handles sheets produced by older templates or low-quality
// rasterizers that collapse the small frame and center dot into one filled
// square. It deliberately uses a smaller window than the hollow-frame
// detector, so a solid mark is not penalized for having no outer ring.
func solidAnchorScore(g grayImage, h homography, x, y float64) float64 {
	return sampleCanonical(g, h, x, y, 2.7, 2.7)
}

// finderScore measures a small square ring, its white inner window, a dark
// center dot, and a quiet zone around it. The samples are taken in canonical
// coordinates through the page homography, so source DPI and perspective do
// not change the feature's meaning.
func finderScore(g grayImage, h homography, x, y float64) (score, ring, inner, center, outside float64) {
	const (
		ringRadius    = 3.65
		innerRadius   = 2.25
		centerRadius  = 0.95
		outsideRadius = 5.5
	)
	ringOffsets := []point{
		{-ringRadius, -ringRadius}, {-ringRadius / 2, -ringRadius}, {0, -ringRadius}, {ringRadius / 2, -ringRadius}, {ringRadius, -ringRadius},
		{-ringRadius, -ringRadius / 2}, {-ringRadius, 0}, {-ringRadius, ringRadius / 2}, {-ringRadius, ringRadius},
		{-ringRadius / 2, ringRadius}, {0, ringRadius}, {ringRadius / 2, ringRadius}, {ringRadius, ringRadius},
		{ringRadius, -ringRadius / 2}, {ringRadius, 0}, {ringRadius, ringRadius / 2},
	}
	innerOffsets := []point{
		{-innerRadius, -innerRadius}, {0, -innerRadius}, {innerRadius, -innerRadius},
		{-innerRadius, 0}, {innerRadius, 0},
		{-innerRadius, innerRadius}, {0, innerRadius}, {innerRadius, innerRadius},
	}
	for _, offset := range ringOffsets {
		ring += canonicalDarkAt(g, h, x+offset.X, y+offset.Y)
	}
	ring /= float64(len(ringOffsets))
	for _, offset := range innerOffsets {
		inner += canonicalDarkAt(g, h, x+offset.X, y+offset.Y)
	}
	inner /= float64(len(innerOffsets))
	center = sampleCanonical(g, h, x, y, centerRadius, centerRadius)
	outside = 0
	outCount := 0
	for iy := -2; iy <= 2; iy++ {
		for ix := -2; ix <= 2; ix++ {
			dx, dy := float64(ix)*outsideRadius/2, float64(iy)*outsideRadius/2
			if dx*dx+dy*dy < 4.7*4.7 || dx*dx+dy*dy > outsideRadius*outsideRadius {
				continue
			}
			outside += canonicalDarkAt(g, h, x+dx, y+dy)
			outCount++
		}
	}
	if outCount > 0 {
		outside /= float64(outCount)
	}
	// A dark outer ring and center, surrounded by a light inner window and
	// quiet zone, is substantially less likely to occur in normal text.
	score = 0.38*ring + 0.24*center + 0.24*(1-inner) + 0.14*(1-outside)
	return score, ring, inner, center, outside
}

func isFinderMaximum(g grayImage, h homography, x, y, score float64) bool {
	for dy := -2.0; dy <= 2.0; dy += 1.0 {
		for dx := -2.0; dx <= 2.0; dx += 1.0 {
			if dx == 0 && dy == 0 {
				continue
			}
			neighbor, _, _, _, _ := finderScore(g, h, x+dx, y+dy)
			if neighbor > score+0.015 {
				return false
			}
		}
	}
	return true
}

func dedupeLayoutAnchors(in []layoutAnchor) []layoutAnchor {
	sort.Slice(in, func(i, j int) bool {
		if math.Abs(in[i].Score-in[j].Score) > 0.001 {
			return in[i].Score > in[j].Score
		}
		return in[i].Y < in[j].Y
	})
	out := make([]layoutAnchor, 0, len(in))
	for _, anchor := range in {
		duplicate := false
		for _, existing := range out {
			if math.Hypot(anchor.X-existing.X, anchor.Y-existing.Y) < 4.5 {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, anchor)
		}
	}
	// Sort the de-duplicated result. Sorting the input here used to leave the
	// returned slice in score order, which made row reconstruction depend on
	// incidental text-like candidates.
	sort.Slice(out, func(i, j int) bool {
		if math.Abs(out[i].Y-out[j].Y) > 1 {
			return out[i].Y < out[j].Y
		}
		return out[i].X < out[j].X
	})
	return out
}

// recoverAnchorGroups performs a global, geometry-first recovery. A handful
// of finder-like pixels is not enough to identify a group: each group must
// have a complete five-anchor header and a regular sequence of row anchors.
// This matters for photographed/scanned sheets where ordinary glyphs can pass
// the local finder test.
func recoverAnchorGroups(anchors []layoutAnchor, expected, questionCount int) []anchorGroup {
	if expected <= 0 {
		return nil
	}
	rows := clusterAnchorRows(anchors, 5.0)
	candidates := make([]anchorGroup, 0, expected*2)
	for _, row := range rows {
		if len(row) < 5 {
			continue
		}
		sort.Slice(row, func(i, j int) bool { return row[i].X < row[j].X })
		for start := 0; start+4 < len(row); start++ {
			window := row[start : start+5]
			gaps := make([]float64, 0, 4)
			valid := true
			for i := 0; i < 4; i++ {
				gap := window[i+1].X - window[i].X
				// The row-number anchor is intentionally farther from the A
				// option than the option columns are from one another. Only the
				// four option columns must be near-equidistant.
				if (i == 0 && (gap < 8 || gap > 36)) || (i > 0 && (gap < 8 || gap > 28)) {
					valid = false
					break
				}
				gaps = append(gaps, gap)
			}
			if !valid || len(gaps) != 4 {
				continue
			}
			optionGaps := gaps[1:]
			if spread(optionGaps) > 4.0 || coefficientOfVariation(optionGaps) > 0.18 {
				continue
			}
			columns := make([]float64, 4)
			for i := range columns {
				columns[i] = window[i+1].X
			}
			columnGap := medianFloat(gaps)
			group := anchorGroup{
				RowX:      window[0].X,
				Columns:   columns,
				HeaderY:   medianFloat(rowY(window)),
				ColumnGap: columnGap,
				Score:     sumAnchorScores(window) - spread(optionGaps),
			}
			// Recover every possible row count here. The global selector later
			// decides whether this is an early full group (10 rows) or the final
			// partial group. Inferring the count from candidate slice length is
			// unstable because one physical row can yield several candidates.
			for rowCount := 1; rowCount <= 10; rowCount++ {
				rowYs, rowGap, rowScore, ok := recoverRegularRows(anchors, group, rowCount)
				if !ok {
					continue
				}
				candidate := group
				candidate.RowYs = rowYs
				candidate.RowGap = rowGap
				candidate.Score += rowScore
				candidates = append(candidates, candidate)
			}
		}
	}
	if len(candidates) < expected {
		return nil
	}
	selected := selectGlobalGroups(candidates, expected, questionCount)
	if len(selected) != expected || !validateRecoveredGroups(selected, questionCount) {
		return nil
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Columns[0] < selected[j].Columns[0] })
	return selected
}

func recoverRegularRows(anchors []layoutAnchor, group anchorGroup, expected int) ([]float64, float64, float64, bool) {
	if expected <= 0 {
		return nil, 0, 0, false
	}
	candidates := make([]layoutAnchor, 0, expected*2)
	for _, anchor := range anchors {
		if math.Abs(anchor.X-group.RowX) <= 4.5 && anchor.Y > group.HeaderY+7 && anchor.Y < group.HeaderY+235 {
			candidates = append(candidates, anchor)
		}
	}
	if len(candidates) < expected {
		return nil, 0, 0, false
	}
	// Merge raster duplicates before searching for a regular progression.
	clusters := clusterAnchorRows(candidates, 5.0)
	values := make([]float64, 0, len(clusters))
	scores := make([]float64, 0, len(clusters))
	for _, cluster := range clusters {
		values = append(values, medianFloat(rowY(cluster)))
		scores = append(scores, sumAnchorScores(cluster))
	}
	if len(values) < expected {
		return nil, 0, 0, false
	}
	bestScore := -math.MaxFloat64
	var best []float64
	var bestGap float64
	// The step is inferred from the scan. It is deliberately not tied to a
	// template coordinate, so modest scaling/perspective/printing drift is OK.
	for start := 0; start < len(values); start++ {
		for gap := 10.0; gap <= 24.0; gap += 0.5 {
			selected := make([]float64, expected)
			score := 0.0
			last := -1
			valid := true
			for i := 0; i < expected; i++ {
				target := values[start] + float64(i)*gap
				index := nearestValue(values, target, 3.5, last+1)
				if index < 0 {
					valid = false
					break
				}
				selected[i] = values[index]
				score += scores[index] - math.Abs(values[index]-target)*0.6
				last = index
			}
			if !valid || spread(diffValues(selected)) > 3.0 {
				continue
			}
			if score > bestScore {
				bestScore, best, bestGap = score, selected, gap
			}
		}
	}
	if len(best) != expected {
		return nil, 0, 0, false
	}
	return best, bestGap, bestScore, true
}

func selectGlobalGroups(candidates []anchorGroup, expected, questionCount int) []anchorGroup {
	// Sort by score first so false local matches are discarded, then enumerate
	// the small set of non-overlapping combinations. The page has at most four
	// groups, so exhaustive selection is cheap and deterministic.
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	limit := len(candidates)
	if limit > 48 {
		limit = 48
	}
	bestScore := -math.MaxFloat64
	var best []anchorGroup
	var visit func(int, []anchorGroup)
	visit = func(start int, selected []anchorGroup) {
		if len(selected) == expected {
			candidate := append([]anchorGroup(nil), selected...)
			if !validateRecoveredGroups(candidate, questionCount) {
				return
			}
			score := 0.0
			for _, group := range candidate {
				score += group.Score
			}
			if score > bestScore {
				bestScore, best = score, candidate
			}
			return
		}
		for i := start; i < limit; i++ {
			candidate := candidates[i]
			conflict := false
			for _, existing := range selected {
				if groupsOverlap(candidate, existing) || math.Abs(candidate.HeaderY-existing.HeaderY) > 12 {
					conflict = true
					break
				}
			}
			if conflict {
				continue
			}
			visit(i+1, append(selected, candidate))
		}
	}
	visit(0, nil)
	return best
}

func validateRecoveredGroups(groups []anchorGroup, questionCount int) bool {
	if len(groups) == 0 {
		return false
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Columns[0] < groups[j].Columns[0] })
	for i, group := range groups {
		need := questionCount - i*10
		if need > 10 {
			need = 10
		}
		if need <= 0 || len(group.Columns) != 4 || len(group.RowYs) != need || group.ColumnGap < 8 || group.ColumnGap > 28 || group.RowGap < 9 || group.RowGap > 25 {
			return false
		}
		if spread(diffValues(group.Columns)) > 4.5 || spread(diffValues(group.RowYs)) > 3.5 {
			return false
		}
		if i > 0 {
			prev := groups[i-1]
			if groupsOverlap(prev, group) || math.Abs(group.HeaderY-prev.HeaderY) > 12 || math.Abs(group.RowGap-prev.RowGap) > 4 {
				return false
			}
		}
	}
	return true
}

func groupsOverlap(a, b anchorGroup) bool {
	return a.Columns[0] <= b.Columns[len(b.Columns)-1]+5 && b.Columns[0] <= a.Columns[len(a.Columns)-1]+5
}

func nearestValue(values []float64, target, tolerance float64, start int) int {
	best := -1
	bestDistance := tolerance
	for i := start; i < len(values); i++ {
		distance := math.Abs(values[i] - target)
		if distance <= bestDistance {
			best, bestDistance = i, distance
		}
	}
	return best
}

func diffValues(values []float64) []float64 {
	if len(values) < 2 {
		return nil
	}
	diffs := make([]float64, len(values)-1)
	for i := range diffs {
		diffs[i] = values[i+1] - values[i]
	}
	return diffs
}

func sumAnchorScores(values []layoutAnchor) float64 {
	total := 0.0
	for _, value := range values {
		total += value.Score
	}
	return total
}

func coefficientOfVariation(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	if mean == 0 {
		return 1
	}
	variance := 0.0
	for _, value := range values {
		delta := value - mean
		variance += delta * delta
	}
	return math.Sqrt(variance/float64(len(values))) / mean
}

func clusterAnchorRows(anchors []layoutAnchor, threshold float64) [][]layoutAnchor {
	if len(anchors) == 0 {
		return nil
	}
	ordered := append([]layoutAnchor(nil), anchors...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Y < ordered[j].Y })
	rows := [][]layoutAnchor{{ordered[0]}}
	for _, anchor := range ordered[1:] {
		last := rows[len(rows)-1]
		if anchor.Y-last[len(last)-1].Y <= threshold {
			rows[len(rows)-1] = append(last, anchor)
		} else {
			rows = append(rows, []layoutAnchor{anchor})
		}
	}
	return rows
}

func rowY(row []layoutAnchor) []float64 {
	values := make([]float64, len(row))
	for i, anchor := range row {
		values[i] = anchor.Y
	}
	return values
}

func spread(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	minValue, maxValue := values[0], values[0]
	for _, value := range values[1:] {
		if value < minValue {
			minValue = value
		}
		if value > maxValue {
			maxValue = value
		}
	}
	return maxValue - minValue
}

func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sortedValues := append([]float64(nil), values...)
	sort.Float64s(sortedValues)
	return sortedValues[len(sortedValues)/2]
}
