package choice

import (
	"reflect"
	"testing"
)

func TestParseChoiceAnswers(t *testing.T) {
	for _, test := range []struct {
		input string
		want  []string
	}{
		{"正确答案为 A", []string{"A"}},
		{"答案：A、C", []string{"A", "C"}},
		{"答案是 (a,c)", []string{"A", "C"}},
		{"AC", []string{"A", "C"}},
		{"解析含 A、B、C，正确选项为 D。", []string{"D"}},
		{"答案为 A,C", []string{"A", "C"}},
		{"答案为 A和C", []string{"A", "C"}},
		{"故选 B。", []string{"B"}},
		{"因此选择 A。", []string{"A"}},
		{"故 B 正确。", []string{"B"}},
		{"A。解题步骤：先判断定义。", []string{"A"}},
		{"答案：A\n题意：分析选项：\nA) grounded\nB) justified\nC) bolstered\nD) inspired", []string{"A"}},
		{"参考答案为 C。逐项分析选项：A 错误，B 错误，C 正确。", []string{"C"}},
	} {
		t.Run(test.input, func(t *testing.T) {
			if got := Parse(test.input); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Parse(%q) = %v, want %v", test.input, got, test.want)
			}
		})
	}
}

func TestValidateChoiceCardinalityAndDuplicates(t *testing.T) {
	if issue := Validate([]string{"A", "C"}, true); issue != "" {
		t.Fatalf("valid multi answer issue = %q", issue)
	}
	for _, labels := range [][]string{{"A"}, {"A", "A"}, {"A", "E"}, {"A", "B", "C", "D", "A"}} {
		if issue := Validate(labels, true); issue == "" {
			t.Errorf("Validate(%v, multi) accepted malformed answer", labels)
		}
	}
	if issue := Validate([]string{"A", "C"}, false); issue == "" {
		t.Fatal("single-choice answer accepted multiple labels")
	}
}

func TestParseAndValidateEnglish2PartBAlphabet(t *testing.T) {
	labels := ParseWithAlphabet("答案：G。根据段落中心论点，G 最匹配。", "ABCDEFG")
	if len(labels) != 1 || labels[0] != "G" {
		t.Fatalf("Part B answer labels = %v, want [G]", labels)
	}
	if issue := ValidateWithAlphabet(labels, false, "ABCDEFG"); issue != "" {
		t.Fatalf("valid Part B answer rejected: %s", issue)
	}
	if issue := Validate(labels, false); issue == "" {
		t.Fatal("A-D validator accepted a G answer")
	}
}
