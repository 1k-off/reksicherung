package restore

import "testing"

func TestFitsDiskBudget(t *testing.T) {
	tests := []struct {
		name           string
		containerBytes int64
		tempBytes      int64
		maxBytes       int64
		want           bool
	}{
		{name: "within ceiling", containerBytes: 100, tempBytes: 100, maxBytes: 200, want: true},
		{name: "exact ceiling", containerBytes: 150, tempBytes: 50, maxBytes: 200, want: true},
		{name: "over ceiling", containerBytes: 150, tempBytes: 51, maxBytes: 200},
		{name: "negative container", containerBytes: -1, tempBytes: 1, maxBytes: 200},
		{name: "negative temp", containerBytes: 1, tempBytes: -1, maxBytes: 200},
		{name: "non-positive ceiling", containerBytes: 1, tempBytes: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FitsDiskBudget(test.containerBytes, test.tempBytes, test.maxBytes); got != test.want {
				t.Fatalf("FitsDiskBudget(%d, %d, %d) = %t, want %t",
					test.containerBytes, test.tempBytes, test.maxBytes, got, test.want)
			}
		})
	}
}
