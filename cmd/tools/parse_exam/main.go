package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"GoT0GenPaper/internal/pipeline/parser"
)

func main() {
	path := "data/exams/csgraduates/math/math1/2024.md"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	p := parser.New(nil)
	res, err := p.Parse(context.Background(), path)
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		return
	}
	fmt.Printf("Total questions: %d\n\n", len(res.Questions))

	sort.Slice(res.Questions, func(i, j int) bool {
		return res.Questions[i].ID < res.Questions[j].ID
	})

	totalScore := 0.0
	typeScores := make(map[string]float64)
	typeCounts := make(map[string]int)
	for _, q := range res.Questions {
		totalScore += q.Score
		typeScores[string(q.Type)] += q.Score
		typeCounts[string(q.Type)]++
		fmt.Printf("  %s [%s] score=%.2f stem_len=%d answer_len=%d\n",
			q.ID, q.Type, q.Score, len(q.Stem), len(q.Answer))
	}
	fmt.Printf("\nTotal score: %.2f\n", totalScore)
	for t, s := range typeScores {
		fmt.Printf("  %s: %d questions, %.2f points\n", t, typeCounts[t], s)
	}
	_ = strings.Contains
}
