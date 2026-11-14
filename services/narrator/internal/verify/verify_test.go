package verify

import (
	"strings"
	"testing"

	analysisv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/analysis/v1"
	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"

	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/prompt"
)

func TestUnverified(t *testing.T) {
	slope := prompt.Fact{Name: "regression.slope_per_decade", Value: -1.5, Units: "mm per decade"}
	p := prompt.Fact{Name: "regression.p_value", Value: 0.0423, Units: "1"}
	ciLo := prompt.Fact{Name: "regression.slope_ci_low", Value: -2.5, Units: "mm per decade"}
	perYear := prompt.Fact{Name: "regression.model.slope_per_year", Value: -0.15, Units: "mm per year"}
	dates := []prompt.Fact{
		{Name: "dates.start_year", Value: 2001, Units: "year"},
		{Name: "dates.start_month", Value: 1, Units: "month"},
		{Name: "dates.start_day", Value: 2, Units: "day"},
		{Name: "dates.end_year", Value: 2002, Units: "year"},
		{Name: "dates.end_month", Value: 6, Units: "month"},
		{Name: "dates.end_day", Value: 15, Units: "day"},
	}
	base := append([]prompt.Fact{slope, p, ciLo, perYear}, dates...)

	tests := []struct {
		name   string
		answer string
		facts  []prompt.Fact
		want   []string
	}{
		{name: "empty", answer: "", facts: base},
		{name: "exact", answer: "slope -1.5", facts: base},
		{name: "rounded", answer: "p = 0.042", facts: base},
		{name: "rounded too far", answer: "p = 0.043", facts: base, want: []string{"0.043"}},
		{name: "comma decimal", answer: "p = 0,042", facts: base},
		{name: "percent of p", answer: "p = 4.2%", facts: base},
		{name: "scientific", answer: "p = 4.23e-2", facts: base},
		{name: "unicode minus", answer: "slope \u22121.5", facts: base},
		{name: "per year from per decade", answer: "about -0.15 per year", facts: []prompt.Fact{slope}},
		{name: "per decade from per year", answer: "about -1.5 per decade", facts: []prompt.Fact{perYear}},
		{name: "decrease of absolute", answer: "a decrease of 1.5", facts: []prompt.Fact{slope}},
		{name: "decrease per year", answer: "a decrease of 0.15 per year", facts: []prompt.Fact{slope}},
		{name: "sign error without decrease", answer: "the slope is 1.5", facts: []prompt.Fact{slope}, want: []string{"1.5"}},
		{name: "en dash range", answer: "from -2.5\u2013-1.5", facts: []prompt.Fact{slope, ciLo}},
		{name: "range one bad", answer: "from 0.1\u20130.3", facts: []prompt.Fact{{Name: "x", Value: 0.1}}, want: []string{"0.3"}},
		{name: "year in range", answer: "during 2002", facts: dates},
		{name: "year outside", answer: "during 1999", facts: dates, want: []string{"1999"}},
		{name: "date in range", answer: "on 2002-06-15", facts: dates},
		{name: "date out of range", answer: "on 2002-11-20", facts: dates, want: []string{"11", "20"}},
		{name: "count whitelist", answer: "there were 3 tests and 0 warnings", facts: nil},
		{name: "eleven is not a count", answer: "there were 11 tests", facts: nil, want: []string{"11"}},
		{name: "percent is not a count", answer: "only 5%", facts: nil, want: []string{"5%"}},
		{name: "question number", answer: "you asked about 18", facts: []prompt.Fact{{Name: "question.number", Value: 18, Units: "question"}}},
		{name: "dedup keeps order", answer: "99 then 100 then 99", facts: nil, want: []string{"99", "100"}},
		{name: "ambiguous comma matches thousands", answer: "n = 1,234", facts: []prompt.Fact{{Name: "series.n_total", Value: 1234, Units: "count"}}},
		{name: "unverified raw kept", answer: "slope \u22129.9e-9", facts: base, want: []string{"\u22129.9e-9"}},
		{name: "p less than a larger threshold", answer: "p < 0.001", facts: []prompt.Fact{{Name: "regression.p_value", Value: 0.0004}}},
		{name: "p less than too small", answer: "p < 0.001", facts: []prompt.Fact{{Name: "regression.p_value", Value: 0.04}}, want: []string{"0.001"}},
		{name: "p less or equal", answer: "p \u2264 0.05", facts: []prompt.Fact{{Name: "regression.p_value", Value: 0.042}}},
		{name: "p less glued", answer: "p<0.01", facts: []prompt.Fact{{Name: "mann_kendall.p_value", Value: 0.003}}},
		{name: "p equal to the threshold", answer: "p < 0.05", facts: []prompt.Fact{{Name: "regression.p_value", Value: 0.05}}},
		{name: "ascii less or equal", answer: "p <= 0.05", facts: []prompt.Fact{{Name: "shuffle.p_value", Value: 0.02}}},
		{name: "p-value word", answer: "p-value < 0.01", facts: []prompt.Fact{{Name: "regression.p_value", Value: 0.004}}},
		{name: "percent threshold", answer: "p < 5%", facts: []prompt.Fact{{Name: "regression.p_value", Value: 0.03}}},
		{name: "percent threshold too small", answer: "p < 1%", facts: []prompt.Fact{{Name: "regression.p_value", Value: 0.03}}, want: []string{"1%"}},
		{name: "comparison without p", answer: "n < 100", facts: []prompt.Fact{{Name: "regression.p_value", Value: 0.01}}, want: []string{"100"}},
		{name: "threshold ignores a slope", answer: "p < 0.001", facts: []prompt.Fact{{Name: "regression.slope_per_decade", Value: 0.0001, Units: "mm per decade"}}, want: []string{"0.001"}},
		{name: "en dash negative", answer: "about \u20130.21", facts: []prompt.Fact{{Name: "regression.slope_per_decade", Value: -0.21, Units: "mm per decade"}}},
		{name: "unicode minus range", answer: "from 0.1\u22120.3", facts: []prompt.Fact{{Name: "x", Value: 0.1}, {Name: "y", Value: 0.3}}},
		{name: "product codes ignored", answer: "MERRA-2, PM2.5, CO2, NO2 and the 20th", facts: nil},
		{name: "gpm has no number", answer: "GPM precipitation", facts: nil},
		{name: "spaced number still checked", answer: "PM 2.5 is not a fact", facts: nil, want: []string{"2.5"}},
		{name: "twelve months", answer: "over 12 months", facts: nil},
		{name: "per ten years", answer: "per 10 years", facts: nil},
		{name: "thirteen months is not the phrase", answer: "over 13 months", facts: nil, want: []string{"13"}},
	}
	v := NewVerifier()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := v.Unverified(tt.answer, tt.facts)
			if !sameStrings(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestFactsBlockIsVerified(t *testing.T) {
	req, facts, err := prompt.NewBuilder().Build(prompt.Input{
		Req: &narratorv1.NarrateRequest{
			Question: "",
			Variable: &narratorv1.VariableContext{
				Id: "rain", Name: "Rain", Definition: "Monthly rain", Units: "mm",
				LlmHints: "Sensor switched in 2011.",
			},
			Bbox: &commonv1.BBox{West: 1, South: 2, East: 3, North: 4},
			Dates: &commonv1.DateRange{
				Start: &commonv1.Date{Year: 2001, Month: 1, Day: 2},
				End:   &commonv1.Date{Year: 2002, Month: 3, Day: 4},
			},
			Llm: &narratorv1.LlmConfig{Model: "gpt-test"},
		},
		Series: &analysisv1.SeriesAnalysis{
			VariableId: "rain", Units: "mm", Alpha: 0.05,
			Field: analysisv1.SeriesField_SERIES_FIELD_ANOMALIES, NValid: 24, NTotal: 24,
			Warnings: []string{"Only 2 years of data"},
			Regression: &analysisv1.RegressionResult{
				SlopePerDecade: -1.5, SlopeCiLow: -2.5, SlopeCiHigh: -0.5,
				PValue: 0.01, Significant: true, RSquared: 0.4, N: 24, NEffective: 20,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The length cap in the rules mentions 200. That is an instruction, not a
	// result, so only the facts block has to be fully verified.
	factsText := req.System
	if i := strings.Index(req.System, "\n## Facts\n"); i >= 0 {
		factsText = req.System[i:]
	}
	bad := NewVerifier().Unverified(factsText, facts)
	if len(bad) > 0 {
		t.Fatalf("facts block unverified: %v\n%s", bad, factsText)
	}
	answer := "From 2001 to 2002 the slope is a decrease of 0.15 mm per year. The p-value is 1% and the 95% interval runs from -2.5 to -0.5. There were 3 checks. A made-up 42.5 does not belong."
	got := NewVerifier().Unverified(answer, facts)
	if !sameStrings(got, []string{"42.5"}) {
		t.Fatalf("got %#v", got)
	}
	if strings.ContainsRune(req.System, '\u2014') {
		t.Fatal("em dash in system prompt")
	}
}

func TestRealisticAnswers(t *testing.T) {
	facts := []prompt.Fact{
		{Name: "regression.slope_per_decade", Value: -0.42, Units: "mm/day per decade"},
		{Name: "regression.p_value", Value: 0.003, Units: "1"},
		{Name: "regression.total_change_over_record", Value: -0.84, Units: "mm/day"},
		{Name: "regression.total_change_per_century", Value: -4.2, Units: "mm/day per century"},
		{Name: "dates.start_year", Value: 2001, Units: "year"},
		{Name: "dates.end_year", Value: 2020, Units: "year"},
	}
	tests := []struct {
		name   string
		answer string
		want   []string
	}{
		{
			name: "english",
			answer: "From 2001 to 2020 the GPM rainfall series falls. The regression slope is a decrease of 0.42 mm/day per decade, about 0.042 per year, and p < 0.01. " +
				"Over the record the change is a decrease of 0.84 mm/day, which is 4.2 per century. MERRA-2, PM2.5, CO2 and NO2 are other products, and the 20th percentile is not used. " +
				"The seasonal cycle covers 12 months, so the rate is quoted per 10 years. A figure of 7.7 does not come from the results.",
			want: []string{"7.7"},
		},
	}
	v := NewVerifier()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := v.Unverified(tt.answer, facts)
			if !sameStrings(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBuiltFactsMatchRealisticAnswers(t *testing.T) {
	req := &narratorv1.NarrateRequest{
		Question: "How large is the change?",
		Variable: &narratorv1.VariableContext{Id: "gpm_precip", Name: "Precipitation", Units: "mm/day"},
		Dates: &commonv1.DateRange{
			Start: &commonv1.Date{Year: 2001, Month: 1, Day: 1},
			End:   &commonv1.Date{Year: 2020, Month: 12, Day: 31},
		},
		Llm: &narratorv1.LlmConfig{Model: "gpt-test"},
	}
	series := &analysisv1.SeriesAnalysis{
		Units: "mm/day", Alpha: 0.05,
		Regression: &analysisv1.RegressionResult{
			SlopePerDecade: -0.42, PValue: 0.003, Significant: true,
		},
		MannKendall: &analysisv1.MannKendallResult{SensSlopePerDecade: -0.39, PValue: 0.005, Significant: true},
	}
	grid := &analysisv1.GridAnalysis{
		Rows: 2, Cols: 2, Units: "mm/day",
		Lats: []float64{10, 8}, Lons: []float64{20, 22},
		Layers: []*analysisv1.GridLayer{{
			SlopesPerDecade: []float64{1, 0.5, -3.5, 0.2},
			Significant:     []bool{true, false, true, false},
		}},
	}
	_, facts, err := prompt.NewBuilder().Build(prompt.Input{Req: req, Series: series, Grid: grid})
	if err != nil {
		t.Fatal(err)
	}
	english := "From 2001 to 2020 rainfall fell. The slope is a decrease of 0.42 mm/day per decade (about 0.042 per year) and p < 0.01. " +
		"The change over the record is a decrease of 0.84, or 4.2 per century. Sen's slope is a decrease of 0.39 per decade. " +
		"One cell increased and the strongest decrease is 3.5 at latitude 8 and longitude 20. MERRA-2 is a different product. 7.7 is not a result."
	v := NewVerifier()
	if got := v.Unverified(english, facts); !sameStrings(got, []string{"7.7"}) {
		t.Fatalf("english: %#v", got)
	}
	where := "The strongest increase is 1 mm per decade at latitude 10 and longitude 20. The strongest decrease is 3.5 at latitude 8 and longitude 20. A slope of 0.5 is not reported."
	if got := v.Unverified(where, facts); !sameStrings(got, []string{"0.5"}) {
		t.Fatalf("grid where: %#v", got)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestRoundingHalfAway(t *testing.T) {
	facts := []prompt.Fact{{Name: "regression.r_squared", Value: 1.25, Units: "1"}}
	v := NewVerifier()
	// 1.25 shown to 1 decimal rounds half away from zero, to 1.3.
	if got := v.Unverified("r2 is 1.3", facts); len(got) != 0 {
		t.Fatalf("1.3 should match 1.25 rounded, got %v", got)
	}
	if got := v.Unverified("r2 is 1.2", facts); len(got) != 1 {
		t.Fatalf("1.2 should not match, got %v", got)
	}
}
