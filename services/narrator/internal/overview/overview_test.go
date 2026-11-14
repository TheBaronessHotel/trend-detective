package overview

import (
	"math"
	"strings"
	"testing"
	"time"

	analysisv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/analysis/v1"
	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	rain = &narratorv1.VariableContext{Id: "gpm_precip", Name: "Precipitation", Units: "mm/day"}
	span = &commonv1.DateRange{
		Start: &commonv1.Date{Year: 2001, Month: 1, Day: 1},
		End:   &commonv1.Date{Year: 2020, Month: 12, Day: 31},
	}
)

// upSeries is a significant rise that both tests agree on.
func upSeries() *analysisv1.SeriesAnalysis {
	return &analysisv1.SeriesAnalysis{
		VariableId: "gpm_precip", Units: "mm/day", Alpha: 0.05,
		Field:  analysisv1.SeriesField_SERIES_FIELD_ANOMALIES,
		NValid: 240, NTotal: 240,
		Regression: &analysisv1.RegressionResult{
			SlopePerDecade: 0.4213, SlopeCiLow: 0.1049, SlopeCiHigh: 0.7377,
			PValue: 0.0031, Significant: true, N: 240,
		},
		MannKendall: &analysisv1.MannKendallResult{
			PValue: 0.0123, Significant: true, SensSlopePerDecade: 0.39,
		},
		Agreement: &analysisv1.Agreement{SameDirection: true, SameSignificance: true},
	}
}

func TestBuild(t *testing.T) {
	tests := []struct {
		name     string
		series   func() *analysisv1.SeriesAnalysis
		grid     *analysisv1.GridAnalysis
		variable *narratorv1.VariableContext
		dates    *commonv1.DateRange
		want     string
		// contains and absent are checked when want is empty.
		contains []string
		absent   []string
	}{
		{
			name:     "significant up",
			series:   upSeries,
			variable: rain,
			dates:    span,
			want: "From January 2001 to December 2020 (240 values), the average precipitation in the selected region, " +
				"with the seasonal cycle removed, rose by about 0.42 mm/day per decade, and the 95% confidence interval " +
				"for that rate runs from 0.10 to 0.74. " +
				"This trend is statistically significant (p = 0.003, below the significance level of 0.05), " +
				"and the Mann-Kendall test, which is less sensitive to extreme months, agrees (p = 0.012). " +
				"Remember that a trend is not proof of a cause: this shows how precipitation changed, not why.",
		},
		{
			name: "significant down with change point and warnings",
			series: func() *analysisv1.SeriesAnalysis {
				return &analysisv1.SeriesAnalysis{
					VariableId: "grace_lwe", Units: "cm", Alpha: 0.05,
					NValid: 228, NTotal: 240,
					Regression: &analysisv1.RegressionResult{
						SlopePerDecade: -3.456, SlopeCiLow: -5.01, SlopeCiHigh: -1.9,
						PValue: 0.00002, Significant: true,
					},
					Pettitt: &analysisv1.PettittResult{
						ChangeTime:  timestamppb.New(time.Date(2011, time.March, 1, 0, 0, 0, 0, time.UTC)),
						PValue:      0.004,
						Significant: true,
						MeanBefore:  2.345, MeanAfter: -4.01,
					},
					Warnings: []string{"Only 19 years of data.", "  ", "Residuals are autocorrelated", "Sensor changed in 2017"},
				}
			},
			variable: &narratorv1.VariableContext{Id: "grace_lwe", Name: "GRACE water storage"},
			dates:    span,
			want: "From January 2001 to December 2020 (228 of 240 time steps with data), the average GRACE water storage " +
				"in the selected region fell by about 3.5 cm per decade, and the 95% confidence interval for that rate " +
				"runs from -5.0 to -1.9. " +
				"This trend is statistically significant (p < 0.001, below the significance level of 0.05). " +
				"Caveats: Only 19 years of data; Residuals are autocorrelated (plus 1 more in the app). " +
				"The Pettitt test found a likely sudden shift around March 2011 (p = 0.004), when the average moved " +
				"from 2.3 to -4.0 cm, and a step like that, for example from a new sensor, can look like a trend. " +
				"Remember that a trend is not proof of a cause: this shows how GRACE water storage changed, not why.",
		},
		{
			name: "not significant, Mann-Kendall agrees",
			series: func() *analysisv1.SeriesAnalysis {
				return &analysisv1.SeriesAnalysis{
					Units: "ppb", Alpha: 0.1, NValid: 96, NTotal: 96,
					Regression: &analysisv1.RegressionResult{
						SlopePerDecade: 0.052, SlopeCiLow: -0.081, SlopeCiHigh: 0.19, PValue: 0.41,
					},
					MannKendall: &analysisv1.MannKendallResult{PValue: 0.55, SensSlopePerDecade: 0.04},
					Pettitt:     &analysisv1.PettittResult{PValue: 0.6, Significant: false},
				}
			},
			variable: &narratorv1.VariableContext{Id: "no2", Name: "NO2 column"},
			dates:    &commonv1.DateRange{Start: &commonv1.Date{Year: 2015, Month: 1}, End: &commonv1.Date{Year: 2022, Month: 12}},
			want: "From January 2015 to December 2022 (96 values), the average NO2 column in the selected region " +
				"rose by about 0.052 ppb per decade, and the 90% confidence interval for that rate runs from -0.081 to 0.190. " +
				"This trend is not statistically significant (p = 0.41, above the significance level of 0.1), " +
				"so natural ups and downs could explain it, and the Mann-Kendall test, which is less sensitive to " +
				"extreme months, agrees (p = 0.55). " +
				"Remember that a trend is not proof of a cause: this shows how NO2 column changed, not why.",
		},
		{
			name: "missing regression falls back to Sen's slope",
			series: func() *analysisv1.SeriesAnalysis {
				return &analysisv1.SeriesAnalysis{
					VariableId: "t2m", Units: "K", Alpha: 0.05, NValid: 360,
					MannKendall: &analysisv1.MannKendallResult{
						PValue: 0.02, Significant: true, SensSlopePerDecade: 0.21, SensCiLow: 0.05, SensCiHigh: 0.36,
					},
				}
			},
			want: "Over 360 values, the average t2m in the selected region rose by about 0.21 K per decade (Sen's slope), " +
				"and the 95% confidence interval for that rate runs from 0.05 to 0.36. " +
				"This trend is statistically significant (p = 0.02, below the significance level of 0.05). " +
				"Remember that a trend is not proof of a cause: this shows how t2m changed, not why.",
		},
		{
			name: "missing regression and Mann-Kendall",
			series: func() *analysisv1.SeriesAnalysis {
				return &analysisv1.SeriesAnalysis{
					VariableId: "gpm_precip", NValid: 30, NTotal: 36,
					Warnings: []string{"Only 3 years of data"},
				}
			},
			variable: rain,
			dates:    &commonv1.DateRange{Start: &commonv1.Date{Year: 2020}, End: &commonv1.Date{Year: 2022}},
			want: "The results for precipitation have no trend estimate, because neither the linear regression nor " +
				"the Mann-Kendall test produced a result, so the rate of change is not available. " +
				"The record runs from 2020 to 2022 with 30 of 36 time steps with data. " +
				"Caveat: Only 3 years of data. " +
				"Remember that a trend is not proof of a cause: this shows how precipitation changed, not why.",
		},
		{
			name: "regression and Mann-Kendall disagree on significance",
			series: func() *analysisv1.SeriesAnalysis {
				s := upSeries()
				s.MannKendall.Significant = false
				s.MannKendall.PValue = 0.2
				s.Agreement = &analysisv1.Agreement{SameDirection: true, SameSignificance: false}
				return s
			},
			variable: rain,
			contains: []string{"does not find a significant trend (p = 0.2)", "a few unusual months may be driving the result"},
			absent:   []string{" agrees"},
		},
		{
			name: "Mann-Kendall significant when regression is not",
			series: func() *analysisv1.SeriesAnalysis {
				s := upSeries()
				s.Regression.Significant = false
				s.Regression.PValue = 0.08
				s.Agreement = nil
				return s
			},
			variable: rain,
			contains: []string{"not statistically significant (p = 0.08, above", "does find a significant trend", "the evidence is mixed"},
		},
		{
			name: "opposite directions",
			series: func() *analysisv1.SeriesAnalysis {
				s := upSeries()
				s.MannKendall.SensSlopePerDecade = -0.1
				s.Agreement = &analysisv1.Agreement{SameDirection: false, SameSignificance: true}
				return s
			},
			variable: rain,
			contains: []string{"points the other way", "even the direction is uncertain"},
		},
		{
			name:   "grid cells counted, NaN cells skipped",
			series: upSeries,
			grid: &analysisv1.GridAnalysis{Layers: []*analysisv1.GridLayer{{
				SlopesPerDecade: []float64{0.1, math.NaN(), 0.3, 0.2},
				Significant:     []bool{true, true, false, true},
			}}},
			variable: rain,
			contains: []string{"On the heatmap, 2 of 3 grid cells show a significant trend"},
		},
		{
			name:     "nil everything",
			series:   func() *analysisv1.SeriesAnalysis { return nil },
			contains: []string{"The results for the variable have no trend estimate", "The record length is not available either.", "not proof of a cause"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Build(tc.series(), tc.grid, tc.variable, tc.dates)
			if tc.want != "" && got != tc.want {
				t.Fatalf("Build mismatch\nGOT:  %s\nWANT: %s", got, tc.want)
			}
			for _, s := range tc.contains {
				if !strings.Contains(got, s) {
					t.Errorf("missing %q in:\n%s", s, got)
				}
			}
			for _, s := range tc.absent {
				if strings.Contains(got, s) {
					t.Errorf("unexpected %q in:\n%s", s, got)
				}
			}
			checkShape(t, BuildSentences(tc.series(), tc.grid, tc.variable, tc.dates))
		})
	}
}

// checkShape holds for every overview: 3 to 5 sentences, the causation
// reminder last, and no em dash.
func checkShape(t *testing.T, sentences []string) {
	t.Helper()
	text := strings.Join(sentences, "")
	n := len(sentences)
	if n < 3 || n > maxSentences {
		t.Errorf("%d sentences, want 3 to %d:\n%s", n, maxSentences, text)
	}
	if !strings.HasSuffix(text, "changed, not why.") || !strings.Contains(text, "a trend is not proof of a cause") {
		t.Errorf("the causation reminder is not last:\n%s", text)
	}
	if strings.ContainsRune(text, '\u2014') {
		t.Errorf("em dash in:\n%s", text)
	}
	for _, bad := range []string{"NaN", "Inf", "  ", "-0.0 ", "-0 "} {
		if strings.Contains(text, bad) {
			t.Errorf("%q in:\n%s", bad, text)
		}
	}
}

func TestBuildCapsAtFiveSentences(t *testing.T) {
	// Warnings, a change point and a grid all compete for two free slots.
	s := upSeries()
	s.Warnings = []string{"Only 20 years of data"}
	s.Pettitt = &analysisv1.PettittResult{PValue: 0.01, Significant: true, MeanBefore: 1, MeanAfter: 2}
	g := &analysisv1.GridAnalysis{Layers: []*analysisv1.GridLayer{{SlopesPerDecade: []float64{1}, Significant: []bool{true}}}}
	got := Build(s, g, rain, span)
	if n := len(BuildSentences(s, g, rain, span)); n != maxSentences {
		t.Fatalf("%d sentences, want %d:\n%s", n, maxSentences, got)
	}
	if !strings.Contains(got, "Caveat: Only 20 years of data.") || !strings.Contains(got, "Pettitt") || strings.Contains(got, "heatmap") {
		t.Fatalf("want the warning and the change point, not the grid:\n%s", got)
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	a := Build(upSeries(), nil, rain, span)
	b := Build(upSeries(), nil, rain, span)
	if a != b {
		t.Fatalf("two builds differ:\n%s\n%s", a, b)
	}
}

func TestBuildSentences(t *testing.T) {
	s := upSeries()
	// A warning with a period of its own stays one sentence.
	s.Warnings = []string{"Gap in 2015. Sensor change in 2016"}
	parts := BuildSentences(s, nil, rain, span)
	if got, want := strings.Join(parts, ""), Build(s, nil, rain, span); got != want {
		t.Fatalf("parts don't join to Build:\n%q\n%q", got, want)
	}
	for i, p := range parts {
		last := i == len(parts)-1
		if strings.HasSuffix(p, " ") == last {
			t.Errorf("part %d %q: want a trailing space on all but the last", i, p)
		}
	}
	found := false
	for _, p := range parts {
		if strings.Contains(p, "Gap in 2015. Sensor change in 2016") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the warning was split: %q", parts)
	}
}

// The probe cases from the review: p at the threshold and intervals near zero.
func TestBuildRoundingNearThresholds(t *testing.T) {
	v := &narratorv1.VariableContext{Name: "Precipitation", Units: "mm/day"}
	tests := []struct {
		name string
		reg  *analysisv1.RegressionResult
		want []string
		bad  []string
	}{
		{
			name: "p just below alpha",
			reg:  &analysisv1.RegressionResult{SlopePerDecade: 0.12, SlopeCiLow: 0.001, SlopeCiHigh: 0.24, PValue: 0.0496, Significant: true},
			want: []string{"(p = 0.0496, below the significance level of 0.05)", "runs from 0.001 to 0.240"},
			bad:  []string{"p = 0.05,"},
		},
		{
			name: "p just above alpha, interval crosses zero",
			reg:  &analysisv1.RegressionResult{SlopePerDecade: 0.12, SlopeCiLow: -0.001, SlopeCiHigh: 0.24, PValue: 0.0504},
			want: []string{"(p = 0.0504, above the significance level of 0.05)", "runs from -0.001 to 0.240"},
			bad:  []string{"from 0.00 "},
		},
		{
			name: "small lower bound",
			reg:  &analysisv1.RegressionResult{SlopePerDecade: 1.2, SlopeCiLow: -0.04, SlopeCiHigh: 2.4, PValue: 0.06},
			want: []string{"rose by about 1.20 mm/day per decade", "runs from -0.04 to 2.40"},
			bad:  []string{"from 0.0 "},
		},
		{
			name: "no change",
			reg:  &analysisv1.RegressionResult{SlopePerDecade: 0, SlopeCiLow: -0.3, SlopeCiHigh: 0.3, PValue: 1},
			want: []string{"shows no change (0.00 mm/day per decade), and the 95% confidence interval"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &analysisv1.SeriesAnalysis{Units: "mm/day", Alpha: 0.05, NValid: 240, Regression: tc.reg}
			got := Build(s, nil, v, nil)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, b := range tc.bad {
				if strings.Contains(got, b) {
					t.Errorf("unwanted %q in:\n%s", b, got)
				}
			}
		})
	}
}

func TestNumberFormatting(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"two significant digits", num(0.4213, decimalsFor(0.4213)), "0.42"},
		{"large value", num(123.4, decimalsFor(123.4)), "123"},
		{"small value", num(0.000123, decimalsFor(0.000123)), "0.00012"},
		{"interval shares decimals", num(0.7377, decimalsFor(0.4213, 0.7377)), "0.74"},
		{"zero skipped for decimals", num(0.5, decimalsFor(0, 0.5)), "0.50"},
		{"negative zero", num(-0.0001, 2), "0.00"},
		{"tiny p", pText(0.00002, 0.05), "p < 0.001"},
		{"p capped at three decimals", pText(0.0031, 0.05), "p = 0.003"},
		{"p drops trailing zeros", pText(0.020, 0.05), "p = 0.02"},
		{"ordinary p", pText(0.41, 0.05), "p = 0.41"},
		{"p just below alpha keeps its digits", pText(0.0496, 0.05), "p = 0.0496"},
		{"p just above alpha keeps its digits", pText(0.0504, 0.05), "p = 0.0504"},
		{"p very close to alpha", pText(0.04999, 0.05), "p = 0.04999"},
		{"p equal to alpha", pText(0.05, 0.05), "p = 0.05"},
		{"p rounding across alpha 0.01", pText(0.0104, 0.01), "p = 0.0104"},
		{"no alpha", pText(0.0496, 0), "p = 0.05"},
		{"bound keeps a digit", num(-0.04, keepBounds(1, -0.04, 2.4)), "-0.04"},
		{"bounds already fine", num(0.11, keepBounds(2, 0.11, 0.43)), "0.11"},
		{"tiny bound", num(-0.001, keepBounds(2, -0.001, 0.24)), "-0.001"},
		{"lowercase a common word", lowerFirst("Precipitation"), "precipitation"},
		{"keep an acronym", lowerFirst("NO2 column"), "NO2 column"},
		{"keep a chemical formula", lowerFirst("O3"), "O3"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
