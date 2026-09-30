// Package parser implements Stage [1]: 真题解析层.
//
// It ingests past-exam papers (Markdown with YAML front matter), splits them into
// individual questions, extracts structured annotations (答案/标签/解析), and
// optionally uses LLM to annotate the four dimensions (知识点子图, 题型, 难度, 认知层).
//
// Real data format (data/exams/csgraduates/):
//   - YAML front matter: title, source
//   - ### 选择题 / ### 填空题 / ### 解答题 / ### 分析题 (section headers)
//   - #### 数据结构 / #### Text 1 (category subsections)
//   - ##### N (individual question headers, N = question number)
//   - - A. / - B. / - C. / - D. (choice options)
//   - **【答案】X** (answer)
//   - 【标签】知识点1,知识点2 (knowledge point tags)
//   - **【解析】** / **【解答】** (explanation boundary)
package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"GoT0GenPaper/internal/latex"
	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
)

// Parser orchestrates the real-exam analysis pipeline.
type Parser struct {
	LLM *llm.Client
}

// New creates a Parser.
func New(llmClient *llm.Client) *Parser {
	return &Parser{LLM: llmClient}
}

// ParseResult is the output of Stage [1].
type ParseResult struct {
	Questions []models.PastQuestion `json:"questions"`
	Templates map[string]string     `json:"templates"` // templateKey → LaTeX skeleton
	IRTParams map[string]float64    `json:"irtParams"` // questionID → b param
	KG        *KnowledgeGraph       `json:"kg"`
}

// KnowledgeGraph is the MVP in-memory KG (networkx equivalent).
type KnowledgeGraph struct {
	Nodes     []KGNode            `json:"nodes"`
	Edges     []KGEdge            `json:"edges"`
	Adjacency map[string][]string `json:"adjacency"` // nodeID → neighborIDs
}

// KGNode is a node in the knowledge graph.
type KGNode struct {
	ID    string `json:"id"`
	Type  string `json:"type"` // "point" | "question" | "type"
	Label string `json:"label"`
}

// KGEdge is an edge in the knowledge graph.
type KGEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Rel  string `json:"rel"` // "prereq" | "related" | "tests" | "adapted"
}

// annotationResponse is the LLM JSON output for four-dim annotation.
type annotationResponse struct {
	Points      []string `json:"points"`
	Cognitive   string   `json:"cognitive"`
	Difficulty  float64  `json:"difficulty"`
	Confidence  float64  `json:"confidence"`
	TemplateKey string   `json:"templateKey"`
}

// --- Regex patterns for real Markdown format ---

var (
	// YAML front matter block: ---\n...\n---
	reYAMLFront = regexp.MustCompile(`(?s)^\x2D\x2D\x2D\n(.*?)\n\x2D\x2D\x2D`)
	// YAML key: value
	reYAMLKey = regexp.MustCompile(`(?m)^(\w+):\s*"?(.*?)"?\s*$`)
	// Section header: ### 选择题 / ### 单选题 / etc.
	reSection = regexp.MustCompile(`(?m)^###\s+(.+?)\s*$`)
	// Category subsection: #### 数据结构 / #### Text 1
	reCategory = regexp.MustCompile(`(?m)^####\s+(.+?)\s*$`)
	// Question header: ##### N  or range ##### N-M (English Part B/C composite questions).
	reQuestionHeader = regexp.MustCompile(`(?m)^#####\s+(\d+)(?:-(\d+))?\s*$`)
	// Answer markers:
	//   **【答案】D**  → inline answer
	//   **【答案】**\n6 → answer on next line
	// The optional "\d+[.\s\)、．)]*" prefix covers English range answers like
	// "**41. 【答案】E**" (period+space), "**41、【答案】[C]**" (Chinese
	// enumeration comma), and "**41．【答案】E**" (full-width dot).
	reAnswerInline = regexp.MustCompile(`\*\*(?:\d+[.\s)、．]*)?【答案】\s*([^*\n]*?)\*\*`)
	reAnswerNext   = regexp.MustCompile(`\*\*(?:\d+[.\s)、．]*)?【答案】\*\*\s*\n\s*([^\n]+)`)
	// Chinese-labelled answers without 【】 brackets: "**41. 答案：C**"
	// or "**41、答案：[F]**".
	reAnswerCN = regexp.MustCompile(`\*\*(?:\d+[.\s)、．]*)?答案\s*[：:]\s*([^*\n]+?)\s*\*\*`)
	// Bracket-labelled "[答案]": "**41. [答案] E**".
	reAnswerBracket = regexp.MustCompile(`\*\*(?:\d+[.\s)、．]*)?\[答案\]\s*([^*\n]*?)\*\*`)
	// English Part C translations label the Chinese rendering 【译文】.
	reTranslation = regexp.MustCompile(`(?m)^\*\*(?:\d+[.\s)、．]*)?【译文】\*\*\s*\n([^\n]+)`)
	// Bare-letter answer for range questions: "**41. E**" (no label, just
	// the letter on the answer line).
	reAnswerBare = regexp.MustCompile(`(?m)^\*\*(\d+)[.\s)、．]*\s*([A-Z])\s*\*\*\s*$`)
	// Bare-name answer for matching questions: "**41. Jay Dunwell → [E]**"
	// or "**41. Ryan Hooper**" (no letter, just the name).
	reAnswerName = regexp.MustCompile(`(?m)^\*\*(\d+)[.\s)、．]*\s*(.+?)\s*\*\*\s*$`)
	// Full-width-paren translation: "**（46）English sentence**" (English
	// Part C 2015+ format — the bolded sentence IS the answer).
	reAnswerFullParen = regexp.MustCompile(`(?m)^\*\*（(\d+)）(.+?)\*\*`)
	// Half-width close-paren bare text: "**46) Chinese text**" (E1 2013).
	reAnswerHalfParen = regexp.MustCompile(`(?m)^\*\*(\d+)\)\s*(.+?)\s*\*\*$`)
	// Tags: 【标签】知识点1,知识点2 (may or may not be bold)
	reTags = regexp.MustCompile(`【标签】\s*([^\n*]+?)(?:\*\*|\s|$)`)
	// Explanation boundary: **【解析】** or **【解答】**
	reExplanation = regexp.MustCompile(`\*\*【(?:解析|解答)】\*\*`)
	// Score in text: (4 分) or (10分) or (15 points) or (20 Points), supports
	// both half-width () and full-width （） parentheses. Case-insensitive
	// for the English units.
	reScoreInline = regexp.MustCompile(`(?i)[\(（]\s*(\d+(?:\.\d+)?)\s*(?:分|points?|pts?)\s*[\)）]`)
	// Section-level score: 每小题 2 分 / 每小题3分
	reScorePerQ = regexp.MustCompile(`每小题\s*(\d+(?:\.\d+)?)\s*分`)
	// Per-question score for major questions: 本题满分 12 分 / 本题满分10分
	// Major-question score: 本题满分 12 分 — also tolerate LaTeX-wrapped
	// numbers split across lines: "（本题满分
	// $10$ 分）".
	// Also tolerate the "（满分 N 分）" variant (without 本题 prefix, e.g.
	// math3 2009 Q19).  To avoid matching "可给满分 15 分" in rubric text
	// (408 2009 Q47 评分说明), require 满分 to be preceded by either
	// "本题" or a full/half-width parenthesis.
	reScoreMajor = regexp.MustCompile(`(?:本题|[（(]\s*)满分\s*\$?\s*(\d+(?:\.\d+)?)\s*\$?\s*分`)
	// Total section score: 共 80 分 / 共80分
	reScoreTotal = regexp.MustCompile(`共\s*(\d+(?:\.\d+)?)\s*分`)
	// Choice option: - A. text
	reChoiceOption = regexp.MustCompile(`(?m)^-\s+([A-D])[.、)]\s*(.+)$`)
)

// Parse runs the full parse pipeline on a source path containing exam files.
//
// source can be a single .md file or a directory (walked recursively).
// Each file is split into individual questions, then each is annotated.
func (p *Parser) Parse(ctx context.Context, source string) (*ParseResult, error) {
	result := &ParseResult{
		Templates: make(map[string]string),
		IRTParams: make(map[string]float64),
		KG:        NewKnowledgeGraph(),
	}

	questions, err := p.extractQuestions(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("extract questions: %w", err)
	}

	// Annotate each question via LLM (or use pre-extracted tags if LLM unavailable).
	for i := range questions {
		annotated, err := p.Annotate(ctx, questions[i])
		if err != nil {
			return nil, fmt.Errorf("annotate question %d: %w", i, err)
		}
		questions[i] = annotated
		result.IRTParams[annotated.ID] = annotated.Difficulty
	}

	result.Questions = questions

	// Build KG from annotated questions.
	p.buildKG(result)

	// Extract template skeletons by (题型, 知识点族) clustering.
	p.extractTemplates(result)

	return result, nil
}

// extractQuestions reads Markdown files and splits them into individual questions.
// Supports recursive directory walking.
func (p *Parser) extractQuestions(ctx context.Context, source string) ([]models.PastQuestion, error) {
	var files []string
	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("stat source %s: %w", source, err)
	}

	if info.IsDir() {
		err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".md" {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", source, err)
		}
	} else {
		files = []string{source}
	}

	var allQuestions []models.PastQuestion
	for _, file := range files {
		// Skip README files.
		if strings.EqualFold(filepath.Base(file), "README.md") {
			continue
		}
		qs, err := p.parseFile(file)
		if err != nil {
			return nil, fmt.Errorf("parse file %s: %w", file, err)
		}
		allQuestions = append(allQuestions, qs...)
	}

	return allQuestions, nil
}

// parseFile reads a single exam Markdown file and splits it into questions.
//
// Real format:
//   - YAML front matter (title, source)
//   - ### Section headers (选择题/填空题/解答题/分析题/完形填空/阅读理解/写作)
//   - #### Category subsections (数据结构/组成原理/Text 1)
//   - ##### N individual question headers
//   - Structured markers: 【答案】/【标签】/【解析】
func (p *Parser) parseFile(path string) ([]models.PastQuestion, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	text := string(content)
	base := filepath.Base(path)

	// Parse YAML front matter for year and subject.
	year, subjectFromYAML, source := parseYAMLFront(text)
	// Determine subject from path (more reliable than YAML title).
	subject, subjectCategory := subjectFromPath(path)

	// Strip YAML front matter from text before processing.
	text = reYAMLFront.ReplaceAllString(text, "")

	// Normalize ## section headers to ### for known question section types.
	// Some exam files use ## (h2) instead of ### (h3) for section headers like
	// "## 选择题". We only normalize recognized question sections so that
	// "## 解析" (answer/explanation appendices) remain as-is and their
	// question-level content stays within the preceding section.
	text = normalizeSectionHeaders(text)

	// Parse section-level score-per-question (e.g. "每小题 2 分").
	sectionScores := parseSectionScores(text)

	// Split into sections by ### headers.
	sections := splitSections(text)

	var questions []models.PastQuestion
	qCounter := 0

	for _, sec := range sections {
		secType := mapSectionToType(sec.Header, subject)
		// Parse section-level score for this section.
		secScorePerQ := sectionScores[sec.Header]

		// Split section into categories by #### headers.
		categories := splitCategories(sec.Body)

		// English exams put "(N points)" in category Directions text rather
		// than a section-level 每小题 score, so per-question scores must be
		// derived by dividing the annotation by the question count. Pass 1
		// gathers each category's question blocks and total annotation.
		type catInfo struct {
			header       string
			qBlocks      []questionBlock
			total        float64
			perQ         float64
			auth         bool   // perQ derived from an explicit total annotation
			fallbackStem string // shared passage for questions with no own stem
		}
		infos := make([]catInfo, 0, len(categories))
		for _, cat := range categories {
			fallback := ""
			if loc := reQuestionHeader.FindStringIndex(cat.Body); loc != nil {
				// Text before the first ##### header (e.g. a shared cloze
				// passage) serves as stem context for questions that only
				// carry answer/explanation content.
				fallback = strings.TrimSpace(cat.Body[:loc[0]])
			}
			infos = append(infos, catInfo{
				header:       cat.Header,
				qBlocks:      splitQuestions(cat.Body),
				total:        parseCategoryTotalScore(cat.Body),
				perQ:         secScorePerQ,
				fallbackStem: fallback,
			})
		}

		// Pass 2: resolve per-question scores. A total annotation on a
		// category with no direct questions acts as an umbrella covering all
		// following categories up to the next annotated one — e.g. the cloze
		// Directions prelude before "#### Text", or "#### Part A" whose
		// questions live in the following "#### Text 1..4" categories.
		for i := 0; i < len(infos); i++ {
			info := &infos[i]
			switch {
			case info.total > 0 && len(info.qBlocks) > 0:
				info.perQ = info.total / float64(len(info.qBlocks))
				info.auth = true
			case info.total > 0:
				n := 0
				j := i + 1
				for ; j < len(infos); j++ {
					if infos[j].total > 0 {
						break
					}
					n += len(infos[j].qBlocks)
				}
				if n > 0 {
					perQ := info.total / float64(n)
					for k := i + 1; k < j; k++ {
						infos[k].perQ = perQ
						infos[k].auth = true
					}
				}
				i = j - 1
			}
		}

		for _, info := range infos {
			for _, qb := range info.qBlocks {
				qNum, _ := strconv.Atoi(qb.Header)
				qCounter++

				q := buildQuestion(base, path, subject, subjectCategory, info.header,
					year, qCounter, qNum, secType, secScorePerQ, info.perQ, info.auth,
					info.fallbackStem, qb.Body, source, subjectFromYAML)

				if len(q.Stem) < 10 && len(q.Points) == 0 && q.Answer == "" {
					continue // skip empty fragments
				}

				questions = append(questions, q)
			}
		}
	}

	return questions, nil
}

// questionBlock holds a split question's header number and body.
type questionBlock struct {
	Header string
	Body   string
}

// sectionBlock holds a section header and its body.
type sectionBlock struct {
	Header string
	Body   string
}

// categoryBlock holds a category header and its body.
type categoryBlock struct {
	Header string
	Body   string
}

// parseYAMLFront extracts title and source from YAML front matter.
// Returns (year, subjectHint, source).
func parseYAMLFront(text string) (int, string, string) {
	match := reYAMLFront.FindStringSubmatch(text)
	if len(match) < 2 {
		return 0, "", ""
	}
	yaml := match[1]
	fields := reYAMLKey.FindAllStringSubmatch(yaml, -1)
	var title, source string
	for _, f := range fields {
		key := strings.TrimSpace(f[1])
		val := strings.TrimSpace(f[2])
		switch strings.ToLower(key) {
		case "title":
			title = val
		case "source":
			source = val
		}
	}
	// Extract year from title (e.g. "2024 年 408 真题" → 2024).
	reYear := regexp.MustCompile(`(\d{4})`)
	yearMatch := reYear.FindStringSubmatch(title)
	year := 0
	if len(yearMatch) >= 2 {
		year, _ = strconv.Atoi(yearMatch[1])
	}
	return year, title, source
}

// subjectFromPath maps a file path to subject and category/sub-family.
//
//	data/exams/csgraduates/408/2024.md       → "408", ""
//	data/exams/csgraduates/math/math1/2024.md → "math1", ""
//	data/exams/csgraduates/politics/2024.md   → "politics", ""
//	data/exams/csgraduates/english/english1/2024.md → "english1", ""
func subjectFromPath(path string) (string, string) {
	// Normalize path separators.
	path = filepath.ToSlash(path)
	parts := strings.Split(path, "/")

	// Find the "csgraduates" index, then take the next component(s).
	csIdx := -1
	for i, p := range parts {
		if p == "csgraduates" {
			csIdx = i
			break
		}
	}
	if csIdx < 0 || csIdx+1 >= len(parts) {
		return "unknown", ""
	}

	next := parts[csIdx+1] // 408, math, politics, english
	switch next {
	case "408":
		return "408", ""
	case "politics":
		return "politics", ""
	case "math":
		if csIdx+2 < len(parts) {
			sub := parts[csIdx+2] // math1, math2, math3, math_old
			return sub, ""
		}
		return "math", ""
	case "english":
		if csIdx+2 < len(parts) {
			sub := parts[csIdx+2] // english1, english2
			return sub, ""
		}
		return "english", ""
	default:
		return next, ""
	}
}

// normalizeSectionHeaders converts ## (h2) headers for known question section
// types to ### (h3) so they are recognized by splitSections. Headers like
// "## 解析" or "## 答案" are left untouched so their content stays within the
// preceding real section.
var reH2Section = regexp.MustCompile(`(?m)^##\s+(.+?)\s*$`)

func normalizeSectionHeaders(text string) string {
	return reH2Section.ReplaceAllStringFunc(text, func(line string) string {
		m := reH2Section.FindStringSubmatch(line)
		if len(m) < 2 {
			return line
		}
		header := m[1]
		lower := strings.ToLower(header)
		// Known question section keywords.
		switch {
		case strings.Contains(lower, "选择"),
			strings.Contains(lower, "填空"),
			strings.Contains(lower, "解答"),
			strings.Contains(lower, "分析"),
			strings.Contains(lower, "计算"),
			strings.Contains(lower, "证明"),
			strings.Contains(lower, "完形"),
			strings.Contains(lower, "阅读"),
			strings.Contains(lower, "写作"),
			strings.Contains(lower, "翻译"):
			return "### " + header
		}
		return line
	})
}

// mapSectionToType maps a section header to a QuestionType.
func mapSectionToType(section, subject string) models.QuestionType {
	lower := strings.ToLower(section)
	switch {
	case strings.Contains(lower, "单选"):
		return models.TypeChoice
	case strings.Contains(lower, "多选"):
		return models.TypeMultiChoice
	case strings.Contains(lower, "选择"):
		return models.TypeChoice
	// 完形 must be checked before 填空 — "完形填空" contains "填空".
	case strings.Contains(lower, "完形"), strings.Contains(lower, "阅读"):
		// English cloze/reading are choice-based.
		return models.TypeChoice
	case strings.Contains(lower, "填空"):
		return models.TypeFillBlank
	case strings.Contains(lower, "写作"):
		return models.TypeMajor
	case strings.Contains(lower, "解答"), strings.Contains(lower, "分析"), strings.Contains(lower, "计算"), strings.Contains(lower, "证明"):
		return models.TypeMajor
	default:
		return models.TypeMajor
	}
}

// parseSectionScores extracts per-question scores from section descriptions.
// Returns map of section header → score per question.
func parseSectionScores(text string) map[string]float64 {
	scores := make(map[string]float64)
	// Match "每小题 N 分" within ~200 chars of section header.
	secScorePattern := regexp.MustCompile(`(?ms)(?:每小题\s*(\d+(?:\.\d+)?)\s*分)`)
	lines := strings.Split(text, "\n")
	currentSection := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### ") {
			currentSection = strings.TrimSpace(strings.TrimPrefix(trimmed, "### "))
		}
		if currentSection == "" {
			continue
		}
		if match := secScorePattern.FindStringSubmatch(line); len(match) >= 2 {
			score, _ := strconv.ParseFloat(match[1], 64)
			if score > 0 {
				scores[currentSection] = score
			}
		}
	}
	return scores
}

// splitSections splits text by ### headers, preserving header→body association.
func splitSections(text string) []sectionBlock {
	matches := reSection.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return []sectionBlock{{Header: "", Body: text}}
	}

	var sections []sectionBlock
	for i, m := range matches {
		headerStart := m[2]
		headerEnd := m[3]
		header := text[headerStart:headerEnd]
		bodyStart := m[1] // end of the ### line
		var bodyEnd int
		if i+1 < len(matches) {
			bodyEnd = matches[i+1][0] // start of next ### line
		} else {
			bodyEnd = len(text)
		}
		body := text[bodyStart:bodyEnd]
		sections = append(sections, sectionBlock{Header: header, Body: body})
	}
	return sections
}

// splitCategories splits a section body by #### headers.
// If no #### headers exist, returns the whole body as a single category.
// If the section body has text before the first #### header (a "prelude"
// containing Directions and score annotations), that prelude is returned
// as a pseudo-category with Header="" so callers can extract score info.
func splitCategories(text string) []categoryBlock {
	matches := reCategory.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return []categoryBlock{{Header: "", Body: text}}
	}

	var cats []categoryBlock
	// Capture the prelude (text before the first #### header) as a
	// pseudo-category so score annotations like "(10 points)" in
	// section-level Directions are not lost.
	if matches[0][0] > 0 {
		prelude := text[:matches[0][0]]
		if strings.TrimSpace(prelude) != "" {
			cats = append(cats, categoryBlock{Header: "", Body: prelude})
		}
	}
	for i, m := range matches {
		headerStart := m[2]
		headerEnd := m[3]
		header := text[headerStart:headerEnd]
		bodyStart := m[1]
		var bodyEnd int
		if i+1 < len(matches) {
			bodyEnd = matches[i+1][0]
		} else {
			bodyEnd = len(text)
		}
		body := text[bodyStart:bodyEnd]
		cats = append(cats, categoryBlock{Header: header, Body: body})
	}
	return cats
}

// parseCategoryTotalScore extracts the total score for a category from its
// Directions text. English exams use "(N points)" or "(N分)" in the
// Directions paragraph (e.g. "(10 points)", "(40 points)", "(15 points)").
// Returns 0 if no total score annotation is found.
func parseCategoryTotalScore(catBody string) float64 {
	// The "(N points)" / "（共N分）" annotation lives in the category's
	// Directions — the text BEFORE the first ##### question header. Scanning
	// only that portion avoids picking up per-question sub-part scores like
	// "（4 分）" that appear inside question bodies when a category has no
	// Directions of its own (e.g. 408 解答题 categories).
	head := catBody
	if loc := reQuestionHeader.FindStringIndex(catBody); loc != nil {
		head = catBody[:loc[0]]
	}
	// Use reScoreInline which matches (N 分), (N points), （N分）, etc.
	if match := reScoreInline.FindStringSubmatch(head); len(match) >= 2 {
		score, _ := strconv.ParseFloat(match[1], 64)
		return score
	}
	return 0
}

// splitQuestions splits a category body by ##### N headers.
// Returns question blocks with the header number and body.
// For range headers like "##### 41-45", the block is expanded into
// individual question blocks by splitting the body at "N." prefixes.
func splitQuestions(text string) []questionBlock {
	matches := reQuestionHeader.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return nil
	}

	var blocks []questionBlock
	for i, m := range matches {
		headerNum := text[m[2]:m[3]] // the first number
		bodyStart := m[1]            // end of the ##### N line
		var bodyEnd int
		if i+1 < len(matches) {
			bodyEnd = matches[i+1][0]
		} else {
			bodyEnd = len(text)
		}
		body := text[bodyStart:bodyEnd]

		// Check if this is a range header (##### N-M).
		if m[4] >= 0 && m[5] >= 0 {
			endNumStr := text[m[4]:m[5]]
			endNum, _ := strconv.Atoi(endNumStr)
			startNum, _ := strconv.Atoi(headerNum)
			// Expand the range into individual sub-questions by splitting
			// the body at "N." or "N " line-start prefixes.
			subBlocks := splitRangeQuestions(body, startNum, endNum)
			if len(subBlocks) > 0 {
				blocks = append(blocks, subBlocks...)
				continue
			}
			// Fallback: treat as a single question with the first number.
		}

		blocks = append(blocks, questionBlock{Header: headerNum, Body: body})
	}
	return blocks
}

// splitRangeQuestions splits a composite question body (from a ##### N-M header)
// into individual question blocks. Content boundaries are the FIRST occurrence
// of each number at line start — either bare ("41. **Hannah**") or parenthesized
// ("(46) They sometimes..."). Numbered answer sections ("**41. 【答案】E**",
// "**46. 【译文】**") that all cluster at the end of the body are cut out and
// re-attached to their own question's block, so every question carries its
// answer/translation.
func splitRangeQuestions(body string, startNum, endNum int) []questionBlock {
	// Content markers: "41. text" / "**46. 【译文】" / "(46) They sometimes..."
	// Supports separators: . (ASCII dot), ．(full-width dot), 、(Chinese
	// enumeration comma), space, ).
	contentPattern := regexp.MustCompile(`(?m)^(?:\*\*)?(\d+)[\.\s)、．]|(?:\(|（)(\d+)(?:\)|）)`)
	// Answer sections: matches any of these formats at line start:
	//  1. "**41. 【答案】E**" / "**46. 【译文】**" / "**41、答案：[C]**" /
	//     "**41. [答案] E**" (labelled — group 1)
	//  2. "**（46）text**" (full-width parens, E1 2015 — group 2)
	//  3. "**46) text**" (half-width close-paren, E1 2013 — group 3)
	//  4. "**46. text**" (bare text, no label, E1 2016 — group 4)
	// Bare-letter ("**41. E**") and bare-name ("**41. Ryan Hooper**")
	// answer lines are also caught by alternative 4 and handled by
	// extractAnswer via reAnswerBare/reAnswerName.
	answerPattern := regexp.MustCompile(`(?m)(?:^\*\*(\d+)[.\s)、．]*(?:【(?:答案|译文)】|\[答案\]|答案\s*[：:])|^\*\*（(\d+)）|^\*\*(\d+)\)|^\*\*(\d+)[.\s)、．]+[^【\[答\n])`)

	type sec struct {
		num   int
		start int // start of the match (line start)
		end   int // end of the prefix match
	}

	var positions []sec
	seen := make(map[int]bool) // track first occurrence per number
	for _, m := range contentPattern.FindAllStringSubmatchIndex(body, -1) {
		var numStr string
		if m[2] >= 0 {
			numStr = body[m[2]:m[3]]
		} else {
			numStr = body[m[4]:m[5]]
		}
		num, _ := strconv.Atoi(numStr)
		if num < startNum || num > endNum {
			continue
		}
		if seen[num] {
			continue // only keep first occurrence
		}
		seen[num] = true
		positions = append(positions, sec{num: num, start: m[0], end: m[1]})
	}

	if len(positions) == 0 {
		return nil
	}

	// Locate the numbered answer sections (their extents run to the next
	// answer section or the end of the body).
	type ansSec struct {
		num        int
		start, end int
	}
	answerMatches := answerPattern.FindAllStringSubmatchIndex(body, -1)
	var answers []ansSec
	for i, m := range answerMatches {
		// Try each capture group to find the number.
		var numStr string
		for g := 2; g < len(m); g += 2 {
			if m[g] >= 0 {
				numStr = body[m[g]:m[g+1]]
				break
			}
		}
		num, _ := strconv.Atoi(numStr)
		if num < startNum || num > endNum {
			continue
		}
		end := len(body)
		if i+1 < len(answerMatches) {
			end = answerMatches[i+1][0]
		}
		answers = append(answers, ansSec{num: num, start: m[0], end: end})
	}
	answerByNum := make(map[int]ansSec, len(answers))
	for _, a := range answers {
		answerByNum[a.num] = a
	}

	// Build blocks: content range [p.end, next p.start), with any answer
	// sections removed from wherever they landed and this block's own
	// answer section appended.
	var blocks []questionBlock
	for i, p := range positions {
		contentStart := p.end
		var contentEnd int
		if i+1 < len(positions) {
			contentEnd = positions[i+1].start
		} else {
			contentEnd = len(body)
		}

		// Strip out any answer sections overlapping this content range
		// (clamped to the range, keeping the text before/between/after).
		// Overlap-based clamping matters when the answer section starts on
		// the same line as the content split point (e.g. English2 Part B,
		// whose only line-start numbers ARE the "**41. 答案：C**" lines).
		var sb strings.Builder
		cur := contentStart
		for _, a := range answers {
			aStart, aEnd := a.start, a.end
			if aStart < contentStart {
				aStart = contentStart
			}
			if aEnd > contentEnd {
				aEnd = contentEnd
			}
			if aEnd > cur {
				if aStart > cur {
					sb.WriteString(body[cur:aStart])
				}
				cur = aEnd
			}
		}
		sb.WriteString(body[cur:contentEnd])
		content := strings.TrimSpace(sb.String())

		// Re-attach this question's own answer/translation section.
		if a, ok := answerByNum[p.num]; ok {
			if content != "" {
				content += "\n\n"
			}
			content += strings.TrimSpace(body[a.start:a.end])
		}

		blocks = append(blocks, questionBlock{
			Header: strconv.Itoa(p.num),
			Body:   content,
		})
	}
	return blocks
}

// buildQuestion constructs a PastQuestion from parsed components.
func buildQuestion(baseFile, fullPath, subject, subjectCategory, category string,
	year, counter, qNum int, qType models.QuestionType, secScorePerQ, catScorePerQ float64,
	catScoreAuth bool, fallbackStem, body, source, yamlTitle string) models.PastQuestion {

	// Extract answer from structured markers.
	answer := extractAnswer(body)

	// Extract knowledge-point tags.
	points := extractTags(body)

	// Extract stem: everything before the first structured marker.
	stem := extractStem(body)
	// Shared-passage fallback: questions whose stem reduced to nothing but
	// markers and a number prefix (e.g. English2 Part B, whose body starts
	// at the "**41. 答案：C**" line) get the category's pre-question text
	// (Directions + passage) as stem context.
	if strings.Trim(stem, "* \t\n\r0123456789.、：:【】()（）") == "" {
		stem = fallbackStem
	}

	// Extract choice options if applicable.
	options := extractOptions(stem)

	// Determine score.
	score := determineScore(body, stem, qType, secScorePerQ, catScorePerQ, catScoreAuth, subject, year)

	// Build ID: subject_year_qNum (e.g. "408_2024_1").
	id := fmt.Sprintf("%s_%d_%d", subject, year, qNum)
	if year == 0 {
		// Fallback if year not found in YAML.
		id = fmt.Sprintf("%s_%s_%d", subject, baseFile, qNum)
	}

	// Template key: type_firstPoint (or type_category).
	tplKey := fmt.Sprintf("%s_%s", qType, "default")
	if len(points) > 0 {
		tplKey = fmt.Sprintf("%s_%s", qType, points[0])
	} else if category != "" {
		tplKey = fmt.Sprintf("%s_%s", qType, category)
	}

	// Source label: the subject category for display.
	srcLabel := subject
	if subjectCategory != "" {
		srcLabel = subjectCategory
	}

	return models.PastQuestion{
		ID:          id,
		Subject:     subject,
		Year:        year,
		Type:        qType,
		Points:      points,
		Score:       score,
		Source:      srcLabel,
		Stem:        stem,
		Answer:      answer,
		Options:     options,
		TemplateKey: tplKey,
	}
}

// extractAnswer finds the answer from **【答案】X**, **【答案】**\nX,
// or — for English Part C translations — the 【译文】 line.
func extractAnswer(body string) string {
	// Try inline first: **【答案】X** (optional "N. " prefix).
	if m := reAnswerInline.FindStringSubmatch(body); len(m) >= 2 {
		ans := strings.TrimSpace(m[1])
		if ans != "" {
			return ans
		}
	}
	// Try next-line: **【答案】**\nX
	if m := reAnswerNext.FindStringSubmatch(body); len(m) >= 2 {
		ans := strings.TrimSpace(m[1])
		// Clean up trailing markdown.
		ans = strings.TrimRight(ans, "* \t")
		return ans
	}
	// Bracket-labelled: **41. [答案] E**
	if m := reAnswerBracket.FindStringSubmatch(body); len(m) >= 2 {
		ans := strings.TrimSpace(m[1])
		if ans != "" {
			return ans
		}
	}
	// Bracket-less Chinese form: **41. 答案：C** or **41、答案：[F]**
	if m := reAnswerCN.FindStringSubmatch(body); len(m) >= 2 {
		ans := strings.TrimSpace(m[1])
		if ans != "" {
			return ans
		}
	}
	// English Part C translation: **N. 【译文】**\n<Chinese rendering>
	if m := reTranslation.FindStringSubmatch(body); len(m) >= 2 {
		ans := strings.TrimSpace(m[1])
		if ans != "" {
			return ans
		}
	}
	// Full-width-paren translation: "**（46）English sentence**" (E1 2015+).
	if m := reAnswerFullParen.FindStringSubmatch(body); len(m) >= 3 {
		ans := strings.TrimSpace(m[2])
		if ans != "" {
			return ans
		}
	}
	// Half-width close-paren bare text: "**46) Chinese text**" (E1 2013).
	if m := reAnswerHalfParen.FindStringSubmatch(body); len(m) >= 3 {
		ans := strings.TrimSpace(m[2])
		if ans != "" {
			return ans
		}
	}
	// E1 2019 Part C: "**（46）**\n**参考译文：**\n<Chinese rendering>".
	// The answer is the text following 参考译文.
	const refTrans = "**参考译文：**"
	if idx := strings.Index(body, refTrans); idx >= 0 {
		rest := strings.TrimSpace(body[idx+len(refTrans):])
		if rest != "" {
			line := strings.SplitN(rest, "\n", 2)[0]
			line = strings.TrimSpace(line)
			if line != "" {
				return line
			}
		}
	}
	// E1 2016 Part C: "**46. English sentence**" (bare text, no label).
	// The bolded line is the English sentence that IS the answer.
	// We try reAnswerName which matches "**N. text**", but only accept
	// if it looks like a sentence (contains a space and is long enough).
	if m := reAnswerName.FindStringSubmatch(body); len(m) >= 3 {
		name := strings.TrimSpace(m[2])
		if name != "" && !strings.Contains(name, "【") &&
			len(name) > 10 && strings.Contains(name, " ") {
			return name
		}
	}
	// Bare-letter answer for range questions: **41. E** (no label).
	// Only valid if body is short (answer-only, no long stem).
	if m := reAnswerBare.FindStringSubmatch(body); len(m) >= 3 {
		return m[2]
	}
	// Bare-name answer for matching questions: "**41. Jay Dunwell → [E]**"
	// or "**41. Ryan Hooper**" (no letter, just the name).
	if m := reAnswerName.FindStringSubmatch(body); len(m) >= 3 {
		name := strings.TrimSpace(m[2])
		if name != "" && !strings.Contains(name, "【") {
			return name
		}
	}
	// Last resort for English2-style full-text translation: the Chinese
	// rendering is the ONLY paragraph following **【解析】**, with no label.
	const explMarker = "**【解析】**"
	if idx := strings.LastIndex(body, explMarker); idx >= 0 {
		rest := strings.TrimSpace(body[idx+len(explMarker):])
		if rest != "" && !strings.Contains(rest, "\n\n") && !strings.Contains(rest, "【") {
			return strings.TrimRight(rest, "* \t")
		}
	}
	return ""
}

// extractTags finds knowledge points from 【标签】标记1,标记2.
func extractTags(body string) []string {
	matches := reTags.FindAllStringSubmatch(body, -1)
	var allPoints []string
	seen := make(map[string]bool)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		raw := strings.TrimSpace(m[1])
		// Split by comma (both Chinese and ASCII).
		for _, pt := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == '，' || r == '/' || r == '、'
		}) {
			pt = strings.TrimSpace(pt)
			if pt == "" || seen[pt] {
				continue
			}
			seen[pt] = true
			allPoints = append(allPoints, pt)
		}
	}
	return allPoints
}

// extractStem returns the question stem: everything before the first structured marker.
// Markers: **【答案】, 【标签】, **【解析】, **【解答】
func extractStem(body string) string {
	// Find the earliest structured marker position. The bare "答案："/
	// "解析：" forms cover English2-style answer sections without 【】
	// brackets; 【译文】 has no ** requirement because range-split bodies
	// start mid-marker ("**46. 【译文】").
	markers := []string{"**【答案】", "【标签】", "**【解析】", "**【解答】", "【译文】", "答案：", "解析："}
	minPos := len(body)
	for _, marker := range markers {
		idx := strings.Index(body, marker)
		if idx >= 0 && idx < minPos {
			minPos = idx
		}
	}
	stem := body[:minPos]
	// Clean up leading/trailing whitespace and blank lines.
	stem = strings.TrimSpace(stem)
	// Remove leading question-number prefix if present (e.g. "1." or "46．").
	// A punctuation separator is required: bare "2023 年 10 月" is prose and
	// must keep its year.
	stem = regexp.MustCompile(`^\d+[\.．、]\s*`).ReplaceAllString(stem, "")
	// Fallback for English cloze: the body starts with **【答案】** and only
	// quotes the blank-containing sentence inside the explanation as a
	// blockquote ("第 N 题题干：" followed by "> ..." lines).
	if stem == "" {
		stem = extractBlockquote(body)
	}
	return stem
}

// extractBlockquote returns the first "> " blockquote in the body with its
// markers stripped and lines joined. Returns "" when no blockquote exists.
func extractBlockquote(body string) string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, ">") {
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(trimmed, ">")))
		} else if len(lines) > 0 {
			break // blockquote ended
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// extractOptions pulls A/B/C/D options from the stem text.
func extractOptions(stem string) []string {
	matches := reChoiceOption.FindAllStringSubmatch(stem, -1)
	if len(matches) == 0 {
		return nil
	}
	var opts []string
	for _, m := range matches {
		if len(m) >= 3 {
			opts = append(opts, strings.TrimSpace(m[2]))
		}
	}
	return opts
}

// determineScore resolves the score for a question.
//
// For major/composite questions (解答题/分析题/综合题), inline (N分) markers
// refer to individual sub-parts, NOT the total. So we sum ALL inline scores
// to get the true total. If a section-level 每小题 score exists, use that
// instead (it's authoritative).
//
// For non-major questions (choice/fill_blank), an inline score usually is the
// per-question score — EXCEPT in English exams, where 采分点 rubrics like
// （0.5分） inside translation explanations are sub-part weights. When the
// category score was derived from an explicit total annotation ("(10 points)"),
// it is authoritative and beats inline markers.
//
// For 408 specifically: each major question is worth a FIXED 10 points
// (70 points across 7 questions, 2009-2025). The inline (N分) sub-part weights
// in the answer/解析 section are rubric guidance and often DON'T sum to 10
// (e.g. missing "评分说明" bonus points). So for 408 major questions without
// an explicit "本题满分" annotation, the subject default (10) is more reliable
// than summing inline sub-parts.
//
// Priority for major:     section per-question > 本题满分 > authoritative category > subject default > sum-of-inline > type default
// Priority for non-major: authoritative category > inline > section per-question > subject default > type default
func determineScore(body, stem string, qType models.QuestionType,
	secScorePerQ, catScorePerQ float64, catScoreAuth bool, subject string, year int) float64 {
	if qType == models.TypeMajor {
		// 1. Section-level per-question score is authoritative for major questions.
		if secScorePerQ > 0 {
			return secScorePerQ
		}
		// 2. "本题满分 N 分" — explicit per-question score for major questions.
		if match := reScoreMajor.FindStringSubmatch(body); len(match) >= 2 {
			score, _ := strconv.ParseFloat(match[1], 64)
			if score > 0 {
				return score
			}
		}
		// 3. Category total derived per-question score (e.g. English writing
		//    "Part B (20 points)" with one question).
		if catScoreAuth && catScorePerQ > 0 {
			return catScorePerQ
		}
		// 4. Sum all inline sub-part scores (e.g. (4分) + (9分) = 13).
		// English-style "(N points)" annotations are Directions totals, not
		// sub-parts — they can repeat verbatim in sample-answer sections, so
		// take the first occurrence instead of summing.
		//
		// For 408, each major question is worth a fixed 10 points. The inline
		// (N分) markers are rubric sub-part weights that often DON'T sum to 10
		// (some don't include "评分说明" bonus points). So for 408, if the
		// sum-of-inline deviates from the known 10-point-per-question default,
		// trust the subject default instead.
		allMatches := reScoreInline.FindAllStringSubmatch(body, -1)
		var sum, pointsTotal float64
		for _, m := range allMatches {
			if len(m) >= 2 {
				s, _ := strconv.ParseFloat(m[1], 64)
				if strings.Contains(strings.ToLower(m[0]), "point") {
					if pointsTotal == 0 {
						pointsTotal = s
					}
					continue
				}
				sum += s
			}
		}
		if pointsTotal > 0 {
			return pointsTotal
		}
		// 5. Subject-specific default (408 major = 10 pts/question).
		// For 408, this is more reliable than sum-of-inline sub-parts.
		if def := subjectDefaultScore(subject, qType, year); def > 0 {
			return def
		}
		if sum > 0 {
			return sum
		}
		// 6. Type default.
		return typeDefaultScore(qType)
	}

	// Non-major: authoritative category score wins (beats 采分点 rubrics),
	// then inline, then section-level per-question.
	if catScoreAuth && catScorePerQ > 0 {
		return catScorePerQ
	}
	if match := reScoreInline.FindStringSubmatch(body); len(match) >= 2 {
		score, _ := strconv.ParseFloat(match[1], 64)
		if score > 0 {
			return score
		}
	}
	// Section-level per-question score.
	if secScorePerQ > 0 {
		return secScorePerQ
	}
	// Subject-specific default.
	if def := subjectDefaultScore(subject, qType, year); def > 0 {
		return def
	}
	// Type default.
	return typeDefaultScore(qType)
}

func subjectDefaultScore(subject string, t models.QuestionType, year int) float64 {
	switch subject {
	case "408":
		switch t {
		case models.TypeChoice:
			return 2
		case models.TypeMajor:
			return 10 // 70 points across 7 major questions
		}
	case "politics":
		switch t {
		case models.TypeChoice:
			return 1
		case models.TypeMultiChoice:
			return 2
		case models.TypeMajor:
			return 10
		}
	case "math1", "math2", "math3":
		switch t {
		case models.TypeChoice:
			if year > 0 && year <= 2020 {
				return 4 // old format (2008-2020): 8 choice @ 4pts = 32
			}
			return 5 // new format (2021+): 10 choice @ 5pts = 50
		case models.TypeFillBlank:
			if year > 0 && year <= 2020 {
				return 4 // old format: 6 fill @ 4pts = 24
			}
			return 5 // new format: 6 fill @ 5pts = 30
		case models.TypeMajor:
			return 12
		}
	case "math_old":
		switch t {
		case models.TypeChoice:
			return 3
		case models.TypeFillBlank:
			return 3
		case models.TypeMajor:
			return 10
		}
	case "english1", "english2":
		switch t {
		case models.TypeChoice:
			return 2
		case models.TypeMajor:
			return 10 // writing
		}
	}
	return 0
}

func typeDefaultScore(t models.QuestionType) float64 {
	switch t {
	case models.TypeChoice, models.TypeMultiChoice:
		return 2
	case models.TypeFillBlank:
		return 4
	default:
		return 10
	}
}

// Annotate uses LLM to label a single question's four dimensions.
// If the question already has tags from 【标签】, those are used as the knowledge points;
// the LLM only fills in cognitive level and difficulty.
// If no LLM is configured, uses heuristic defaults.
func (p *Parser) Annotate(ctx context.Context, q models.PastQuestion) (models.PastQuestion, error) {
	// If we already have tags from structured data, keep them.
	hasTags := len(q.Points) > 0 && q.Points[0] != "unknown"

	if p.LLM == nil {
		// Without LLM, use heuristic defaults.
		q.Difficulty = 0.5
		q.DifficultyConfidence = 0.3
		if !hasTags {
			q.Points = []string{"unknown"}
		}
		if q.Cognitive == "" {
			q.Cognitive = models.CogApply
		}
		if q.TemplateKey == "" {
			if hasTags {
				q.TemplateKey = fmt.Sprintf("%s_%s", q.Type, q.Points[0])
			} else {
				q.TemplateKey = fmt.Sprintf("%s_default", q.Type)
			}
		}
		return q, nil
	}

	// If we already have structured tags, only ask LLM for cognitive/difficulty.
	system := `你是一个考研真题分析专家。你的任务是标注真题的两个维度:
1. 认知层(Bloom): remember/understand/apply/analyze/evaluate/create
2. 难度: 0~1, 1=最难

请以JSON格式返回标注结果，格式如下:
{"cognitive": "apply", "difficulty": 0.5, "confidence": 0.8}`

	user := fmt.Sprintf("科目: %s\n年份: %d\n题型: %s\n分值: %.0f\n题面:\n%s",
		q.Subject, q.Year, q.Type, q.Score, q.Stem)

	var resp annotationResponse
	if err := p.LLM.ChatJSON(ctx, p.bestModel(), []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, &resp); err != nil {
		// Fallback: keep defaults with low confidence.
		q.Difficulty = 0.5
		q.DifficultyConfidence = 0.3
		if !hasTags {
			q.Points = []string{"unknown"}
		}
		if q.Cognitive == "" {
			q.Cognitive = models.CogApply
		}
		if q.TemplateKey == "" {
			q.TemplateKey = fmt.Sprintf("%s_default", q.Type)
		}
		return q, nil
	}

	// LLM provides cognitive + difficulty; tags come from structured data.
	if !hasTags && len(resp.Points) > 0 {
		q.Points = resp.Points
	}
	if !hasTags && len(q.Points) == 0 {
		q.Points = []string{"unknown"}
	}
	q.Cognitive = models.CognitiveLevel(resp.Cognitive)
	if q.Cognitive == "" {
		q.Cognitive = models.CogApply
	}
	q.Difficulty = resp.Difficulty
	if q.Difficulty <= 0 || q.Difficulty > 1 {
		q.Difficulty = 0.5
	}
	q.DifficultyConfidence = resp.Confidence
	if q.DifficultyConfidence <= 0 || q.DifficultyConfidence > 1 {
		q.DifficultyConfidence = 0.5
	}
	if q.TemplateKey == "" {
		if resp.TemplateKey != "" {
			q.TemplateKey = resp.TemplateKey
		} else if len(q.Points) > 0 {
			q.TemplateKey = fmt.Sprintf("%s_%s", q.Type, q.Points[0])
		} else {
			q.TemplateKey = fmt.Sprintf("%s_default", q.Type)
		}
	}

	return q, nil
}

// bestModel returns the parser's preferred model name.
func (p *Parser) bestModel() string {
	return "deepseek-chat"
}

// buildKG constructs the knowledge graph from annotated questions.
func (p *Parser) buildKG(result *ParseResult) {
	kg := result.KG
	seenPoints := make(map[string]bool)

	for _, q := range result.Questions {
		// Add question node.
		qNode := KGNode{ID: q.ID, Type: "question", Label: fmt.Sprintf("%s %d", q.Subject, q.Year)}
		kg.AddNode(qNode)

		// Add knowledge-point nodes and edges.
		for _, pt := range q.Points {
			if !seenPoints[pt] {
				kg.AddNode(KGNode{ID: pt, Type: "point", Label: pt})
				seenPoints[pt] = true
			}
			kg.AddEdge(q.ID, pt, "tests")
		}

		// Add co-occurring point-pair edges (related).
		for i := 0; i < len(q.Points); i++ {
			for j := i + 1; j < len(q.Points); j++ {
				kg.AddEdge(q.Points[i], q.Points[j], "related")
			}
		}
	}
}

// extractTemplates creates skeleton template keys from annotated questions.
// Each unique (type, knowledge-family) pair gets a template entry.
func (p *Parser) extractTemplates(result *ParseResult) {
	for _, q := range result.Questions {
		if q.TemplateKey == "" {
			continue
		}
		if _, exists := result.Templates[q.TemplateKey]; !exists {
			result.Templates[q.TemplateKey] = defaultSkeleton(q.Type)
		}
	}
}

// defaultSkeleton returns a basic LaTeX skeleton for a question type.
func defaultSkeleton(t models.QuestionType) string {
	return latex.QuestionSkeleton(string(t))
}

// NewKnowledgeGraph creates an empty KG.
func NewKnowledgeGraph() *KnowledgeGraph {
	return &KnowledgeGraph{
		Adjacency: make(map[string][]string),
	}
}

// AddNode adds a node to the KG (dedup by ID).
func (kg *KnowledgeGraph) AddNode(n KGNode) {
	for _, existing := range kg.Nodes {
		if existing.ID == n.ID {
			return
		}
	}
	kg.Nodes = append(kg.Nodes, n)
}

// AddEdge adds a directed edge to the KG (dedup by from+to+rel).
func (kg *KnowledgeGraph) AddEdge(from, to, rel string) {
	for _, e := range kg.Edges {
		if e.From == from && e.To == to && e.Rel == rel {
			return
		}
	}
	kg.Edges = append(kg.Edges, KGEdge{From: from, To: to, Rel: rel})
	kg.Adjacency[from] = append(kg.Adjacency[from], to)
}

// MarshalJSON ensures adjacency map is non-nil for JSON output.
func (kg *KnowledgeGraph) MarshalJSON() ([]byte, error) {
	if kg.Adjacency == nil {
		kg.Adjacency = make(map[string][]string)
	}
	type alias KnowledgeGraph
	return json.Marshal((*alias)(kg))
}
