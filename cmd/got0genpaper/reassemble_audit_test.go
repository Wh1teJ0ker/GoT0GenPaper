package main

// ReassembleAudit reassembles a persisted paper (output/live/paper.json)
// through the current assembler + compiles the three deliverables. It exists
// to reproduce live-run compile failures offline, without re-paying the LLM
// cost. Skipped when the artifact is absent.
//
//	go test ./cmd/got0genpaper -run TestReassembleAudit -v

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"GoT0GenPaper/internal/latex"
	"GoT0GenPaper/internal/models"
)

func TestReassembleAudit(t *testing.T) {
	liveDir := repoPath("output", "live")
	data, err := os.ReadFile(filepath.Join(liveDir, "paper.json"))
	if err != nil {
		t.Skip("no output/live/paper.json — run the live pipeline first")
	}
	var paper models.ExamPaper
	if err := json.Unmarshal(data, &paper); err != nil {
		t.Fatalf("paper.json: %v", err)
	}

	a := newTestAPI(t)
	a.ctx = context.Background()
	out, err := a.AssemblePaper(paper)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	_ = os.MkdirAll(liveDir, 0o755)
	_ = os.WriteFile(filepath.Join(liveDir, "exam.tex"), []byte(out.ExamLaTeX), 0o644)
	_ = os.WriteFile(filepath.Join(liveDir, "answers.tex"), []byte(out.AnswerLaTeX), 0o644)
	_ = os.WriteFile(filepath.Join(liveDir, "answer_sheet.tex"), []byte(out.AnswerSheetTeX), 0o644)

	engine, err := latex.FindEngine()
	if err != nil {
		t.Skip("no TeX engine:", err)
	}
	for _, name := range []string{"exam.tex", "answers.tex", "answer_sheet.tex"} {
		res, cerr := latex.CompileTex(a.ctx, engine, filepath.Join(liveDir, name))
		if cerr != nil {
			logTail := ""
			if res != nil {
				logTail = res.Log
			}
			t.Errorf("编译 %s: %v\n%s", name, cerr, logTail)
			continue
		}
		t.Logf("✓ %s → %s", name, res.PDF)
	}
}
