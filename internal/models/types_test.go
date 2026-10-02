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
