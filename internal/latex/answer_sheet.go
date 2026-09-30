package latex

import "fmt"

type AnswerSheetSpec struct {
	Title               string
	CardName            string
	ChoiceSection       string
	ChoiceNums          []int
	ChoiceLabels        []string
	ChoicePageSize      int
	FillSection         string
	FillNums            []int
	MajorSection        string
	MajorNums           []int
	CandidateNumCols    int
	BarcodeOnFirstMajor bool
}

type answerSheetTemplateData struct {
	Title            string
	CardName         string
	CandidateColumns []candidateColumn
	DigitRows        [][]candidateDigit
	FirstChoice      *choicePageTemplateData
	ExtraChoicePages []choicePageTemplateData
	FillSection      string
	FillPages        []fillPageTemplateData
	MajorSection     string
	MajorPages       []majorPageTemplateData
	HasNonObjective  bool
}

type candidateColumn struct{}

type candidateDigit struct {
	Digit   int
	First   bool
	HasNext bool
}

type bubbleRowTemplateData struct {
	Number int
	Labels []string
}

type bubbleGroupTemplateData struct {
	Labels []string
	Rows   []bubbleRowTemplateData
}

type choicePageTemplateData struct {
	Section string
	Groups  []bubbleGroupTemplateData
}

type fillPageTemplateData struct {
	Number     int
	BreakAfter bool
}

type majorPageTemplateData struct {
	Number       int
	Barcode      bool
	Continuation bool
	BreakAfter   bool
}

func AnswerSheet(spec AnswerSheetSpec) string {
	choicePageSize := spec.ChoicePageSize
	if choicePageSize <= 0 {
		choicePageSize = 40
	}
	labels := append([]string(nil), spec.ChoiceLabels...)
	if len(labels) == 0 {
		labels = []string{"A", "B", "C", "D"}
	}
	candidateColumns := spec.CandidateNumCols
	if candidateColumns <= 0 {
		candidateColumns = 15
	}
	data := answerSheetTemplateData{
		Title:            escapeLaTeX(spec.Title),
		CardName:         escapeLaTeX(spec.CardName),
		CandidateColumns: make([]candidateColumn, candidateColumns),
		DigitRows:        make([][]candidateDigit, 10),
		FillSection:      escapeLaTeX(spec.FillSection),
		MajorSection:     escapeLaTeX(spec.MajorSection),
		HasNonObjective:  len(spec.FillNums) > 0 || len(spec.MajorNums) > 0,
	}
	for digit := 0; digit <= 9; digit++ {
		row := make([]candidateDigit, candidateColumns)
		for i := range row {
			row[i] = candidateDigit{Digit: digit, First: i == 0, HasNext: i < candidateColumns-1}
		}
		data.DigitRows[digit] = row
	}
	if len(spec.ChoiceNums) > 0 {
		end := min(choicePageSize, len(spec.ChoiceNums))
		first := choicePage(spec.ChoiceSection, spec.ChoiceNums[:end], labels)
		data.FirstChoice = &first
		for start := choicePageSize; start < len(spec.ChoiceNums); start += choicePageSize {
			end := min(start+choicePageSize, len(spec.ChoiceNums))
			data.ExtraChoicePages = append(data.ExtraChoicePages,
				choicePage(spec.ChoiceSection, spec.ChoiceNums[start:end], labels))
		}
	}
	for i, number := range spec.FillNums {
		data.FillPages = append(data.FillPages, fillPageTemplateData{
			Number:     number,
			BreakAfter: i < len(spec.FillNums)-1 || len(spec.MajorNums) > 0,
		})
	}
	for i, number := range spec.MajorNums {
		data.MajorPages = append(data.MajorPages, majorPageTemplateData{
			Number:       number,
			Barcode:      i == 0 && spec.BarcodeOnFirstMajor,
			Continuation: i > 0 && (i+1)%2 == 0,
			BreakAfter:   i < len(spec.MajorNums)-1,
		})
	}
	return renderTemplate("answer_sheet", data)
}

func choicePageSection(section string, nums []int) string {
	if len(nums) == 0 {
		return escapeLaTeX(section)
	}
	return fmt.Sprintf("%s（本页 %d～%d 题）", escapeLaTeX(section), nums[0], nums[len(nums)-1])
}

func choicePage(section string, nums []int, labels []string) choicePageTemplateData {
	const perGroup = 10
	page := choicePageTemplateData{Section: choicePageSection(section, nums)}
	for start := 0; start < len(nums); start += perGroup {
		end := min(start+perGroup, len(nums))
		group := bubbleGroupTemplateData{Labels: append([]string(nil), labels...)}
		for _, number := range nums[start:end] {
			group.Rows = append(group.Rows, bubbleRowTemplateData{Number: number, Labels: group.Labels})
		}
		page.Groups = append(page.Groups, group)
	}
	return page
}
