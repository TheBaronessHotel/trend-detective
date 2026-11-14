package prompt

import (
	"math"
	"testing"
)

func TestScanNumbers(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []float64
		alts    []float64 // parallel to want; NaN means no alt
		places  []int
		percent []bool
		sci     []bool
		raw     []string
	}{
		{
			name: "ints and signed decimals",
			in:   "a 42 b -0.5 c +3",
			want: []float64{42, -0.5, 3},
			raw:  []string{"42", "-0.5", "+3"},
		},
		{
			name: "unicode minus",
			in:   "value \u22120.5",
			want: []float64{-0.5},
			raw:  []string{"\u22120.5"},
		},
		{
			name: "comma decimal",
			in:   "p = 0,05",
			want: []float64{0.05},
			raw:  []string{"0,05"},
		},
		{
			name: "ambiguous comma",
			in:   "1,234",
			want: []float64{1.234},
			alts: []float64{1234},
			raw:  []string{"1,234"},
		},
		{
			name: "thousands",
			in:   "1,234,567",
			want: []float64{1234567},
			raw:  []string{"1,234,567"},
		},
		{
			name: "us grouped",
			in:   "1,234.5",
			want: []float64{1234.5},
			raw:  []string{"1,234.5"},
		},
		{
			name: "scientific e",
			in:   "1.5e-3 and 2E+2",
			want: []float64{0.0015, 200},
			sci:  []bool{true, true},
			raw:  []string{"1.5e-3", "2E+2"},
		},
		{
			name: "times ten",
			in:   "1.5\u00d710^\u22123",
			want: []float64{0.0015},
			sci:  []bool{true},
			raw:  []string{"1.5\u00d710^\u22123"},
		},
		{
			name:    "percent",
			in:      "4.2% and 5 %",
			want:    []float64{4.2, 5},
			percent: []bool{true, true},
			raw:     []string{"4.2%", "5 %"},
		},
		{
			name: "en dash range",
			in:   "0.1\u20130.3",
			want: []float64{0.1, 0.3},
			raw:  []string{"0.1", "0.3"},
		},
		{
			name: "hyphen range keeps the sign on the first",
			in:   "-0.4-0.2",
			want: []float64{-0.4, 0.2},
			raw:  []string{"-0.4", "0.2"},
		},
		{
			name: "leading dot",
			in:   ".5",
			want: []float64{0.5},
			raw:  []string{".5"},
		},
		{
			name: "bare comma decimal",
			in:   "0,21",
			want: []float64{0.21},
			raw:  []string{"0,21"},
		},
		{
			name: "en dash sign",
			in:   "about \u20130.21",
			want: []float64{-0.21},
			raw:  []string{"\u20130.21"},
		},
		{
			name: "unicode minus sign",
			in:   "value \u22120.21",
			want: []float64{-0.21},
			raw:  []string{"\u22120.21"},
		},
		{
			name: "unicode minus range stays positive",
			in:   "0.1\u22120.3",
			want: []float64{0.1, 0.3},
			raw:  []string{"0.1", "0.3"},
		},
		{
			name: "spaced en dash range",
			in:   "0.1 \u2013 0.3",
			want: []float64{0.1, 0.3},
			raw:  []string{"0.1", "0.3"},
		},
		{
			name: "product codes and ordinals are not numbers",
			in:   "MERRA-2 PM2.5 CO2 NO2 20th GPM",
		},
		{
			name: "spaced measurement stays a number",
			in:   "PM 2.5",
			want: []float64{2.5},
			raw:  []string{"2.5"},
		},
		{
			name: "identifier does not swallow the next number",
			in:   "MERRA-2 and 3",
			want: []float64{3},
			raw:  []string{"3"},
		},
		{
			name:    "percent words",
			in:      "20 percent, 3.5 per cent, 7 PCT and 4 percentage points",
			want:    []float64{20, 3.5, 7, 4},
			percent: []bool{true, true, true, false},
			raw:     []string{"20 percent", "3.5 per cent", "7 PCT", "4"},
		},
		{
			name: "superscript exponent",
			in:   "1.2 \u00d7 10\u207b\u00b3 and 4\u00d710\u2075",
			want: []float64{0.0012, 400000},
			sci:  []bool{true, true},
			raw:  []string{"1.2 \u00d7 10\u207b\u00b3", "4\u00d710\u2075"},
		},
		{
			name: "units glued after a number are still numbers",
			in:   "5km, 0.5mm/day and 12C",
			want: []float64{5, 0.5, 12},
			raw:  []string{"5", "0.5", "12"},
		},
		{
			name: "ordinal and decade suffixes are not numbers",
			in:   "the 1st, 2nd, 3rd, 11th and the 1990s",
		},
		{
			name: "a comma between two numbers is not a decimal",
			in:   "0.1,0.3",
			want: []float64{0.1, 0.3},
			raw:  []string{"0.1", "0.3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScanNumbers(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d numbers %#v, want %d", len(got), raws(got), len(tt.want))
			}
			for i, n := range got {
				if math.Abs(n.Value-tt.want[i]) > 1e-9 {
					t.Errorf("value[%d] = %v, want %v (raw %q)", i, n.Value, tt.want[i], n.Raw)
				}
				if tt.raw != nil && n.Raw != tt.raw[i] {
					t.Errorf("raw[%d] = %q, want %q", i, n.Raw, tt.raw[i])
				}
				if tt.alts != nil && !math.IsNaN(tt.alts[i]) {
					if !n.HasAlt || math.Abs(n.Alt-tt.alts[i]) > 1e-9 {
						t.Errorf("alt[%d] = %v has=%v, want %v", i, n.Alt, n.HasAlt, tt.alts[i])
					}
				}
				if tt.percent != nil && n.Percent != tt.percent[i] {
					t.Errorf("percent[%d] = %v, want %v", i, n.Percent, tt.percent[i])
				}
				if tt.sci != nil && n.Sci != tt.sci[i] {
					t.Errorf("sci[%d] = %v, want %v", i, n.Sci, tt.sci[i])
				}
			}
		})
	}
}

func raws(ns []Number) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Raw
	}
	return out
}
