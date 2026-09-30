package scan

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"GoT0GenPaper/internal/models"
)

// buildObjectiveSegments writes one upright crop per objective answer. The
// crop bounds come from the row/column anchors detected in the current scan;
// they are not reused as an answer classifier.
func buildObjectiveSegments(g grayImage, h homography, sourcePath, dir string, page, start int, questions []models.GeneratedQuestion, layout choiceLayout) ([]AnswerSegment, error) {
	segments := make([]AnswerSegment, 0, len(questions))
	for slot, question := range questions {
		segment := AnswerSegment{
			ID:         fmt.Sprintf("p%d-q%d", page+1, start+slot+1),
			QuestionID: question.ID,
			Number:     start + slot + 1,
			Page:       page + 1,
			Kind:       "objective",
			SourcePath: sourcePath,
			Status:     StatusNeedsReview,
		}
		if slot >= len(layout.RowAnchors) || slot*len(optionLabels)+len(optionLabels)-1 >= len(layout.Centers) {
			segment.Note = "缺少当前扫描图中的完整题目行结构"
			segments = append(segments, segment)
			continue
		}
		row := layout.RowAnchors[slot]
		last := layout.Centers[slot*len(optionLabels)+len(optionLabels)-1]
		x0, x1 := row.X-10, last.X+10
		y0, y1 := row.Y-9, row.Y+9
		segment.X, segment.Y = x0, y0
		segment.Width, segment.Height = x1-x0, y1-y0
		segment.Status = StatusRecognized
		imagePath, err := writeCanonicalCrop(g, h, dir, segment.ID, x0, y0, x1, y1)
		if err != nil {
			return nil, err
		}
		segment.ImagePath = imagePath
		segments = append(segments, segment)
	}
	return segments, nil
}

// buildSubjectiveSegments produces a page-area crop for each subjective
// answer. The visual model receives the current page image after registration,
// The resulting crop is the only answer content sent to the multimodal model;
// no local OCR dependency is required.
func buildSubjectiveSegments(g grayImage, h homography, sourcePath, dir string, page, number int, questionID string) (AnswerSegment, error) {
	segment := AnswerSegment{
		ID:         fmt.Sprintf("p%d-q%d", page+1, number),
		QuestionID: questionID,
		Number:     number,
		Page:       page + 1,
		Kind:       "subjective",
		SourcePath: sourcePath,
		X:          24,
		Y:          48,
		Width:      pageWidthPT - 48,
		Height:     pageHeightPT - 72,
		Status:     StatusRecognized,
	}
	imagePath, err := writeCanonicalCrop(g, h, dir, segment.ID, segment.X, segment.Y, segment.X+segment.Width, segment.Y+segment.Height)
	if err != nil {
		return AnswerSegment{}, err
	}
	segment.ImagePath = imagePath
	return segment, nil
}

func writeCanonicalCrop(g grayImage, h homography, dir, id string, x0, y0, x1, y1 float64) (string, error) {
	if x1 <= x0 || y1 <= y0 {
		return "", fmt.Errorf("invalid segment bounds for %s", id)
	}
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "got0genpaper-segments-")
		if err != nil {
			return "", fmt.Errorf("create segment directory: %w", err)
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create segment directory %s: %w", dir, err)
	}
	const pixelsPerPoint = 2
	w := int((x1 - x0) * pixelsPerPoint)
	height := int((y1 - y0) * pixelsPerPoint)
	if w < 32 {
		w = 32
	}
	if height < 32 {
		height = 32
	}
	out := image.NewGray(image.Rect(0, 0, w, height))
	for y := 0; y < height; y++ {
		canonicalY := y0 + float64(y)/float64(height)*(y1-y0)
		for x := 0; x < w; x++ {
			canonicalX := x0 + float64(x)/float64(w)*(x1-x0)
			p := h.mapPoint(canonicalTargetPoint(canonicalX, canonicalY))
			sourceX := int(p.X * float64(g.W))
			sourceY := int(p.Y * float64(g.H))
			out.SetGray(x, y, color.Gray{Y: g.at(sourceX, sourceY)})
		}
	}
	path := filepath.Join(dir, id+".png")
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create segment %s: %w", id, err)
	}
	defer f.Close()
	if err := png.Encode(f, out); err != nil {
		return "", fmt.Errorf("encode segment %s: %w", id, err)
	}
	return path, nil
}
