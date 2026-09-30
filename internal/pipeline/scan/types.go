// Package scan reads photographed or scanned answer sheets.  The scanner is
// deliberately conservative: image geometry is calibrated from registration
// marks, while every answer is classified with an explicit confidence/status.
package scan

import "GoT0GenPaper/internal/models"

const (
	StatusRecognized  = "recognized"
	StatusBlank       = "blank"
	StatusAmbiguous   = "ambiguous"
	StatusNeedsReview = "needs_review"
)

type ScanRequest struct {
	PaperPath  string   `json:"paperPath"`
	ImagePaths []string `json:"imagePaths"`
	OutputPath string   `json:"outputPath,omitempty"`
}

type ScanOptions struct {
	// AllowUncalibrated is intended for diagnostics only. Production scans
	// should require registration marks because raw coordinates drift under
	// rotation, crop, and perspective distortion.
	AllowUncalibrated bool
	// SegmentsDir optionally receives upright PNG crops used by visual grading.
	// It is an audit artifact, never the source of question coordinates.
	SegmentsDir string
}

type PageScanResult struct {
	Page         int      `json:"page"`
	Path         string   `json:"path"`
	Width        int      `json:"width"`
	Height       int      `json:"height"`
	MarkerCount  int      `json:"markerCount"`
	Calibrated   bool     `json:"calibrated"`
	LayoutFound  bool     `json:"layoutFound"`
	LayoutScore  float64  `json:"layoutScore,omitempty"`
	RotationHint float64  `json:"rotationHint"`
	Status       string   `json:"status"`
	Warnings     []string `json:"warnings,omitempty"`
}

type ObjectiveAnswer struct {
	QuestionID string   `json:"questionId"`
	Number     int      `json:"number"`
	Selected   []string `json:"selected"`
	Confidence float64  `json:"confidence"`
	Status     string   `json:"status"`
	Note       string   `json:"note,omitempty"`
}

type SubjectiveAnswer struct {
	QuestionID   string `json:"questionId"`
	Number       int    `json:"number"`
	Page         int    `json:"page"`
	VisionStatus string `json:"visionStatus"`
	Status       string `json:"status"`
	Note         string `json:"note,omitempty"`
}

// AnswerSegment describes a crop measured from the current scan. Canonical
// bounds are only the requested crop area; the page transform is fitted from
// the current image's registration marks before pixels are sampled.
type AnswerSegment struct {
	ID         string  `json:"id"`
	QuestionID string  `json:"questionId"`
	Number     int     `json:"number"`
	Page       int     `json:"page"`
	Kind       string  `json:"kind"`
	SourcePath string  `json:"sourcePath"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Width      float64 `json:"width"`
	Height     float64 `json:"height"`
	Status     string  `json:"status"`
	ImagePath  string  `json:"imagePath,omitempty"`
	Note       string  `json:"note,omitempty"`
}

type ScanResult struct {
	PaperID          string             `json:"paperId"`
	PaperFingerprint string             `json:"paperFingerprint,omitempty"`
	Subject          string             `json:"subject"`
	Pages            []PageScanResult   `json:"pages"`
	Objective        []ObjectiveAnswer  `json:"objective"`
	Subjective       []SubjectiveAnswer `json:"subjective"`
	Segments         []AnswerSegment    `json:"segments"`
	Warnings         []string           `json:"warnings,omitempty"`
}

type ScanGradeResult struct {
	QuestionID string  `json:"questionId"`
	Number     int     `json:"number"`
	Score      float64 `json:"score"`
	MaxScore   float64 `json:"maxScore"`
	Status     string  `json:"status"`
	Note       string  `json:"note,omitempty"`
}

type ScanGradeReport struct {
	Scan        ScanResult        `json:"scan"`
	Results     []ScanGradeResult `json:"results"`
	TotalScore  float64           `json:"totalScore"`
	MaxScore    float64           `json:"maxScore"`
	NeedsReview []string          `json:"needsReview,omitempty"`
	Warnings    []string          `json:"warnings,omitempty"`
	Mode        string            `json:"mode,omitempty"`
}

func objectiveQuestions(paper models.ExamPaper) []models.GeneratedQuestion {
	var out []models.GeneratedQuestion
	for _, q := range paper.Questions {
		if q.Type == models.TypeChoice || q.Type == models.TypeMultiChoice {
			out = append(out, q)
		}
	}
	return out
}
