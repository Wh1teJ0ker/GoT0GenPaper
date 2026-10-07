// Command render_sample builds a printable 408 exam from real past-exam content
// and compiles it to PDF when a TeX engine is
// available. It exercises the format pipeline end-to-end:
//
//	go run ./cmd/tools/render_sample [exam.md]   (default: 2024 408 paper)
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"GoT0GenPaper/internal/config"
	"GoT0GenPaper/internal/latex"
	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/assembler"
	"GoT0GenPaper/internal/pipeline/parser"
	"GoT0GenPaper/internal/storage"
)

func main() {
	source := "data/exams/csgraduates/408/2024.md"
	if len(os.Args) > 1 {
		source = os.Args[1]
	}
	outDir := "output/render_sample"

	cfg := config.Default()
	cfg.Paths.DataDir = "data"
	cfg.Paths.OutputDir = outDir
	cfg.Paths.Templates = "templates"
	store, err := storage.New(cfg.Paths.DataDir, cfg.Paths.OutputDir, cfg.Paths.Templates)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}

	p := parser.New(nil)
	res, err := p.Parse(context.Background(), source)
	if err != nil {
		log.Fatalf("parse: %v", err)
	}
	fmt.Printf("parsed %d questions from %s\n", len(res.Questions), source)

	// Map parsed questions straight into a paper (real stems/options/answers
	// give a faithful visual sample of the format pipeline).
	paper := models.ExamPaper{
		ID:         "sample",
		Subject:    "408",
		TotalScore: 150,
	}
	total := 0.0
	for _, pq := range res.Questions {
		total += pq.Score
		paper.Questions = append(paper.Questions, models.GeneratedQuestion{
			ID:         "q_" + pq.ID,
			SpecID:     "spec_" + pq.ID,
			Stem:       pq.Stem,
			Options:    pq.Options,
			Answer:     pq.Answer,
			Type:       pq.Type,
			Points:     pq.Points,
			Score:      pq.Score,
			Difficulty: 0.5,
		})
	}
	paper.TotalScore = total
	// Double-check the choice section intro assumption: uniform per-question
	// scores come straight from the parsed annotations.

	asm := assembler.New()
	out, err := asm.Assemble(context.Background(), paper)
	if err != nil {
		log.Fatalf("assemble: %v", err)
	}
	for name, content := range map[string]string{
		"exam.tex": out.ExamLaTeX,
	} {
		path := filepath.Join(outDir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			log.Fatalf("write %s: %v", path, err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", path, len(content))
	}

	engine, err := latex.FindEngine()
	if err != nil {
		fmt.Println("no TeX engine found — tex files written, PDF skipped")
		return
	}
	fmt.Println("engine:", engine)
	compiled, err := latex.CompileTex(context.Background(), engine, filepath.Join(outDir, "exam.tex"))
	if err != nil {
		fmt.Printf("✗ exam.tex: %v\n", err)
		if compiled != nil {
			fmt.Println(compiled.Log)
		}
		return
	}
	fmt.Printf("✓ exam.tex → %s (%s)\n", compiled.PDF, compiled.Duration)
	_ = store
}
