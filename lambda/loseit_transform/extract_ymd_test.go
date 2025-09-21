package main

import "testing"

func TestExtractYMD(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected struct{ y, m, d string }
	}{
		{
			name:     "normal path",
			input:    "raw/loseit_csv/year=2025/month=08/day=27/example_report.csv",
			expected: struct{ y, m, d string }{"2025", "08", "27"},
		},
		{
			name:     "URL-encoded path",
			input:    "raw/loseit_csv/year%3D2025/month%3D09/day%3D21/Daily_Report_39644994_20250920.csv",
			expected: struct{ y, m, d string }{"2025", "09", "21"},
		},
		{
			name:     "no date info",
			input:    "raw/loseit_csv/some_file.csv",
			expected: struct{ y, m, d string }{"", "", ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			y, m, d := extractYMD(tt.input)
			if y != tt.expected.y || m != tt.expected.m || d != tt.expected.d {
				t.Errorf("extractYMD(%s) = (%s, %s, %s), want (%s, %s, %s)",
					tt.input, y, m, d, tt.expected.y, tt.expected.m, tt.expected.d)
			}
		})
	}
}
