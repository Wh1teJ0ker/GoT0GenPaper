package scan

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
)

// choicePosition is test data only. Production scanning must obtain bubble
// centers from the printed row/column anchors in the current image.
func choicePosition(slot, option int) (float64, float64) {
	group := slot / 10
	row := slot % 10
	groupStarts := [...]float64{62.57, 145.30, 228.02, 310.75}
	if group >= len(groupStarts) {
		group = len(groupStarts) - 1
	}
	return groupStarts[group] + 15.23*float64(option), 470.12 + 17.61*float64(row)
}

func TestClassifyBubbles(t *testing.T) {
	blank := []bubbleObservation{{"A", .12}, {"B", .14}, {"C", .10}, {"D", .13}}
	selected, _, status, _ := classifyBubbles(blank, false)
	if status != StatusBlank || len(selected) != 0 {
		t.Fatalf("blank = %v, %s", selected, status)
	}

	one := []bubbleObservation{{"A", .78}, {"B", .12}, {"C", .13}, {"D", .11}}
	selected, _, status, _ = classifyBubbles(one, false)
	if status != StatusRecognized || len(selected) != 1 || selected[0] != "A" {
		t.Fatalf("single = %v, %s", selected, status)
	}

	many := []bubbleObservation{{"A", .78}, {"B", .74}, {"C", .12}, {"D", .11}}
	selected, _, status, _ = classifyBubbles(many, false)
	if status != StatusAmbiguous || len(selected) != 2 {
		t.Fatalf("multi mark = %v, %s", selected, status)
	}
}

func TestRunCalibratesBlankPNGAndJPEG(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "page-1.png"), filepath.Join(dir, "page-2.jpg")}
	for i, path := range paths {
		im := image.NewGray(image.Rect(0, 0, 1200, 1700))
		for y := 0; y < im.Bounds().Dy(); y++ {
			for x := 0; x < im.Bounds().Dx(); x++ {
				im.SetGray(x, y, color.Gray{Y: 255})
			}
		}
		// Four registration marks at the same normalized positions as the
		// generated A4 template; the second page is intentionally also blank.
		for _, rect := range []image.Rectangle{
			image.Rect(12, 91, 45, 110), image.Rect(1175, 91, 1208, 110),
			image.Rect(12, 1622, 45, 1641), image.Rect(1175, 1622, 1208, 1641),
		} {
			for y := rect.Min.Y; y < rect.Max.Y; y++ {
				for x := rect.Min.X; x < rect.Max.X; x++ {
					im.SetGray(x, y, color.Gray{Y: 0})
				}
			}
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			err = png.Encode(f, im)
		} else {
			err = jpeg.Encode(f, im, &jpeg.Options{Quality: 95})
		}
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	paper := models.ExamPaper{ID: "p1", Subject: "408", Questions: []models.GeneratedQuestion{
		{ID: "q1", Type: models.TypeChoice, Score: 2, Answer: "正确选项为 A"},
		{ID: "q2", Type: models.TypeChoice, Score: 2, Answer: "正确选项为 B"},
		{ID: "q3", Type: models.TypeMajor, Score: 10},
	}}
	result, err := Run(context.Background(), paper, paths, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pages) != 2 || !result.Pages[0].Calibrated || result.Pages[0].MarkerCount < 4 {
		t.Fatalf("pages = %+v", result.Pages)
	}
	if len(result.Objective) != 2 || result.Objective[0].Status != StatusNeedsReview || result.Objective[1].Status != StatusNeedsReview {
		t.Fatalf("objective = %+v", result.Objective)
	}
	if len(result.Subjective) != 1 || result.Subjective[0].Status != StatusNeedsReview || result.Subjective[0].VisionStatus != "pending" {
		t.Fatalf("subjective = %+v", result.Subjective)
	}
	report := GradeScan(paper, *result)
	if report.TotalScore != 0 || len(report.NeedsReview) == 0 {
		t.Fatalf("grade report = %+v", report)
	}
}

func TestRunKeepsEmptySubjectiveListAsJSONArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objective-only.png")
	im := image.NewGray(image.Rect(0, 0, 400, 500))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
	paper := models.ExamPaper{Questions: []models.GeneratedQuestion{{ID: "q1", Type: models.TypeChoice, Score: 2}}}
	result, err := Run(context.Background(), paper, []string{path}, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Subjective == nil {
		t.Fatal("subjective list must be non-nil for stable JSON output")
	}
}

func TestRunDoesNotUseLegacyCoordinatesWhenLayoutIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordinate-trap.png")
	im := image.NewGray(image.Rect(0, 0, 1200, 1700))
	for y := 0; y < im.Bounds().Dy(); y++ {
		for x := 0; x < im.Bounds().Dx(); x++ {
			im.SetGray(x, y, color.Gray{Y: 255})
		}
	}
	for _, rect := range []image.Rectangle{
		image.Rect(12, 91, 45, 110), image.Rect(1175, 91, 1208, 110),
		image.Rect(12, 1622, 45, 1641), image.Rect(1175, 1622, 1208, 1641),
	} {
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				im.SetGray(x, y, color.Gray{Y: 0})
			}
		}
	}
	// This is deliberately placed at the old canonical A position, but there
	// is no machine-readable row/column structure in the scan. It must never
	// become an automatically recognized answer.
	xpt, ypt := choicePosition(0, 0)
	cx := int(xpt / pageWidthPT * 1200)
	cy := int(ypt / pageHeightPT * 1700)
	for yy := cy - 10; yy <= cy+10; yy++ {
		for xx := cx - 10; xx <= cx+10; xx++ {
			if (xx-cx)*(xx-cx)+(yy-cy)*(yy-cy) <= 100 {
				im.SetGray(xx, yy, color.Gray{Y: 0})
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	paper := models.ExamPaper{Questions: []models.GeneratedQuestion{{ID: "q1", Type: models.TypeChoice, Score: 2, Answer: "答案为 A"}}}
	result, err := Run(context.Background(), paper, []string{path}, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Pages[0].LayoutFound || result.Objective[0].Status != StatusNeedsReview || len(result.Objective[0].Selected) != 0 {
		t.Fatalf("coordinate fallback produced an answer: page=%+v answer=%+v", result.Pages[0], result.Objective[0])
	}
}

func TestRunRequiresCalibration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewGray(image.Rect(0, 0, 400, 500))); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
	paper := models.ExamPaper{Questions: []models.GeneratedQuestion{{ID: "q1", Type: models.TypeChoice, Score: 2}}}
	result, err := Run(context.Background(), paper, []string{path}, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Pages[0].Calibrated || result.Objective[0].Status != StatusNeedsReview {
		t.Fatalf("uncalibrated result = %+v", result)
	}
}

func TestObjectiveKeyDoesNotReadOptionText(t *testing.T) {
	key := objectiveAnswerKey(models.GeneratedQuestion{Answer: "解法中 A、B、C 均为中间变量，正确选项为 D。"})
	if len(key) != 1 || key[0] != "D" {
		t.Fatalf("key = %v", key)
	}
}

func TestRunRecognizesFilledBubbleAfterCalibration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filled.png")
	im := image.NewGray(image.Rect(0, 0, 1200, 1700))
	for y := 0; y < im.Bounds().Dy(); y++ {
		for x := 0; x < im.Bounds().Dx(); x++ {
			im.SetGray(x, y, color.Gray{Y: 255})
		}
	}
	for _, rect := range []image.Rectangle{
		image.Rect(12, 91, 45, 110), image.Rect(1175, 91, 1208, 110),
		image.Rect(12, 1622, 45, 1641), image.Rect(1175, 1622, 1208, 1641),
	} {
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				im.SetGray(x, y, color.Gray{Y: 0})
			}
		}
	}
	// A filled A bubble in the first slot, expressed through the template's
	// canonical page coordinates rather than a hard-coded scan pixel.
	xpt, ypt := choicePosition(0, 0)
	x := int(xpt / pageWidthPT * 1200)
	y := int(ypt / pageHeightPT * 1700)
	for yy := y - 8; yy <= y+8; yy++ {
		for xx := x - 8; xx <= x+8; xx++ {
			if (xx-x)*(xx-x)+(yy-y)*(yy-y) <= 64 {
				im.SetGray(xx, yy, color.Gray{Y: 0})
			}
		}
	}
	// The revised answer sheet prints a five-anchor header and one row anchor.
	// Their positions are intentionally allowed to differ from the old
	// template's fixed bubble coordinates; the scanner should recover them from
	// the page instead of assuming these values.
	drawAnchor := func(xpt, ypt float64) {
		cx := int(xpt / pageWidthPT * 1200)
		cy := int(ypt / pageHeightPT * 1700)
		for yy := cy - 5; yy <= cy+5; yy++ {
			for xx := cx - 5; xx <= cx+5; xx++ {
				im.SetGray(xx, yy, color.Gray{Y: 0})
			}
		}
	}
	for _, anchorX := range []float64{45, 62.57, 77.80, 93.03, 108.26} {
		drawAnchor(anchorX, 450)
	}
	drawAnchor(45, ypt)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
	paper := models.ExamPaper{Questions: []models.GeneratedQuestion{{ID: "q1", Type: models.TypeChoice, Score: 2, Answer: "答案为 A"}}}
	result, err := Run(context.Background(), paper, []string{path}, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Objective) != 1 || result.Objective[0].Status != StatusRecognized || len(result.Objective[0].Selected) != 1 || result.Objective[0].Selected[0] != "A" {
		t.Fatalf("filled bubble = %+v", result.Objective)
	}
	if len(result.Segments) != 1 || result.Segments[0].ImagePath == "" {
		t.Fatalf("filled bubble segment = %+v", result.Segments)
	}
	if _, err := os.Stat(result.Segments[0].ImagePath); err != nil {
		t.Fatalf("segment image missing: %v", err)
	}
}

func TestRunFollowsShiftedAnchorLayout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shifted.png")
	im := image.NewGray(image.Rect(0, 0, 1200, 1700))
	for y := 0; y < im.Bounds().Dy(); y++ {
		for x := 0; x < im.Bounds().Dx(); x++ {
			im.SetGray(x, y, color.Gray{Y: 255})
		}
	}
	for _, rect := range []image.Rectangle{
		image.Rect(12, 91, 45, 110), image.Rect(1175, 91, 1208, 110),
		image.Rect(12, 1622, 45, 1641), image.Rect(1175, 1622, 1208, 1641),
	} {
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				im.SetGray(x, y, color.Gray{Y: 0})
			}
		}
	}
	draw := func(xpt, ypt float64, radius int) {
		cx := int(xpt / pageWidthPT * 1200)
		cy := int(ypt / pageHeightPT * 1700)
		for yy := cy - radius; yy <= cy+radius; yy++ {
			for xx := cx - radius; xx <= cx+radius; xx++ {
				if radius == 5 || (xx-cx)*(xx-cx)+(yy-cy)*(yy-cy) <= radius*radius {
					im.SetGray(xx, yy, color.Gray{Y: 0})
				}
			}
		}
	}
	// All machine-readable anchors are shifted from the old template. The
	// filled A mark is placed at the shifted A column, not at choicePosition.
	for _, xpt := range []float64{35, 52, 69, 86, 103} {
		draw(xpt, 450, 5)
	}
	draw(35, 478, 5)
	draw(52, 478, 5)
	draw(52, 478, 8)

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
	paper := models.ExamPaper{Questions: []models.GeneratedQuestion{{ID: "q1", Type: models.TypeChoice, Score: 2, Answer: "答案为 A"}}}
	result, err := Run(context.Background(), paper, []string{path}, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Objective) != 1 || result.Objective[0].Status != StatusRecognized || len(result.Objective[0].Selected) != 1 || result.Objective[0].Selected[0] != "A" {
		t.Fatalf("shifted anchor result = %+v", result.Objective)
	}
}

func TestRunRecoversResizedInsetPageAndMildPerspective(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inset-perspective.png")
	const width, height = 1400, 1900
	im := image.NewGray(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			im.SetGray(x, y, color.Gray{Y: 255})
		}
	}

	// The sheet occupies only part of the image, with white margins and mild
	// projective distortion. All marks and bubbles share the transform; the
	// scanner must recover the current page geometry before reading answers.
	project := func(xpt, ypt float64) (int, int) {
		x := xpt / pageWidthPT
		y := ypt / pageHeightPT
		denom := 1 + 0.018*x - 0.014*y
		x = (0.15 + 0.70*x + 0.012*y) / denom
		y = (0.10 + 0.008*x + 0.78*y) / denom
		return int(x * width), int(y * height)
	}
	drawRect := func(xpt, ypt float64, halfW, halfH int) {
		cx, cy := project(xpt, ypt)
		for yy := cy - halfH; yy <= cy+halfH; yy++ {
			for xx := cx - halfW; xx <= cx+halfW; xx++ {
				if xx >= 0 && xx < width && yy >= 0 && yy < height {
					im.SetGray(xx, yy, color.Gray{Y: 0})
				}
			}
		}
	}
	for _, p := range []point{
		{14, 45.4}, {574, 45.4}, {14, 807.4}, {574, 807.4},
	} {
		drawRect(p.X, p.Y, 17, 10)
	}
	for _, xpt := range []float64{45, 62.57, 77.80, 93.03, 108.26} {
		drawRect(xpt, 450, 5, 5)
	}
	drawRect(45, 470.12, 5, 5)
	// Filled answer A for the first row. The row anchor is the first column;
	// the answer itself is the second column.
	cx, cy := project(62.57, 470.12)
	for yy := cy - 9; yy <= cy+9; yy++ {
		for xx := cx - 9; xx <= cx+9; xx++ {
			if (xx-cx)*(xx-cx)+(yy-cy)*(yy-cy) <= 81 {
				im.SetGray(xx, yy, color.Gray{Y: 0})
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	paper := models.ExamPaper{Questions: []models.GeneratedQuestion{{ID: "q1", Type: models.TypeChoice, Score: 2, Answer: "答案为 A"}}}
	result, err := Run(context.Background(), paper, []string{path}, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pages) != 1 || !result.Pages[0].Calibrated || !result.Pages[0].LayoutFound {
		t.Fatalf("inset-page calibration/layout failed: %+v", result.Pages)
	}
	if len(result.Objective) != 1 || result.Objective[0].Status != StatusRecognized || len(result.Objective[0].Selected) != 1 || result.Objective[0].Selected[0] != "A" {
		t.Fatalf("inset-page answer = %+v", result.Objective)
	}
}

func TestRunRecoversQuarterTurnScanFromRelativeStructure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quarter-turn.png")
	const sourceWidth, sourceHeight = 1200, 1700
	source := image.NewGray(image.Rect(0, 0, sourceWidth, sourceHeight))
	for y := 0; y < sourceHeight; y++ {
		for x := 0; x < sourceWidth; x++ {
			source.SetGray(x, y, color.Gray{Y: 255})
		}
	}
	drawCanonical := func(xpt, ypt float64, halfW, halfH int) {
		cx := int(xpt / pageWidthPT * sourceWidth)
		cy := int(ypt / pageHeightPT * sourceHeight)
		for yy := cy - halfH; yy <= cy+halfH; yy++ {
			for xx := cx - halfW; xx <= cx+halfW; xx++ {
				if xx >= 0 && xx < sourceWidth && yy >= 0 && yy < sourceHeight {
					source.SetGray(xx, yy, color.Gray{Y: 0})
				}
			}
		}
	}
	for _, p := range []point{
		{14, 45.4}, {574, 45.4}, {14, 807.4}, {574, 807.4},
	} {
		drawCanonical(p.X, p.Y, 16, 9)
	}
	for _, xpt := range []float64{45, 62.57, 77.80, 93.03, 108.26} {
		drawCanonical(xpt, 450, 5, 5)
	}
	drawCanonical(45, 470.12, 5, 5)
	// The answer is deliberately expressed in the page's current structure.
	drawCanonical(62.57, 470.12, 9, 9)

	rotated := image.NewGray(image.Rect(0, 0, sourceHeight, sourceWidth))
	for y := 0; y < sourceHeight; y++ {
		for x := 0; x < sourceWidth; x++ {
			rotated.SetGray(sourceHeight-1-y, x, source.GrayAt(x, y))
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, rotated); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	paper := models.ExamPaper{Questions: []models.GeneratedQuestion{{ID: "q1", Type: models.TypeChoice, Score: 2, Answer: "答案为 A"}}}
	result, err := Run(context.Background(), paper, []string{path}, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pages) != 1 || !result.Pages[0].Calibrated || !result.Pages[0].LayoutFound {
		t.Fatalf("quarter-turn calibration/layout failed: %+v", result.Pages)
	}
	if len(result.Objective) != 1 || result.Objective[0].Status != StatusRecognized || len(result.Objective[0].Selected) != 1 || result.Objective[0].Selected[0] != "A" {
		t.Fatalf("quarter-turn answer = %+v", result.Objective)
	}
}

func TestGradeScanSeparatesKnownAndUnknownQuestionIDs(t *testing.T) {
	paper := models.ExamPaper{Questions: []models.GeneratedQuestion{
		{ID: "q1", Type: models.TypeChoice, Score: 2, Answer: "答案为 A"},
		{ID: "q2", Type: models.TypeChoice, Score: 2, Answer: "答案为 B"},
	}}
	report := GradeScan(paper, ScanResult{Pages: []PageScanResult{{Page: 1, Calibrated: true, LayoutFound: true}}, Objective: []ObjectiveAnswer{
		{QuestionID: "q1", Selected: []string{"A"}, Confidence: 1, Status: StatusRecognized},
		{QuestionID: "q-unknown", Selected: []string{"A"}, Status: StatusRecognized},
	}})
	if report.TotalScore != 2 {
		t.Fatalf("total score = %.1f, want 2", report.TotalScore)
	}
	if !containsString(report.NeedsReview, "q2") || !containsString(report.NeedsReview, "q-unknown") {
		t.Fatalf("needs review = %v", report.NeedsReview)
	}
	if !containsWarning(report.Warnings, "未知客观题 q-unknown") {
		t.Fatalf("warnings = %v", report.Warnings)
	}
}

func TestGradeScanRejectsDuplicateQuestionResults(t *testing.T) {
	paper := models.ExamPaper{Questions: []models.GeneratedQuestion{
		{ID: "q1", Type: models.TypeChoice, Score: 2, Answer: "答案为 A"},
	}}
	report := GradeScan(paper, ScanResult{Objective: []ObjectiveAnswer{
		{QuestionID: "q1", Selected: []string{"A"}, Confidence: 1, Status: StatusRecognized},
		{QuestionID: "q1", Selected: []string{"B"}, Confidence: 1, Status: StatusRecognized},
	}})
	if report.TotalScore != 0 || !containsString(report.NeedsReview, "q1") {
		t.Fatalf("duplicate report = %+v", report)
	}
	if len(report.Results) != 1 || report.Results[0].Status != StatusNeedsReview {
		t.Fatalf("duplicate result = %+v", report.Results)
	}
}

func TestGradeScanDoesNotAutoScoreLowConfidenceMark(t *testing.T) {
	paper := models.ExamPaper{Questions: []models.GeneratedQuestion{{ID: "q1", Type: models.TypeChoice, Score: 2, Answer: "答案为 A"}}}
	report := GradeScan(paper, ScanResult{Objective: []ObjectiveAnswer{{
		QuestionID: "q1", Selected: []string{"A"}, Confidence: 0.49, Status: StatusRecognized,
	}}})
	if report.TotalScore != 0 || len(report.Results) != 1 || report.Results[0].Status != StatusNeedsReview {
		t.Fatalf("low confidence report = %+v", report)
	}
}

func TestGradeScanRejectsMismatchedPaperFingerprint(t *testing.T) {
	paper := models.ExamPaper{ID: "paper-1", Subject: "408", Questions: []models.GeneratedQuestion{{ID: "q1", Type: models.TypeChoice, Score: 2, Answer: "答案为 A"}}}
	report := GradeScan(paper, ScanResult{
		PaperID:          paper.ID,
		PaperFingerprint: "sha256:wrong-layout",
		Pages:            []PageScanResult{{Page: 1, Calibrated: true, LayoutFound: true}},
		Objective:        []ObjectiveAnswer{{QuestionID: "q1", Selected: []string{"A"}, Confidence: 1, Status: StatusRecognized}},
	})
	if report.TotalScore != 0 || len(report.Results) != 1 || report.Results[0].Status != StatusNeedsReview {
		t.Fatalf("mismatched fingerprint was auto-scored: %+v", report)
	}
}

func TestGradeScanSurfacesSubjectiveAnomalies(t *testing.T) {
	paper := models.ExamPaper{Questions: []models.GeneratedQuestion{
		{ID: "major-1", Type: models.TypeMajor, Score: 10},
		{ID: "major-2", Type: models.TypeMajor, Score: 10},
	}}
	report := GradeScan(paper, ScanResult{Subjective: []SubjectiveAnswer{
		{QuestionID: "major-1", Number: 1},
		{QuestionID: "major-1", Number: 2},
		{QuestionID: "major-unknown", Number: 3},
		{QuestionID: "", Number: 4},
	}})
	if !containsString(report.NeedsReview, "major-1") || !containsString(report.NeedsReview, "major-unknown") || !containsString(report.NeedsReview, "major-2") {
		t.Fatalf("needs review = %v", report.NeedsReview)
	}
	if !containsWarning(report.Warnings, "重复扫描结果") || !containsWarning(report.Warnings, "未知主观题") || !containsWarning(report.Warnings, "缺少题号") || !containsWarning(report.Warnings, "缺少主观题 major-2") {
		t.Fatalf("warnings = %v", report.Warnings)
	}
	for _, result := range report.Results {
		if result.Status != StatusNeedsReview {
			t.Fatalf("subjective result was auto-scored: %+v", result)
		}
	}
}

func TestGradeScanRejectsInvalidPaperScore(t *testing.T) {
	paper := models.ExamPaper{Questions: []models.GeneratedQuestion{{ID: "q1", Type: models.TypeChoice, Score: math.NaN(), Answer: "答案为 A"}}}
	report := GradeScan(paper, ScanResult{Objective: []ObjectiveAnswer{{
		QuestionID: "q1", Selected: []string{"A"}, Confidence: 1, Status: StatusRecognized,
	}}})
	if report.MaxScore != 0 || report.TotalScore != 0 || !containsString(report.NeedsReview, "q1") {
		t.Fatalf("invalid score report = %+v", report)
	}
	if len(report.Results) != 1 || report.Results[0].Status != StatusNeedsReview {
		t.Fatalf("invalid score result = %+v", report.Results)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsWarning(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
