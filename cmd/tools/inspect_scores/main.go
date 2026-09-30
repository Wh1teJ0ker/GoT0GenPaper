package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/parser"
)

func main() {
	exams := []struct {
		subject string
		path    string
	}{
		{"math2", "data/exams/csgraduates/math/math2/2018.md"},
		{"math3", "data/exams/csgraduates/math/math3/2008.md"},
		{"math3", "data/exams/csgraduates/math/math3/2009.md"},
		{"math3", "data/exams/csgraduates/math/math3/2012.md"},
		{"math3", "data/exams/csgraduates/math/math3/2017.md"},
	}

	for _, e := range exams {
		abs, _ := filepath.Abs(e.path)
		p := parser.New(nil)
		res, err := p.Parse(context.Background(), abs)
		if err != nil {
			fmt.Printf("Error parsing %s: %v\n", e.path, err)
			continue
		}
		total := 0.0
		fmt.Printf("\n===== %s (total questions=%d) =====\n", e.path, len(res.Questions))
		for _, q := range res.Questions {
			typeName := ""
			switch q.Type {
			case models.TypeChoice:
				typeName = "Choice"
			case models.TypeFillBlank:
				typeName = "Fill"
			case models.TypeMajor:
				typeName = "Major"
			case models.TypeMultiChoice:
				typeName = "Multi"
			}
			total += q.Score
			stem := strings.ReplaceAll(q.Stem, "\n", " ")
			if len(stem) > 80 {
				stem = stem[:80] + "..."
			}
			fmt.Printf("  [%-6s] score=%5.1f  %s\n", typeName, q.Score, stem)
		}
		fmt.Printf("  TOTAL = %.1f\n", total)
		_ = os.Stdout
	}
}
