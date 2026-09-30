// Package choice provides one strict parser for A-D objective answers.
// Generation, OMR and text grading must agree on the answer shape.
package choice

import (
	"regexp"
	"strings"
	"unicode"
)

var answerMarker = regexp.MustCompile(`(?i)(?:正确答案|正确选项|答案|选项|选择)\s*(?:为|是|[:：=])?\s*`)
var selectionMarker = regexp.MustCompile(`(?i)(?:故|所以|因此|故而|从而)?\s*选(?:择)?(?:项)?\s*(?:为|是|[:：=])?\s*\(?\s*([ABCD](?:\s*[,，、/和及或&]\s*[ABCD])*)\s*\)?\s*(?:[。.!！,，;；]|$)`)
var concludingChoice = regexp.MustCompile(`(?i)(?:故|所以|因此|故而|从而)\s*\(?\s*([ABCD])\s*\)?\s*(?:项)?\s*(?:正确|错误|对|错)?(?:[。.!！,，;；]|$)`)
var leadingChoice = regexp.MustCompile(`(?i)^\s*\(?\s*([ABCD])\s*\)?\s*[。.!！:：、，,]`)

// Parse extracts the final objective answer token after an explicit answer
// marker. It also accepts a standalone token such as "(A、C)".
func Parse(raw string) []string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil
	}
	if matches := answerMarker.FindAllStringIndex(text, -1); len(matches) > 0 {
		end := matches[len(matches)-1][1]
		labels, _ := parseRun([]rune(text[end:]))
		return labels
	}
	if matches := selectionMarker.FindAllStringSubmatch(text, -1); len(matches) > 0 {
		labels, _ := parseRun([]rune(matches[len(matches)-1][1]))
		return labels
	}
	if matches := concludingChoice.FindAllStringSubmatch(text, -1); len(matches) > 0 {
		return []string{strings.ToUpper(matches[len(matches)-1][1])}
	}
	if match := leadingChoice.FindStringSubmatch(text); len(match) == 2 {
		return []string{strings.ToUpper(match[1])}
	}
	labels, consumed := parseRun([]rune(text))
	if len(labels) == 0 || !standaloneTail([]rune(text)[consumed:]) {
		return nil
	}
	return labels
}

// Validate checks the cardinality and alphabet for a single- or multi-choice
// answer. It returns an empty string when the labels are safe to score.
func Validate(labels []string, multi bool) string {
	if len(labels) == 0 {
		return "未检测到合法选项，需人工复核"
	}
	seen := make(map[string]struct{}, len(labels))
	for _, raw := range labels {
		label := strings.ToUpper(strings.TrimSpace(raw))
		if len([]rune(label)) != 1 || !strings.Contains("ABCD", label) {
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

func parseRun(input []rune) ([]string, int) {
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
		if r >= 'A' && r <= 'D' {
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
