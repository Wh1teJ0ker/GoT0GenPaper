// Package models defines the core domain types shared across all pipeline stages.
// These structs mirror the concepts described in the project documentation.
package models

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
	Entries    []SpecEntry `json:"entries"`
	TotalScore float64     `json:"totalScore"`
	Subject    string      `json:"subject"`
}

// GeneratedQuestion is a question produced by the LLM generator.
type GeneratedQuestion struct {
	ID         string       `json:"id"`
	SpecID     string       `json:"specId"`            // links back to SpecEntry
	Stem       string       `json:"stem"`              // 题面(LaTeX)
	Options    []string     `json:"options,omitempty"` // 选择题
	Answer     string       `json:"answer"`            // 标准答案(LaTeX)
	Type       QuestionType `json:"type"`
	Points     []string     `json:"points"`
	Score      float64      `json:"score"`              // 分值(从spec带入,校验/装配用)
	Difficulty float64      `json:"difficulty"`         // 实测难度(校验后)
	Status     string       `json:"status,omitempty"`   // ready / needs_review
	Warnings   []string     `json:"warnings,omitempty"` // 生成质量或外部依赖告警
}

// RubricItem is one scoring checkpoint in a rubric.
type RubricItem struct {
	ID          string  `json:"id"`
	Description string  `json:"description"` // e.g. "设变量"
	MaxScore    float64 `json:"maxScore"`
}

// Rubric is the versioned scoring standard shared across answer and grading stages.
type Rubric struct {
	ID                string       `json:"id"`
	QuestionID        string       `json:"questionId"`
	Items             []RubricItem `json:"items"`
	AcceptableMethods []string     `json:"acceptableMethods"` // 可接受解法族
	Version           int          `json:"version"`
}

// GradingResult is the per-checkpoint verdict for a student's answer.
type GradingResult struct {
	RubricItemID string  `json:"rubricItemId"`
	Passed       bool    `json:"passed"`
	PartialScore float64 `json:"partialScore"`
	Note         string  `json:"note"` // 误差来源标注
}

// ExamPaper is the final assembled deliverable.
type ExamPaper struct {
	ID         string              `json:"id"`
	Subject    string              `json:"subject"`
	Questions  []GeneratedQuestion `json:"questions"`
	SpecTable  SpecTable           `json:"specTable"` // 生成后细目表
	Rubrics    []Rubric            `json:"rubrics"`
	TotalScore float64             `json:"totalScore"`
}
