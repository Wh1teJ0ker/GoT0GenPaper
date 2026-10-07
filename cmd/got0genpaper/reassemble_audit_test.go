package main

// ReassembleAudit reassembles a persisted paper through the current assembler
// and compiles the exam deliverable. It reproduces compile failures offline,
// without calling an LLM. OUTPUT_AUDIT_DIR selects the artifact directory;
// when unset it defaults to output/live.
//
//	RUN_OUTPUT_AUDIT=1 OUTPUT_AUDIT_DIR=output go test ./cmd/got0genpaper -run TestReassembleAudit -v -count=1

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
	if os.Getenv("RUN_OUTPUT_AUDIT") != "1" {
		t.Skip("set RUN_OUTPUT_AUDIT=1 to audit persisted output/live artifacts")
	}
	auditDir := os.Getenv("OUTPUT_AUDIT_DIR")
	if auditDir == "" {
		auditDir = repoPath("output", "live")
	} else if !filepath.IsAbs(auditDir) {
		auditDir = repoPath(auditDir)
	}
	data, err := os.ReadFile(filepath.Join(auditDir, "paper.json"))
	if err != nil {
		t.Skip("no output/live/paper.json — run the live pipeline first")
	}
	var paper models.ExamPaper
	if err := json.Unmarshal(data, &paper); err != nil {
		t.Fatalf("paper.json: %v", err)
	}

	a := newTestApp(t)
	a.ctx = context.Background()
	out, err := a.assemblePaper(paper)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if err := os.MkdirAll(auditDir, 0o755); err != nil {
		t.Fatalf("create audit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(auditDir, "exam.tex"), []byte(out.ExamLaTeX), 0o644); err != nil {
		t.Fatalf("write exam.tex: %v", err)
	}

	engine, err := latex.FindEngine()
	if err != nil {
		t.Skip("no TeX engine:", err)
	}
	res, cerr := latex.CompileTex(a.ctx, engine, filepath.Join(auditDir, "exam.tex"))
	if cerr != nil {
		logTail := ""
		if res != nil {
			logTail = res.Log
		}
		t.Errorf("编译 exam.tex: %v\n%s", cerr, logTail)
	} else {
		t.Logf("✓ exam.tex → %s", res.PDF)
	}
}
