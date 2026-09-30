// PDF compilation: render generated LaTeX into PDFs via a TeX engine.
//
// Engine lookup order: xelatex (full TeX Live / MacTeX / TeX Live on
// Windows) → tectonic (self-contained, auto-fetches packages). CJK output
// requires the xetex engine family, which both provide.
package latex

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CompileResult reports one LaTeX→PDF compilation.
type CompileResult struct {
	PDF         string          `json:"pdf"`    // produced PDF path ("" on failure)
	Log         string          `json:"log"`    // tail of the engine log
	Engine      string          `json:"engine"` // xelatex | tectonic
	Duration    string          `json:"duration"`
	Diagnostics []TexDiagnostic `json:"diagnostics,omitempty"`
}

// FindEngine locates a usable TeX engine, preferring xelatex.
func FindEngine() (string, error) {
	for _, name := range []string{"xelatex", "tectonic"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("未找到 TeX 引擎: 请安装 MacTeX/TeX Live (xelatex) 或 tectonic")
}

// CompileTex compiles one .tex file into its directory. Two passes run for
// xelatex so page totals (第 N 页 共 M 页) resolve; tectonic handles passes
// internally.
func CompileTex(ctx context.Context, engine, texPath string) (*CompileResult, error) {
	start := time.Now()
	texPath, err := filepath.Abs(texPath)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", texPath, err)
	}
	dir := filepath.Dir(texPath)
	base := filepath.Base(texPath)
	source, readErr := os.ReadFile(texPath)
	if readErr != nil {
		return nil, fmt.Errorf("读取 %s: %w", texPath, readErr)
	}
	sourceDiagnostics := InspectTeX(string(source))
	if hasDiagnosticError(sourceDiagnostics) {
		return &CompileResult{Engine: filepath.Base(engine), Diagnostics: sourceDiagnostics}, fmt.Errorf("LaTeX 源码检查失败: %s", firstDiagnosticMessage(sourceDiagnostics))
	}

	var cmd *exec.Cmd
	switch filepath.Base(engine) {
	case "tectonic":
		cmd = exec.CommandContext(ctx, engine, "-o", dir, "--keep-logs", texPath)
	case "xelatex":
		cmd = exec.CommandContext(ctx, engine, "-interaction=nonstopmode", "-halt-on-error",
			"-output-directory", dir, base)
	default:
		return nil, fmt.Errorf("unsupported engine %q", engine)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		logText := tailLog(dir, base, stdout.String(), stderr.String())
		return &CompileResult{
			Log:         logText,
			Engine:      filepath.Base(engine),
			Diagnostics: append(sourceDiagnostics, InspectTeXLog(logText)...),
		}, fmt.Errorf("%s 编译失败: %v", filepath.Base(engine), err)
	}

	// xelatex needs a second pass for \thepage references in the footer to
	// stabilise (共 M 页 totals).
	if filepath.Base(engine) == "xelatex" {
		cmd2 := exec.CommandContext(ctx, engine, "-interaction=nonstopmode", "-halt-on-error",
			"-output-directory", dir, base)
		cmd2.Dir = dir
		_ = cmd2.Run()
	}
	logText := readCompileLog(dir, base)
	diagnostics := append(sourceDiagnostics, InspectTeXLog(logText)...)

	pdf := strings.TrimSuffix(texPath, ".tex") + ".pdf"
	if _, err := os.Stat(pdf); err != nil {
		return &CompileResult{Engine: filepath.Base(engine), Log: logText, Diagnostics: diagnostics}, fmt.Errorf("编译后未找到 %s", pdf)
	}
	result := &CompileResult{
		PDF:         pdf,
		Log:         logText,
		Engine:      filepath.Base(engine),
		Duration:    time.Since(start).Round(time.Millisecond).String(),
		Diagnostics: diagnostics,
	}
	diagnostics = append(diagnostics, inspectPDFText(pdf)...)
	result.Diagnostics = diagnostics
	if hasDiagnosticError(diagnostics) {
		return result, fmt.Errorf("LaTeX 编译完成但存在渲染错误: %s", firstDiagnosticMessage(diagnostics))
	}
	return result, nil
}

// inspectPDFText checks the text layer emitted by the PDF engine. It cannot
// replace visual review, but it catches the most damaging silent failures:
// replacement glyphs and escaped TeX helper names leaking into the output.
func inspectPDFText(pdfPath string) []TexDiagnostic {
	pdftotext, err := exec.LookPath("pdftotext")
	if err != nil {
		return nil
	}
	cmd := exec.Command(pdftotext, "-enc", "UTF-8", pdfPath, "-")
	data, err := cmd.Output()
	if err != nil {
		return []TexDiagnostic{{Severity: "warning", Kind: "pdf-text-check", Message: fmt.Sprintf("pdftotext 检查失败: %v", err)}}
	}
	text := string(data)
	var diagnostics []TexDiagnostic
	if strings.ContainsRune(text, '\uFFFD') {
		diagnostics = append(diagnostics, TexDiagnostic{
			Severity: "error", Kind: "pdf-replacement-character",
			Message: "PDF 文本层含 U+FFFD 替换字符，至少有部分数学符号或字形未成功渲染",
		})
	}
	for _, marker := range []string{`textasciicircum`, `textasciitilde`} {
		if strings.Contains(text, marker) {
			diagnostics = append(diagnostics, TexDiagnostic{
				Severity: "warning", Kind: "pdf-raw-tex-marker",
				Message: fmt.Sprintf("PDF 文本层仍含 LaTeX 转义标记 %q，可能有数学内容被当作普通文本输出", marker),
			})
		}
	}
	return diagnostics
}

func readCompileLog(dir, base string) string {
	logPath := filepath.Join(dir, strings.TrimSuffix(base, ".tex")+".log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	return string(data)
}

func firstDiagnosticMessage(diagnostics []TexDiagnostic) string {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == "error" {
			if diagnostic.Line > 0 {
				return fmt.Sprintf("第 %d 行: %s", diagnostic.Line, diagnostic.Message)
			}
			return diagnostic.Message
		}
	}
	return "未知错误"
}

func tailLog(dir, base, stdout, stderr string) string {
	if data, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(base, ".tex")+".log")); err == nil {
		lines := strings.Split(string(data), "\n")
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		return strings.Join(lines, "\n")
	}
	out := stdout + stderr
	if len(out) > 2000 {
		out = out[len(out)-2000:]
	}
	return out
}
