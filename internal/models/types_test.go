package models

import "testing"

func TestBandFromDifficultyUsesStableBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		value float64
		want  DifficultyBand
	}{
		{name: "easy lower edge", value: 0, want: BandEasy},
		{name: "easy upper edge", value: 0.34, want: BandEasy},
		{name: "medium lower edge", value: 0.35, want: BandMedium},
		{name: "medium upper edge", value: 0.64, want: BandMedium},
		{name: "hard lower edge", value: 0.65, want: BandHard},
		{name: "hard upper edge", value: 1, want: BandHard},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BandFromDifficulty(tt.value); got != tt.want {
				t.Fatalf("BandFromDifficulty(%v) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func TestEnglishQuestionOrderMatchesOfficialSections(t *testing.T) {
	cases := []struct {
		name    string
		points  []string
		section int
		pos     int
	}{
		{"cloze", []string{"english2", "完形填空", "语篇词汇与语境", "题位1"}, 0, 1},
		{"reading", []string{"english2", "阅读理解", "Part A", "Text 1", "题位21"}, 1, 21},
		{"new type", []string{"english2", "阅读理解", "Part B", "人物观点匹配", "题位41"}, 2, 41},
		{"translation", []string{"english2", "翻译", "英译汉", "题位46"}, 3, 46},
		{"application", []string{"english2", "写作", "应用文", "题位47"}, 4, 47},
		{"essay", []string{"english2", "写作", "图表或主题作文", "题位48"}, 5, 48},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			section, position := EnglishQuestionOrder(tt.points)
			if section != tt.section || position != tt.pos {
				t.Fatalf("EnglishQuestionOrder(%v) = (%d, %d), want (%d, %d)", tt.points, section, position, tt.section, tt.pos)
			}
		})
	}
}

func TestEnglish2PartBMatchTypeAcceptsLegacyAliasOnly(t *testing.T) {
	if !IsEnglish2PartBMatchType("人物观点匹配") || !IsEnglish2PartBMatchType("观点匹配") {
		t.Fatal("named-viewpoint labels were rejected")
	}
	if IsEnglish2PartBMatchType("段落匹配") {
		t.Fatal("paragraph matching was accepted for 2026 English II Part B")
	}
}
