// Package gnn implements Stage [2]: GNN 预测层.
//
// Uses HGT (heterogeneous graph transformer) to produce:
//   - per-point×题型×认知层 weight matrix (node-level prediction)
//   - knowledge-point co-occurrence subgraphs (edge-level prediction)
//
// GNN runs offline once; CP-SAT consumes the scored candidates.
// MVP uses statistical priors from real exams instead; V2 swaps in HGT.
package gnn

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"GoT0GenPaper/internal/models"
)

// Predictor is the interface for both MVP (statistical) and V2 (HGT) backends.
type Predictor interface {
	// PredictWeights returns the per-point×题型×认知层 weight matrix.
	// Outer key = knowledge-point ID; inner key = "题型_认知层" composite;
	// value = occurrence probability in [0,1].
	PredictWeights(ctx context.Context, kg interface{}) (map[string]map[string]float64, error)
	// PredictCooccurrence returns point-pair co-occurrence probabilities.
	// Outer key = point A; inner key = point B; value = Jaccard co-occurrence in [0,1].
	PredictCooccurrence(ctx context.Context, kg interface{}) (map[string]map[string]float64, error)
}

// StatisticalPredictor is the MVP backend: derives priors from real-exam statistics.
//
// It replaces the V2 HGT model with frequency-based estimates:
//   - Weight matrix: normalised occurrence counts of (point, type, cognitive) tuples.
//   - Co-occurrence: Jaccard similarity of point pairs across all questions.
//
// This is a pure-function statistical estimator — no training, no embeddings.
type StatisticalPredictor struct {
	Questions []models.PastQuestion
}

// New creates the MVP statistical predictor.
func New(qs []models.PastQuestion) *StatisticalPredictor {
	return &StatisticalPredictor{Questions: qs}
}

// compositeKey builds the "题型_认知层" inner key for the weight matrix.
func compositeKey(t models.QuestionType, c models.CognitiveLevel) string {
	return fmt.Sprintf("%s_%s", t, c)
}

// PredictWeights derives occurrence-frequency weights per knowledge point.
//
// For each knowledge point p, the weight w(p, type, cognitive) is:
//
//	count(p appears in a question with that type+cognitive) / count(p appears in any question)
//
// This gives a probability distribution over (type, cognitive) for each point,
// which CP-SAT uses as a prior when selecting spec entries.
func (s *StatisticalPredictor) PredictWeights(ctx context.Context, kg interface{}) (map[string]map[string]float64, error) {
	if len(s.Questions) == 0 {
		return make(map[string]map[string]float64), nil
	}

	// totalPerPoint counts how many questions reference each point (any type/cog).
	totalPerPoint := make(map[string]int)
	// pairCount[point][compositeKey] = co-occurrence of point with that type+cog.
	pairCount := make(map[string]map[string]int)

	for _, q := range s.Questions {
		key := compositeKey(q.Type, q.Cognitive)
		for _, pt := range q.Points {
			totalPerPoint[pt]++
			if pairCount[pt] == nil {
				pairCount[pt] = make(map[string]int)
			}
			pairCount[pt][key]++
		}
	}

	weights := make(map[string]map[string]float64)
	for pt, total := range totalPerPoint {
		if total == 0 {
			continue
		}
		weights[pt] = make(map[string]float64)
		for key, count := range pairCount[pt] {
			weights[pt][key] = float64(count) / float64(total)
		}
	}

	return weights, nil
}

// PredictCooccurrence derives point-pair co-occurrence using Jaccard similarity.
//
// For points A and B:
//
//	J(A,B) = |questions testing both A and B| / |questions testing A or B|
//
// This is used by CP-SAT to prune candidate spec space: point pairs with
// very low co-occurrence are unlikely to appear together in a generated question.
func (s *StatisticalPredictor) PredictCooccurrence(ctx context.Context, kg interface{}) (map[string]map[string]float64, error) {
	if len(s.Questions) == 0 {
		return make(map[string]map[string]float64), nil
	}

	// pointSets[pt] = set of question indices that test pt.
	pointSets := make(map[string]map[int]bool)
	for i, q := range s.Questions {
		for _, pt := range q.Points {
			if pointSets[pt] == nil {
				pointSets[pt] = make(map[int]bool)
			}
			pointSets[pt][i] = true
		}
	}

	// Collect all unique point IDs, sorted for deterministic output.
	pointIDs := make([]string, 0, len(pointSets))
	for pt := range pointSets {
		pointIDs = append(pointIDs, pt)
	}
	sort.Strings(pointIDs)

	coocc := make(map[string]map[string]float64)
	for i, a := range pointIDs {
		coocc[a] = make(map[string]float64)
		for _, b := range pointIDs[i+1:] {
			intersection := 0
			setA := pointSets[a]
			setB := pointSets[b]
			// Iterate over the smaller set for efficiency.
			if len(setA) > len(setB) {
				setA, setB = setB, setA
			}
			for idx := range setA {
				if setB[idx] {
					intersection++
				}
			}
			union := len(setA) + len(setB) - intersection
			if union == 0 {
				continue
			}
			jaccard := float64(intersection) / float64(union)
			coocc[a][b] = jaccard
			// Symmetric entry.
			if coocc[b] == nil {
				coocc[b] = make(map[string]float64)
			}
			coocc[b][a] = jaccard
		}
	}

	return coocc, nil
}

// TopCooccurrence returns the top-k co-occurring points for a given point,
// sorted by Jaccard descending. Useful for candidate pruning in CP-SAT.
func TopCooccurrence(coocc map[string]map[string]float64, point string, k int) []PointScore {
	scores := make([]PointScore, 0, len(coocc[point]))
	for other, score := range coocc[point] {
		scores = append(scores, PointScore{Point: other, Score: score})
	}
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Score > scores[j].Score
	})
	if k > 0 && k < len(scores) {
		scores = scores[:k]
	}
	return scores
}

// PointScore pairs a knowledge-point ID with a predicted score.
type PointScore struct {
	Point string  `json:"point"`
	Score float64 `json:"score"`
}

// WeightFor looks up the weight of a (point, type, cognitive) tuple.
// Returns 0 if the point or composite key is unknown.
func WeightFor(weights map[string]map[string]float64, pt string, t models.QuestionType, c models.CognitiveLevel) float64 {
	inner, ok := weights[pt]
	if !ok {
		return 0
	}
	return inner[compositeKey(t, c)]
}

// AllPoints returns a sorted list of all knowledge-point IDs in the weight matrix.
func AllPoints(weights map[string]map[string]float64) []string {
	pts := make([]string, 0, len(weights))
	for pt := range weights {
		pts = append(pts, pt)
	}
	sort.Strings(pts)
	return pts
}

// FormatWeights returns a human-readable summary of the weight matrix for debugging.
func FormatWeights(weights map[string]map[string]float64) string {
	var b strings.Builder
	for _, pt := range AllPoints(weights) {
		b.WriteString(pt)
		b.WriteString(": ")
		inner := weights[pt]
		keys := make([]string, 0, len(inner))
		for k := range inner {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("%s=%.3f", k, inner[k]))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// HGTPredictor is the V2 backend (PyG HGT via Python subprocess or ONNX).
// Placeholder until V2.
type HGTPredictor struct{}

func (h *HGTPredictor) PredictWeights(ctx context.Context, kg interface{}) (map[string]map[string]float64, error) {
	return nil, nil // V2
}

func (h *HGTPredictor) PredictCooccurrence(ctx context.Context, kg interface{}) (map[string]map[string]float64, error) {
	return nil, nil // V2
}

// Clamp ensures a score is within [0, 1]; helper for downstream normalisation.
func Clamp(x float64) float64 {
	return math.Max(0, math.Min(1, x))
}
