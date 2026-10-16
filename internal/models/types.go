// Package models defines the core domain types shared across all pipeline stages.
// These structs mirror the concepts described in the project documentation.
package models

import (
	"fmt"
	"strconv"
	"strings"
)

// KnowledgePoint is a node in the knowledge graph.
type KnowledgePoint struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Subject  string `json:"subject"` // e.g. "数据结构", "高数"
	Chapter  string `json:"chapter"`
	Bloom    string `json:"bloom"`              // 记忆/理解/应用/分析/综合/评价
	ParentID string `json:"parentId,omitempty"` // hierarchy within KG
}

// QuestionType enumerates the discrete question formats.
type QuestionType string

const (
	TypeChoice      QuestionType = "choice"       // 选择(单选)
	TypeMultiChoice QuestionType = "multi_choice" // 多选
	TypeFillBlank   QuestionType = "fill_blank"   // 填空
	TypeMajor       QuestionType = "major"        // 大题(计算/证明/算法)
)

// IsEnglishSubject identifies the two English postgraduate-paper profiles.
func IsEnglishSubject(subject string) bool {
	subject = strings.ToLower(strings.TrimSpace(subject))
	return subject == "english1" || subject == "english2" || subject == "英语一" || subject == "英语二"
}

// EnglishSectionKind identifies the official section a position belongs to.
type EnglishSectionKind int

const (
	EnglishCloze EnglishSectionKind = iota
	EnglishPartA
	EnglishPartB
	EnglishTranslation
	EnglishWritingA
	EnglishWritingB
)

// EnglishSlot is the official descriptor of one English position.
type EnglishSlot struct {
	Kind  EnglishSectionKind
	Type  QuestionType
	Score float64
	Text  int // Part A text number, 1-4
}

// EnglishSubjectTag normalises an English subject to the tag used in
// knowledge-point paths ("english1"/"english2"). It returns "" for other
// subjects.
func EnglishSubjectTag(subject string) string {
	subject = strings.ToLower(strings.TrimSpace(subject))
	switch subject {
	case "english1", "英语一":
		return "english1"
	case "english2", "英语二":
		return "english2"
	}
	return ""
}

// EnglishPaperSize returns the number of positions of the fixed English layout
// (English I: 52, English II: 48, other subjects: 0).
func EnglishPaperSize(subject string) int {
	switch EnglishSubjectTag(subject) {
	case "english1":
		return 52
	case "english2":
		return 48
	}
	return 0
}

// EnglishSlotFor describes the official section, type and score of one English
// position. It is the single source of truth shared by composition, generation,
// validation and assembly.
func EnglishSlotFor(subject string, position int) (EnglishSlot, bool) {
	tag := EnglishSubjectTag(subject)
	if tag == "" || position < 1 || position > EnglishPaperSize(subject) {
		return EnglishSlot{}, false
	}
	switch {
	case position <= 20:
		return EnglishSlot{Kind: EnglishCloze, Type: TypeChoice, Score: 0.5}, true
	case position <= 40:
		return EnglishSlot{Kind: EnglishPartA, Type: TypeChoice, Score: 2, Text: (position-21)/5 + 1}, true
	case position <= 45:
		return EnglishSlot{Kind: EnglishPartB, Type: TypeChoice, Score: 2}, true
	}
	if tag == "english1" {
		switch {
		case position <= 50:
			return EnglishSlot{Kind: EnglishTranslation, Type: TypeMajor, Score: 2}, true
		case position == 51:
			return EnglishSlot{Kind: EnglishWritingA, Type: TypeMajor, Score: 10}, true
		default:
			return EnglishSlot{Kind: EnglishWritingB, Type: TypeMajor, Score: 20}, true
		}
	}
	switch position {
	case 46:
		return EnglishSlot{Kind: EnglishTranslation, Type: TypeMajor, Score: 15}, true
	case 47:
		return EnglishSlot{Kind: EnglishWritingA, Type: TypeMajor, Score: 10}, true
	default:
		return EnglishSlot{Kind: EnglishWritingB, Type: TypeMajor, Score: 15}, true
	}
}

// EnglishPointsPath builds the official knowledge-point path of one English
// position (subject tag, section, task label, position tag).
func EnglishPointsPath(subject string, position int) ([]string, bool) {
	tag := EnglishSubjectTag(subject)
	slot, ok := EnglishSlotFor(subject, position)
	if !ok {
		return nil, false
	}
	positionTag := fmt.Sprintf("题位%d", position)
	switch slot.Kind {
	case EnglishCloze:
		return []string{tag, "完形填空", "语篇词汇与语境", positionTag}, true
	case EnglishPartA:
		return []string{tag, "阅读理解", "Part A", fmt.Sprintf("Text %d", slot.Text), positionTag}, true
	case EnglishPartB:
		return []string{tag, "阅读理解", "Part B", EnglishPartBTaskLabel(subject), positionTag}, true
	case EnglishTranslation:
		return []string{tag, "翻译", "英译汉", positionTag}, true
	case EnglishWritingA:
		return []string{tag, "写作", "应用文", positionTag}, true
	default:
		if tag == "english1" {
			return []string{tag, "写作", "图表或主题作文", positionTag}, true
		}
		return []string{tag, "写作", "图表作文", positionTag}, true
	}
}

// EnglishQuestionOrder returns the official section order and the source
// position encoded in English knowledge-point paths. It keeps composition and
// rendering aligned even though all objective English items use TypeChoice.
func EnglishQuestionOrder(points []string) (section, position int) {
	section = 6
	if len(points) > 1 {
		switch strings.TrimSpace(points[1]) {
		case "完形填空":
			section = 0
		case "阅读理解":
			section = 1
			if len(points) > 2 && strings.EqualFold(strings.TrimSpace(points[2]), "Part B") {
				section = 2
			}
		case "翻译":
			section = 3
		case "写作":
			section = 5
			if len(points) > 2 && strings.Contains(strings.TrimSpace(points[2]), "应用文") {
				section = 4
			}
		}
	}
	position = 1 << 30
	if len(points) > 0 {
		last := strings.TrimSpace(points[len(points)-1])
		last = strings.TrimPrefix(last, "题位")
		if n, err := strconv.Atoi(last); err == nil && n > 0 {
			position = n
		}
	}
	return section, position
}

// EnglishQuestionPosition returns the official question number encoded in an
// English question's knowledge-point path.
func EnglishQuestionPosition(points []string) (int, bool) {
	if len(points) == 0 {
		return 0, false
	}
	last := strings.TrimSpace(points[len(points)-1])
	last = strings.TrimPrefix(last, "题位")
	position, err := strconv.Atoi(last)
	return position, err == nil && position > 0
}

// IsEnglishPartB identifies the Part B (新题型) questions of either English
// paper. Their answer choices are spread over a shared option list (A-G) rather
// than the usual per-question A-D.
func IsEnglishPartB(points []string) bool {
	if len(points) < 3 || !IsEnglishSubject(points[0]) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(points[1]), "阅读理解") &&
		strings.EqualFold(strings.TrimSpace(points[2]), "Part B")
}

// IsEnglish2PartB identifies the matching-task questions whose answer choices
// are A-G rather than the usual A-D.
func IsEnglish2PartB(points []string) bool {
	if len(points) < 3 {
		return false
	}
	subject := strings.ToLower(strings.TrimSpace(points[0]))
	return (subject == "english2" || subject == "英语二") && IsEnglishPartB(points)
}

// EnglishPartBTaskLabel names the Part B task generated for a subject. English I
// uses the sentence-insertion task (七选五, five gaps filled from seven
// sentences), English II the named-viewpoint matching task.
func EnglishPartBTaskLabel(subject string) string {
	subject = strings.ToLower(strings.TrimSpace(subject))
	if subject == "english1" || subject == "英语一" {
		return "七选五"
	}
	return "人物观点匹配"
}

// IsEnglishPartBTaskLabel reports whether the label names the Part B task of the
// given subject, so validation can reject a mismatched saved paper.
func IsEnglishPartBTaskLabel(subject, label string) bool {
	label = strings.TrimSpace(label)
	if label == "" {
		return false
	}
	want := EnglishPartBTaskLabel(subject)
	if label == want {
		return true
	}
	// Older saved English II papers used the shorter label.
	return want == "人物观点匹配" && label == "观点匹配"
}

// IsEnglish2PartBMatchType accepts the canonical label and the shorter label
// used by older saved papers. Both describe the 2026 named-viewpoint task;
// paragraph-heading labels are intentionally excluded.
func IsEnglish2PartBMatchType(value string) bool {
	value = strings.TrimSpace(value)
	return value == "人物观点匹配" || value == "观点匹配"
}

// CognitiveLevel maps to Bloom's taxonomy.
type CognitiveLevel string

const (
	CogRemember   CognitiveLevel = "remember"
	CogUnderstand CognitiveLevel = "understand"
	CogApply      CognitiveLevel = "apply"
	CogAnalyze    CognitiveLevel = "analyze"
	CogEvaluate   CognitiveLevel = "evaluate"
	CogCreate     CognitiveLevel = "create"
)

// DifficultyBand is a banded difficulty target, not a point value.
// This lets CP-SAT enforce a histogram while allowing ±1 band slack per item.
type DifficultyBand int

const (
	BandEasy   DifficultyBand = 0
	BandMedium DifficultyBand = 1
	BandHard   DifficultyBand = 2
)

// PastQuestion is an annotated real-exam question.
// Annotations come from LLM (knowledge/题型/认知层) + objective anchor (difficulty).
// Answer/Options/Stem come from structured markers in the Markdown source.
type PastQuestion struct {
	ID                   string         `json:"id"`
	Subject              string         `json:"subject"`
	Year                 int            `json:"year"`
	Type                 QuestionType   `json:"type"`
	Points               []string       `json:"points"` // knowledge-point IDs
	Cognitive            CognitiveLevel `json:"cognitive"`
	Difficulty           float64        `json:"difficulty"`           // IRT b if available, else LLM-estimated
	DifficultyConfidence float64        `json:"difficultyConfidence"` // [0,1], LLM 标注置信度
	Score                float64        `json:"score"`                // 分值
	Source               string         `json:"source"`               // 真题来源(数一/数二/数三/408)
	TemplateKey          string         `json:"templateKey"`          // (题型, 知识点族) 模板键
	Stem                 string         `json:"stem"`                 // 题面原文(few-shot示例库来源)
	Answer               string         `json:"answer,omitempty"`     // 标准答案(【答案】提取)
	Options              []string       `json:"options,omitempty"`    // 选择题选项(A/B/C/D)
}

// SpecEntry is one row of the CP-SAT spec table (生成前双向细目表).
type SpecEntry struct {
	ID          string         `json:"id"`
	Index       int            `json:"index"` // 题位序
	Type        QuestionType   `json:"type"`
	Points      []string       `json:"points"` // 知识点子图
	Cognitive   CognitiveLevel `json:"cognitive"`
	DiffBand    DifficultyBand `json:"diffBand"`
	Score       float64        `json:"score"`
	TemplateKey string         `json:"templateKey"`
}

// SpecTable is the CP-SAT output — the blueprint before generation.
type SpecTable struct {
	Entries                []SpecEntry            `json:"entries"`
	TotalScore             float64                `json:"totalScore"`
	Subject                string                 `json:"subject"`
	DifficultyTarget       map[DifficultyBand]int `json:"difficultyTarget,omitempty"`
	DifficultyTargetStrict bool                   `json:"difficultyTargetStrict,omitempty"`
}

// BandFromDifficulty is the single conversion used by historical-question
// annotations, generated outputs, validation, and report rendering.
func BandFromDifficulty(value float64) DifficultyBand {
	switch {
	case value < 0.35:
		return BandEasy
	case value < 0.65:
		return BandMedium
	default:
		return BandHard
	}
}

// GeneratedQuestion is a question produced by the LLM generator.
type GeneratedQuestion struct {
	ID             string       `json:"id"`
	SpecID         string       `json:"specId"`            // links back to SpecEntry
	GroupID        string       `json:"groupId,omitempty"` // shared-material group for English composite tasks
	SharedMaterial string       `json:"sharedMaterial,omitempty"`
	SharedOptions  []string     `json:"sharedOptions,omitempty"`
	Stem           string       `json:"stem"`              // 题面(LaTeX)
	Options        []string     `json:"options,omitempty"` // 选择题
	Answer         string       `json:"answer"`            // 标准答案(LaTeX)
	Type           QuestionType `json:"type"`
	Points         []string     `json:"points"`
	Score          float64      `json:"score"`              // 分值(从spec带入,校验/装配用)
	Difficulty     float64      `json:"difficulty"`         // 实测难度(校验后)
	Status         string       `json:"status,omitempty"`   // ready / needs_review
	Warnings       []string     `json:"warnings,omitempty"` // 生成质量或外部依赖告警
}

// ExamPaper is the final assembled deliverable.
type ExamPaper struct {
	ID         string              `json:"id"`
	Subject    string              `json:"subject"`
	Questions  []GeneratedQuestion `json:"questions"`
	SpecTable  SpecTable           `json:"specTable"` // 生成后细目表
	TotalScore float64             `json:"totalScore"`
}
