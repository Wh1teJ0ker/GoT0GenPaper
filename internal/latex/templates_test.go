package latex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func copyTemplatesForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	entries, err := os.ReadDir(defaultTemplateDir())
	if err != nil {
		t.Fatalf("read default template directory: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".tmpl" {
			continue
		}
		source := filepath.Join(defaultTemplateDir(), entry.Name())
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read template %s: %v", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dir, entry.Name()), content, 0o644); err != nil {
			t.Fatalf("copy template %s: %v", entry.Name(), err)
		}
	}
	return dir
}

func useTestTemplateDir(t *testing.T, dir string) {
	t.Helper()
	templateMu.RLock()
	oldDir := templateDir
	templateMu.RUnlock()
	if err := SetTemplateDir(dir); err != nil {
		t.Fatalf("set test template directory: %v", err)
	}
	t.Cleanup(func() {
		if err := SetTemplateDir(oldDir); err != nil {
			t.Errorf("restore template directory: %v", err)
		}
	})
}

func TestSetTemplateDirLoadsExternalDirectory(t *testing.T) {
	dir := copyTemplatesForTest(t)
	useTestTemplateDir(t, dir)

	if got := QuestionSkeleton("choice"); !strings.Contains(got, "\\begin{choices}") {
		t.Fatalf("loaded default copy did not render choice skeleton: %q", got)
	}
}

func TestSetTemplateDirRejectsIncompleteDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "question_choice.tmpl"), []byte(`{{%define "question_choice"%}}only one{{%end%}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetTemplateDir(dir); err == nil || !strings.Contains(err.Error(), "missing required templates") {
		t.Fatalf("incomplete template directory should fail clearly, got %v", err)
	}
}

func TestQuestionSkeletonUsesExternalTemplate(t *testing.T) {
	dir := copyTemplatesForTest(t)
	custom := `{{%define "question_choice"%}}CUSTOM-SKELETON{{%end%}}`
	if err := os.WriteFile(filepath.Join(dir, "question_choice.tmpl"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	useTestTemplateDir(t, dir)

	if got := QuestionSkeleton("choice"); got != "CUSTOM-SKELETON" {
		t.Fatalf("QuestionSkeleton did not use external template: %q", got)
	}
}

func TestCustomTemplateChangesExamOutput(t *testing.T) {
	dir := copyTemplatesForTest(t)
	custom := `{{%define "choice_options"%}}{{%range .%}}{{%end%}}{{%end%}}
{{%define "choice_question"%}}CUSTOM-QUESTION {{% .Number %}}: {{% .Stem %}}{{%end%}}`
	if err := os.WriteFile(filepath.Join(dir, "choice_question.tmpl"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	useTestTemplateDir(t, dir)

	doc := ExamPaper(ExamMeta{Subject: "测试科目"}, []ExamSection{{
		Spec:      SectionSpec{Title: "一、选择题", Detail: "第 1 小题"},
		Questions: []string{ChoiceQuestion(1, "自定义题干", []string{"甲", "乙", "丙", "丁"})},
	}}, nil, FooterBranding{})
	if !strings.Contains(doc, "CUSTOM-QUESTION 1: 自定义题干") {
		t.Fatalf("custom question template did not affect exam output: %q", doc)
	}
}

func TestDecideOptionLayout(t *testing.T) {
	cases := []struct {
		opts []string
		want optionLayout
	}{
		{[]string{"栈", "队列", "树", "图"}, optionsInline4},
		{[]string{"90 ns", "80 ns", "70 ns", "60 ns"}, optionsInline4},
		{[]string{"指令操作码的译码结果", "指令和数据的寻址方式", "指令周期的不同阶段", "指令和数据所在的存储单元"}, optionsInline2},
		{[]string{"x=000007FH，y=FFF9H，z=0000076H", "x=000007FH，y=FFF9H，z=FFFF0076H"}, optionsStacked},
	}
	for _, c := range cases {
		if got := decideOptionLayout(c.opts); got != c.want {
			t.Errorf("decideOptionLayout(%v) = %d, want %d", c.opts, got, c.want)
		}
	}
}

func TestRenderOptionsLayouts(t *testing.T) {
	// 4-in-row for short options.
	out := renderOptions([]string{"2", "3", "4", "5"})
	if strings.Count(out, "\\quad ") != 3 {
		t.Errorf("inline4 should join with \\quad, got %q", out)
	}
	// 2-per-row for medium options.
	out = renderOptions([]string{"指令操作码的译码结果", "指令和数据的寻址方式", "指令周期的不同阶段", "指令和数据所在的存储单元"})
	if strings.Count(out, "\n\n") != 1 || strings.Contains(out, "译码结果\\quad 寻址") {
		t.Errorf("inline2 should break after two options, got %q", out)
	}
	// One per line for long options.
	out = renderOptions([]string{"建立建立在 TCP 之上的控制连接连接", "建立在 TCP 之上的数据数据连接连接xx"})
	if strings.Contains(out, "\\quad") {
		t.Errorf("stacked options must not join with \\quad, got %q", out)
	}
}

func TestEscapeLaTeX(t *testing.T) {
	cases := []struct{ in, want string }{
		{"100% 正确", `100\% 正确`},
		{"a & b # c _ d", `a \& b \# c \_ d`},
		{"$x_1 + y_2$ 保持", `$x_1 + y_2$ 保持`}, // math untouched
		{`\(x^2 + y^2\) 且 α∈Ω`, `\(x^2 + y^2\) 且 \ensuremath{\alpha}\ensuremath{\in}\ensuremath{\Omega}`},
		{`√2 和 ∛x`, `\ensuremath{\sqrt{2}} 和 \ensuremath{\sqrt[3]{x}}`},
		{"复述 ( ): 正确", `复述 ( ): 正确`},
		{"$а_1 + Р_2$", `$a_1 + P_2$`}, // Cyrillic lookalikes become TeX-safe Latin variables.
		{"$$\n\n\\begin{cases}\nx=1\n\\end{cases}\n\n$$", "$$\n\\begin{cases}\nx=1\n\\end{cases}\n$$"},
		{`填空：\_\_\_\_\_\_。`, `填空：\underline{\quad\quad}。`},
		{`填空：______。`, `填空：\underline{\quad\quad}。`},
		{`$\lim_{x\to 0⁻} f(x)$ 与 $0⁺$`, `$\lim_{x\to 0^-} f(x)$ 与 $0^+$`},
		{`文本 0⁻`, `文本 0\textsuperscript{-}`},
	}
	for _, c := range cases {
		if got := escapeLaTeX(c.in); got != c.want {
			t.Errorf("escapeLaTeX(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEscapeLaTeXKeepsAllMathDelimiters(t *testing.T) {
	cases := []string{
		`答案 \(x^2\) 与 \[\int_0^1 x\,dx\]。`,
		`答案 $$x^2+y^2$$。`,
	}
	for _, in := range cases {
		out := escapeLaTeX(in)
		for _, marker := range []string{`\(`, `\)`, `\[`, `\]`} {
			if strings.Contains(in, marker) && !strings.Contains(out, marker) {
				t.Errorf("escapeLaTeX(%q) lost %q: %q", in, marker, out)
			}
		}
		if strings.Contains(out, `\textasciicircum`) {
			t.Errorf("math caret was escaped as text: %q", out)
		}
	}
}

func TestInspectTeX(t *testing.T) {
	good := `\documentclass{article}
\begin{document}
\(x^2\) and \[\begin{array}{c}a\\b\end{array}\]
\end{document}`
	if diagnostics := InspectTeX(good); len(diagnostics) != 0 {
		t.Fatalf("valid TeX reported diagnostics: %+v", diagnostics)
	}
	bad := `\documentclass{article}
\begin{document}
\(x^2
\begin{itemize}
\item text
\end{document}`
	diagnostics := InspectTeX(bad)
	if !hasDiagnosticKind(diagnostics, "unclosed-math") || !hasDiagnosticKind(diagnostics, "unclosed-environment") {
		t.Fatalf("expected unmatched math/environment diagnostics, got %+v", diagnostics)
	}
}

func TestInspectTeXLog(t *testing.T) {
	log := `Missing character: There is no α (U+03B1) in font foo.
	Overfull \hbox (12.0pt too wide) in paragraph at lines 4--5`
	diagnostics := InspectTeXLog(log)
	if !hasDiagnosticKind(diagnostics, "missing-character") || !hasDiagnosticKind(diagnostics, "overfull-hbox") {
		t.Fatalf("expected compiler diagnostics, got %+v", diagnostics)
	}
}

func hasDiagnosticKind(diagnostics []TexDiagnostic, kind string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Kind == kind {
			return true
		}
	}
	return false
}

func TestCleanMarkdownStem(t *testing.T) {
	in := "题干内容 `code` 与 **加粗**。\n- A. 选项一\n- B. 选项二\n后续行"
	out := CleanMarkdownStem(in)
	if strings.Contains(out, "- A.") || strings.Contains(out, "`") || strings.Contains(out, "**") {
		t.Errorf("markdown markers survived cleaning: %q", out)
	}
	if !strings.Contains(out, "后续行") {
		t.Errorf("non-bullet lines must survive: %q", out)
	}
}

// TestEscapeLaTeXDisplayMath pins the \[…\] handling: array & and _ must
// stay raw inside display math, and CleanMarkdownStem must not fold math
// lines as fragments (a blank line inside \[…\] is a LaTeX error — this is
// exactly how a real LLM-generated adjacency table broke the exam compile).
func TestEscapeLaTeXDisplayMath(t *testing.T) {
	stem := "已知无向图 \\(G\\) 的邻接多重表如下所示。各顶点的 firstedge 依次为：\n\\[\n\\begin{array}{c|c|c|c|c}\n\\text{边结点} & \\text{ivex} & \\text{ilink} & \\text{jvex} & \\text{jlink}\\\\\n\\hline\nE0 & 0 & E3 & 1 & E1\\\\\nE2 & 2 & E4 & 3 & E3\n\\end{array}\n\\]"
	cleaned := CleanMarkdownStem(stem)
	escaped := escapeLaTeX(cleaned)
	if strings.Count(escaped, `\[`) != 1 || strings.Count(escaped, `\]`) != 1 {
		t.Errorf("display-math delimiters damaged: %q", escaped)
	}
	if strings.Contains(escaped, `\&`) && strings.Contains(escaped, `\begin{array}`) {
		t.Errorf("& was escaped inside display math: %q", escaped)
	}
	if strings.Contains(escaped, "\\n\\n\\\\[") || strings.Contains(escaped, "\\\n\n\\[") {
		t.Errorf("blank line introduced around math delimiters: %q", escaped)
	}

	// The full stem must compile when embedded in an exam document.
	secs := []ExamSection{{
		Spec:      SectionSpec{Title: "一、单项选择题", Detail: "第 1 小题，共 2 分"},
		Questions: []string{ChoiceQuestion(1, cleaned, []string{"A", "B", "C", "D"})},
	}}
	doc := ExamPaper(ExamMeta{Subject: "408"}, secs, nil, FooterBranding{})
	dir := t.TempDir()
	tex := dir + "/math_stem.tex"
	if err := osWrite(tex, doc); err != nil {
		t.Fatal(err)
	}
	engine, err := FindEngine()
	if err != nil {
		t.Skip("no TeX engine:", err)
	}
	if res, cerr := CompileTex(context.Background(), engine, tex); cerr != nil {
		t.Errorf("stem with display math failed to compile: %v\n%s", cerr, res.Log)
	}
}

func osWrite(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// TestCleanMarkdownStemLiteralNewline: LLM answers carry literal `\n`
// two-char sequences — a bare \n becomes a real newline, but LaTeX commands
// starting with "n" (\neq, \nu, \noindent) must survive.
func TestCleanMarkdownStemLiteralNewline(t *testing.T) {
	in := "第一步：计算入度。\n第二步：输出顶点 $A$，则 $\\neq$ 与 $\\nu$ 不受影响。\\noindent 尾部"
	out := CleanMarkdownStem(in)
	if !strings.Contains(out, "\n第二步") {
		t.Errorf("literal \\n not converted: %q", out)
	}
	for _, keep := range []string{`\neq`, `\nu`, `\noindent`} {
		if !strings.Contains(out, keep) {
			t.Errorf("LaTeX command %q corrupted: %q", keep, out)
		}
	}
}

func TestRenderOptionsStripsLLMLabels(t *testing.T) {
	out := renderOptions([]string{"A. 2", "B、3", "C）4", "5"})
	if strings.Contains(out, "A．A") || strings.Contains(out, "B．B") || strings.Contains(out, "C．C") {
		t.Errorf("double option label survived: %q", out)
	}
	for _, want := range []string{"A．2", "B．3", "C．4", "D．5"} {
		if !strings.Contains(out, want) {
			t.Errorf("options rendered wrong, missing %q: %q", want, out)
		}
	}
}

func TestExamPaperStructure(t *testing.T) {
	secs := []ExamSection{
		{Spec: SectionSpec{Title: "一、单项选择题", Detail: "第 1～2 小题，每小题 2 分，共 4 分"},
			Questions: []string{ChoiceQuestion(1, "测试题干（ ）。", []string{"甲", "乙", "丙", "丁"})}},
		{Spec: SectionSpec{Title: "二、综合应用题", Detail: "第 2 小题，共 8 分"},
			Questions: []string{MajorQuestion(2, 8, "计算下题。\n(1) 第一问 (2) 第二问")}},
	}
	doc := ExamPaper(ExamMeta{Year: "2026", Subject: "408", FullTitle: "测试学科联考试题"}, secs, nil, FooterBranding{Left: "L", Right: "R"})
	for _, want := range []string{
		"2026 年全国硕士研究生入学统一考试",
		"一、单项选择题（第 1～2 小题，每小题 2 分，共 4 分）",
		"{\\bfseries 1．}测试题干（ ）。",
		"A．甲\\quad B．乙\\quad C．丙\\quad D．丁",
		"{\\bfseries 2．}（8 分）计算下题。",
		"\\pageref{LastPage}",
		"\\documentclass[12pt]{ctexart}",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("exam paper missing %q", want)
		}
	}
	// The second section's header must come AFTER the first section's
	// questions (interleaved, matching the printed exams).
	q1 := strings.Index(doc, "{\\bfseries 1．}")
	sec2 := strings.Index(doc, "二、综合应用题")
	if q1 == -1 || sec2 == -1 || sec2 < q1 {
		t.Errorf("section headers must interleave with questions (q1@%d, sec2@%d)", q1, sec2)
	}
}

func TestExamPaperCoverDoesNotAffectBodyPagination(t *testing.T) {
	cover := []string{"数学二模拟试卷", "中等难度"}
	doc := ExamPaper(
		ExamMeta{Year: "2026", Subject: "数学二"},
		[]ExamSection{{
			Spec:      SectionSpec{Title: "一、单项选择题", Detail: "第 1 小题，共 5 分"},
			Questions: []string{ChoiceQuestion(1, "测试题。", []string{"甲", "乙", "丙", "丁"})},
		}},
		&cover,
		FooterBranding{},
	)
	for _, want := range []string{
		"\\begin{titlepage}\n\\thispagestyle{empty}",
		"\\end{titlepage}\n\\pagenumbering{arabic}\n\\setcounter{page}{1}\n\\pagestyle{fancy}",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("cover pagination contract missing %q", want)
		}
	}
}

func TestExamPreambleCoversCommonUnicodeGlyphs(t *testing.T) {
	preamble := examPreamble(FooterBranding{}, 0)
	for _, want := range []string{
		`\newunicodechar{∼}{\ensuremath{\sim}}`,
		`\newunicodechar{Ⅰ}{\ensuremath{\mathrm{I}}}`,
		`\newunicodechar{Ⅱ}{\ensuremath{\mathrm{II}}}`,
		`\newunicodechar{Ⅲ}{\ensuremath{\mathrm{III}}}`,
		`\newunicodechar{Ⅳ}{\ensuremath{\mathrm{IV}}}`,
		`\newunicodechar{Р}{P}`,
	} {
		if !strings.Contains(preamble, want) {
			t.Errorf("preamble missing Unicode glyph mapping %q", want)
		}
	}
}

func TestAnswerSheetStructure(t *testing.T) {
	spec := AnswerSheetSpec{
		Title:               "全国硕士研究生招生考试",
		CardName:            "计算机学科专业学位联考答题卡1",
		ChoiceSection:       "一、单项选择题:1～40小题,每小题2分,共80分。",
		ChoiceNums:          []int{1, 2, 3, 4},
		FillSection:         "二、填空题:11～16小题,共30分。",
		FillNums:            []int{11},
		MajorSection:        "三、综合应用题:41～47小题,共70分。",
		MajorNums:           []int{41, 42},
		BarcodeOnFirstMajor: true,
	}
	doc := AnswerSheet(spec)
	for _, want := range []string{
		"\\color{examred}\\zihao{3}\\bfseries 计算机学科专业学位联考答题卡1",
		"准考证号(左对齐)",
		"注\\ 意\\ 事\\ 项",
		"一、单项选择题:1～40小题,每小题2分,共80分。",
		"\\bubble{A}",
		"阴影部分请勿作答或做任何标记",
		"二、填空题:11～16小题,共30分。",
		"三、综合应用题:41～47小题,共70分。",
		"\\fillbox", "{11}", "\\majorbox", "{41}", "{42}",
		"考生姓名",
		"考生信息条形码粘贴位置",
		"\\pageref{LastPage}",
		"\\AddToShipoutPictureBG",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("answer sheet missing %q", want)
		}
	}
}

func TestAnswerSheetSplitsLargeObjectiveSection(t *testing.T) {
	nums := make([]int, 41)
	for i := range nums {
		nums[i] = i + 1
	}
	doc := AnswerSheet(AnswerSheetSpec{Title: "T", CardName: "C", ChoiceSection: "选择题", ChoiceNums: nums, ChoicePageSize: 40})
	if strings.Count(doc, "\\begin{document}") != 1 || strings.Count(doc, "\\clearpage") != 1 {
		t.Fatalf("unexpected page breaks")
	}
	if !strings.Contains(doc, "本页 1～40 题") || !strings.Contains(doc, "本页 41～41 题") {
		t.Fatalf("page ranges missing")
	}
}
