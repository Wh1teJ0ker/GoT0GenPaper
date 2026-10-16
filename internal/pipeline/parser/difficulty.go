package parser

import (
	"sort"
	"strings"

	"GoT0GenPaper/internal/models"
)

// estimatedDifficultyConfidence marks a difficulty value that came from the
// parser's default rather than a real annotation. Values at or below it are
// replaced by the corpus-relative estimate below.
const estimatedDifficultyConfidence = 0.3

// relativeDifficultyConfidence is attached to a corpus-relative estimate so a
// later real annotation (which reports a higher confidence) still wins.
const relativeDifficultyConfidence = 0.35

// relativeDifficultyCuts returns the cumulative easy/medium cut points of a
// subject's exam design. Every subject uses the balanced 30/50/20 layout except
// Math II, whose papers are designed hard-biased (10/45/45).
func relativeDifficultyCuts(subject string) (easyCut, mediumCut float64) {
	switch strings.ToLower(strings.TrimSpace(subject)) {
	case "math2", "数学二":
		return 0.10, 0.55
	default:
		return 0.30, 0.80
	}
}

// AssignRelativeDifficulty gives historical questions a difficulty estimate
// derived from the corpus itself.
//
// The bundled corpora carry no difficulty labels, so without this step every
// historical question would be "medium". Composition could then never build a
// paper whose easy/medium/hard histogram matches the blueprint, and whole-paper
// validation could never pass. The estimate is relative rather than
// psychometric:
//
//   - questions are ranked by score (a 12-point 综合题 is harder than a
//     2-point 单选) and by topic rarity (a topic tested once in eighteen years
//     is harder than one tested every year);
//   - the ranking is cut into bands using the subject's design distribution;
//   - every occurrence of a knowledge-point key receives the same value, so the
//     composer's deduplicated pool keeps one stable band per topic.
//
// Questions that already carry a real annotation (confidence above the parser
// default) are left untouched.
func AssignRelativeDifficulty(questions []models.PastQuestion) []models.PastQuestion {
	type keyStats struct {
		scoreSum float64
		count    int
	}
	bySubject := make(map[string]map[string]*keyStats)
	for _, q := range questions {
		if q.DifficultyConfidence > estimatedDifficultyConfidence {
			continue
		}
		subject := strings.TrimSpace(q.Subject)
		key := difficultyKey(q)
		if bySubject[subject] == nil {
			bySubject[subject] = make(map[string]*keyStats)
		}
		stats := bySubject[subject][key]
		if stats == nil {
			stats = &keyStats{}
			bySubject[subject][key] = stats
		}
		stats.scoreSum += q.Score
		stats.count++
	}

	estimated := make(map[string]float64)
	for subject, keys := range bySubject {
		keyNames := make([]string, 0, len(keys))
		for key := range keys {
			keyNames = append(keyNames, key)
		}
		sort.Strings(keyNames)
		scores := make([]float64, len(keyNames))
		frequencies := make([]float64, len(keyNames))
		for i, key := range keyNames {
			stats := keys[key]
			scores[i] = stats.scoreSum / float64(stats.count)
			frequencies[i] = float64(stats.count)
		}
		scoreRanks := midRankPercentiles(scores)
		frequencyRanks := midRankPercentiles(frequencies)
		composite := make([]float64, len(keyNames))
		for i := range keyNames {
			composite[i] = 0.6*scoreRanks[i] + 0.4*(1-frequencyRanks[i])
		}
		order := make([]int, len(keyNames))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool {
			left, right := composite[order[a]], composite[order[b]]
			if left != right {
				return left > right
			}
			return keyNames[order[a]] < keyNames[order[b]]
		})
		easyCut, mediumCut := relativeDifficultyCuts(subject)
		for rank, index := range order {
			// order is sorted by composite descending, so the first entry is the
			// hardest topic: invert the rank to keep position ascending in
			// difficulty.
			position := 1 - (float64(rank)+0.5)/float64(len(order))
			estimated[subject+"\x00"+keyNames[index]] = relativeDifficultyValue(position, easyCut, mediumCut)
		}
	}

	for i := range questions {
		q := &questions[i]
		if q.DifficultyConfidence > estimatedDifficultyConfidence {
			continue
		}
		value, ok := estimated[strings.TrimSpace(q.Subject)+"\x00"+difficultyKey(*q)]
		if !ok {
			continue
		}
		q.Difficulty = value
		q.DifficultyConfidence = relativeDifficultyConfidence
	}
	return questions
}

func difficultyKey(q models.PastQuestion) string {
	return string(q.Type) + "|" + strings.Join(q.Points, "|")
}

// relativeDifficultyValue maps a rank position in [0,1] to a difficulty inside
// the band that position falls in.
func relativeDifficultyValue(position, easyCut, mediumCut float64) float64 {
	switch {
	case position < easyCut:
		return 0.20 + 0.15*(position/easyCut)
	case position < mediumCut:
		return 0.35 + 0.30*((position-easyCut)/(mediumCut-easyCut))
	default:
		return 0.65 + 0.15*((position-mediumCut)/(1-mediumCut))
	}
}

// midRankPercentiles returns the mid-rank percentile of each value in [0,1].
// Tied values share one percentile, which keeps the assignment deterministic.
func midRankPercentiles(values []float64) []float64 {
	order := make([]int, len(values))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return values[order[a]] < values[order[b]] })
	out := make([]float64, len(values))
	for i := 0; i < len(order); {
		j := i
		for j+1 < len(order) && values[order[j+1]] == values[order[i]] {
			j++
		}
		percentile := ((float64(i)+float64(j))/2 + 0.5) / float64(len(order))
		for k := i; k <= j; k++ {
			out[order[k]] = percentile
		}
		i = j + 1
	}
	return out
}
