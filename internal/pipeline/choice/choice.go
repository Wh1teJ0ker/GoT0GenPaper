// Package choice provides strict parsing and validation for objective answers
// used by generation and paper-quality checks.
package choice

import (
	"regexp"
	"strings"
	"unicode"
)

var answerMarker = regexp.MustCompile(`(?i)(?:正确答案|正确选项|答案|选项|选择)\s*(?:为|是|[:：=])?\s*`)
var leadingAnswerMarker = regexp.MustCompile(`(?is)^\s*(?:(?:正确|参考)?答案|正确选项|选项|选择)\s*(?:为|是|[:：=])?\s*`)
var selectionMarker = regexp.MustCompile(`(?i)(?:故|所以|因此|故而|从而)?\s*选(?:择)?(?:项)?\s*(?:为|是|[:：=])?\s*\(?\s*([ABCD](?:\s*[,，、/和及或&]\s*[ABCD])*)\s*\)?\s*(?:[。.!！,，;；]|$)`)
var concludingChoice = regexp.MustCompile(`(?i)(?:故|所以|因此|故而|从而)\s*\(?\s*([ABCD])\s*\)?\s*(?:项)?\s*(?:正确|错误|对|错)?(?:[。.!！,，;；]|$)`)
var leadingChoice = regexp.MustCompile(`(?i)^\s*\(?\s*([ABCD])\s*\)?\s*[。.!！:：、，,]`)

// Parse extracts the final objective answer token after an explicit answer
// marker. It also accepts a standalone token such as "(A、C)".
func Parse(raw string) []string {
	return ParseWithAlphabet(raw, "ABCD")
}

// ParseWithAlphabet is the same strict answer parser as Parse, with an
// explicit alphabet for matching tasks such as English II Part B (A-G).
func ParseWithAlphabet(raw, alphabet string) []string {
	alphabet = strings.ToUpper(strings.TrimSpace(alphabet))
	if alphabet == "" {
		return nil
	}
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil
	}
	selectionMarker := regexp.MustCompile(`(?i)(?:故|所以|因此|故而|从而)?\s*选(?:择)?(?:项)?\s*(?:为|是|[:：=])?\s*\(?\s*([` + alphabet + `](?:\s*[,，、/和及或&]\s*[` + alphabet + `])*)\s*\)?\s*(?:[。.!！,，;；]|$)`)
	concludingChoice := regexp.MustCompile(`(?i)(?:故|所以|因此|故而|从而)\s*\(?\s*([` + alphabet + `])\s*\)?\s*(?:项)?\s*(?:正确|错误|对|错)?(?:[。.!！,，;；]|$)`)
	leadingChoice := regexp.MustCompile(`(?i)^\s*\(?\s*([` + alphabet + `])\s*\)?\s*[。.!！:：、，,]`)
	// Prefer an explicit answer label at the start. Explanations often discuss
	// "选项 A/B/C/D" later, and the last generic marker may refer to a distractor.
	if loc := leadingAnswerMarker.FindStringIndex(text); loc != nil {
		labels, _ := parseRun([]rune(text[loc[1]:]), alphabet)
		return labels
	}
	if matches := answerMarker.FindAllStringIndex(text, -1); len(matches) > 0 {
		end := matches[len(matches)-1][1]
		labels, _ := parseRun([]rune(text[end:]), alphabet)
		return labels
	}
	if matches := selectionMarker.FindAllStringSubmatch(text, -1); len(matches) > 0 {
		labels, _ := parseRun([]rune(matches[len(matches)-1][1]), alphabet)
		return labels
	}
	if matches := concludingChoice.FindAllStringSubmatch(text, -1); len(matches) > 0 {
		return []string{strings.ToUpper(matches[len(matches)-1][1])}
	}
	if match := leadingChoice.FindStringSubmatch(text); len(match) == 2 {
		return []string{strings.ToUpper(match[1])}
	}
	labels, consumed := parseRun([]rune(text), alphabet)
	if len(labels) == 0 || !standaloneTail([]rune(text)[consumed:]) {
		return nil
	}
	return labels
}

// Validate checks the cardinality and alphabet for a single- or multi-choice
// answer. It returns an empty string when the labels are safe to score.
func Validate(labels []string, multi bool) string {
	return ValidateWithAlphabet(labels, multi, "ABCD")
}

// ValidateWithAlphabet validates answer labels against the supplied option
// alphabet while preserving the single-/multi-answer cardinality checks.
func ValidateWithAlphabet(labels []string, multi bool, alphabet string) string {
	alphabet = strings.ToUpper(strings.TrimSpace(alphabet))
	if len(labels) == 0 {
		return "未检测到合法选项，需人工复核"
	}
	seen := make(map[string]struct{}, len(labels))
	for _, raw := range labels {
		label := strings.ToUpper(strings.TrimSpace(raw))
		if len([]rune(label)) != 1 || !strings.Contains(alphabet, label) {
			return "检测到未知选项，需人工复核"
		}
		if _, exists := seen[label]; exists {
			return "检测到重复选项，需人工复核"
		}
		seen[label] = struct{}{}
	}
	if multi {
		if len(labels) < 2 || len(labels) > 4 {
			return "多选题选项数量无效，需人工复核"
		}
		return ""
	}
	if len(labels) != 1 {
		return "单选题检测到多个答案，需人工复核"
	}
	return ""
}

// Same compares two already validated label sets without depending on order.
func Same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, label := range a {
		seen[strings.ToUpper(strings.TrimSpace(label))] = struct{}{}
	}
	for _, label := range b {
		if _, ok := seen[strings.ToUpper(strings.TrimSpace(label))]; !ok {
			return false
		}
	}
	return true
}

func parseRun(input []rune, alphabet string) ([]string, int) {
	labels := make([]string, 0, 4)
	i := 0
	for i < len(input) && unicode.IsSpace(input[i]) {
		i++
	}
	if i < len(input) && (input[i] == '(' || input[i] == '（') {
		i++
	}
	for i < len(input) {
		for i < len(input) && unicode.IsSpace(input[i]) {
			i++
		}
		if i >= len(input) {
			break
		}
		r := unicode.ToUpper(input[i])
		if strings.ContainsRune(alphabet, r) {
			labels = append(labels, string(r))
			i++
			continue
		}
		if strings.ContainsRune(",，、/和及或&", input[i]) {
			i++
			continue
		}
		if input[i] == ')' || input[i] == '）' {
			i++
			break
		}
		break
	}
	return labels, i
}

func standaloneTail(tail []rune) bool {
	for _, r := range tail {
		if unicode.IsSpace(r) || r == ')' || r == '）' || r == '.' || r == '。' || r == '、' {
			continue
		}
		return false
	}
	return true
}
