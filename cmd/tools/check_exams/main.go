package main

import (
	"context"
	"fmt"

	"path/filepath"
	"sort"
	"strings"

	"GoT0GenPaper/internal/pipeline/parser"
)

func main() {
	roots := []struct {
		name string
		path string
	}{
		{"408", "data/exams/csgraduates/408"},
		{"politics", "data/exams/csgraduates/politics"},
		{"math1", "data/exams/csgraduates/math/math1"},
		{"math2", "data/exams/csgraduates/math/math2"},
		{"math3", "data/exams/csgraduates/math/math3"},
		{"english1", "data/exams/csgraduates/english/english1"},
		{"english2", "data/exams/csgraduates/english/english2"},
	}

	for _, r := range roots {
		files, _ := filepath.Glob(filepath.Join(r.path, "*.md"))
		if len(files) == 0 {
			fmt.Printf("\n===== %s: no files in %s =====\n", r.name, r.path)
			continue
		}
		sort.Strings(files)
		fmt.Printf("\n===== %s (%d files) =====\n", r.name, len(files))
		for _, f := range files {
			p := parser.New(nil)
			res, err := p.Parse(context.Background(), f)
			if err != nil {
				fmt.Printf("  ERROR %s: %v\n", filepath.Base(f), err)
				continue
			}
			totalScore := 0.0
			typeCounts := make(map[string]int)
			typeScores := make(map[string]float64)
			emptyStem := 0
			emptyAnswer := 0
			zeroScore := 0
			for _, q := range res.Questions {
				totalScore += q.Score
				typeCounts[string(q.Type)]++
				typeScores[string(q.Type)] += q.Score
				if q.Stem == "" {
					emptyStem++
				}
				if q.Answer == "" {
					emptyAnswer++
				}
				if q.Score == 0 {
					zeroScore++
				}
			}
			year := strings.TrimSuffix(filepath.Base(f), ".md")
			fmt.Printf("  %s: %d q, score=%.0f", year, len(res.Questions), totalScore)
			if emptyStem > 0 {
				fmt.Printf(" [empty_stem=%d]", emptyStem)
			}
			if emptyAnswer > 0 {
				fmt.Printf(" [empty_answer=%d]", emptyAnswer)
			}
			if zeroScore > 0 {
				fmt.Printf(" [zero_score=%d]", zeroScore)
			}
			fmt.Println()
		}
	}
}
