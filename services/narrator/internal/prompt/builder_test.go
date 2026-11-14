package prompt

import (
	"math"
	"strings"
	"testing"
	"time"

	analysisv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/analysis/v1"
	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/llm"
)

func TestBuildNilRequest(t *testing.T) {
	_, _, err := NewBuilder().Build(Input{})
	if err == nil {
		t.Fatal("expected an error for a nil request")
	}
}

func TestBuildGoldenOverview(t *testing.T) {
	req, facts, err := NewBuilder().Build(Input{Req: goldenReq(""), Series: goldenSeries()})
	if err != nil {
		t.Fatal(err)
	}
	want := systemRules + `

## Facts

### Variable
variable.id: rain
variable.name: Rain
variable.definition: Monthly rain
variable.units: mm
variable.llm_hints: Sensor switched in 2011.

### Region
bbox.west: 1
bbox.south: 2
bbox.east: 3
bbox.north: 4

### Dates
dates.start_year: 2001
dates.start_month: 1
dates.start_day: 2
dates.end_year: 2002
dates.end_month: 3
dates.end_day: 4
dates.record_years: 1.16906229 years

### Series
series.variable_id: rain
series.units: mm
series.alpha: 0.05
series.confidence_percent: 95
series.field: anomalies
series.n_valid: 24
series.n_total: 24

### Regression
regression.slope_per_decade: -1.5
regression.slope_ci_low: -2.5
regression.slope_ci_high: -0.5
regression.p_value: 0.01
regression.significant: true
regression.r_squared: 0.4
regression.n: 24
regression.n_effective: 20
regression.total_change_over_record: -0.17535934 mm
regression.total_change_per_century: -15 mm per century

### Warnings
warnings.0: Only 2 years of data
`
	if req.System != want {
		t.Fatalf("system mismatch\nGOT:\n%s\nWANT:\n%s", req.System, want)
	}
	if strings.ContainsRune(req.System, '\u2014') {
		t.Fatal("system prompt contains an em dash")
	}
	for _, phrase := range []string{
		"plain language",
		"Never compute a new number",
		"confidence interval",
		"p-value",
		"significant at the given alpha",
		"Mention every warning",
		"Do not claim that the trend causes anything",
		"language of the user's question",
		"overview in English",
		"at most about 200 words",
		"unless the user asks for more detail",
		"change over the whole record",
		"change per century",
	} {
		if !strings.Contains(req.System, phrase) {
			t.Errorf("system prompt missing %q", phrase)
		}
	}
	if !strings.Contains(req.System, "2\u20133 short paragraphs") {
		t.Fatal("brevity range should use an en dash")
	}
	if strings.Contains(req.System, "### Mann-Kendall") || strings.Contains(req.System, "### Pettitt") || strings.Contains(req.System, "### Grid") {
		t.Fatal("unset sections were rendered")
	}
	if req.Model != "gpt-test" || req.Temperature != 0 || req.MaxTokens != 0 {
		t.Fatalf("request meta: model=%q temp=%v max=%d", req.Model, req.Temperature, req.MaxTokens)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != llm.RoleUser || req.Messages[0].Text != overviewQuestion {
		t.Fatalf("overview turn: %#v", req.Messages)
	}
	for _, wantF := range []Fact{
		{Name: "regression.slope_per_decade", Value: -1.5, Units: "mm per decade"},
		{Name: "regression.p_value", Value: 0.01, Units: "1"},
		{Name: "series.confidence_percent", Value: 95, Units: "percent"},
		{Name: "dates.start_year", Value: 2001, Units: "year"},
		{Name: "dates.end_year", Value: 2002, Units: "year"},
		{Name: "bbox.north", Value: 4, Units: "degrees"},
		{Name: "regression.total_change_per_century", Value: -15, Units: "mm per century"},
	} {
		if !hasFact(facts, wantF) {
			t.Errorf("missing fact %#v", wantF)
		}
	}
	years, ok := recordYears(goldenReq("").GetDates())
	if !ok {
		t.Fatal("record years")
	}
	if !hasFact(facts, Fact{Name: "dates.record_years", Value: years, Units: "years"}) {
		t.Fatal("missing record years")
	}
	if !hasFact(facts, Fact{Name: "regression.total_change_over_record", Value: -1.5 * (years / 10), Units: "mm"}) {
		t.Fatal("missing total change over the record")
	}
	if !hasValue(facts, "variable.llm_hints#0", 2011) {
		t.Fatal("hint year was not recorded")
	}
	if !hasValue(facts, "warnings.0#0", 2) {
		t.Fatal("warning number was not recorded")
	}
}

func TestBuildQuestionAndHistory(t *testing.T) {
	req := goldenReq("Is 18 enough?")
	req.History = []*narratorv1.ChatMessage{
		{Role: narratorv1.Role_ROLE_USER, Text: "What changed?"},
		nil,
		{Role: narratorv1.Role_ROLE_UNSPECIFIED, Text: ""},
		{Role: narratorv1.Role_ROLE_ASSISTANT, Text: "The slope is negative."},
	}
	got, facts, err := NewBuilder().Build(Input{Req: req, Series: goldenSeries()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages: %#v", got.Messages)
	}
	if got.Messages[0].Role != llm.RoleUser || got.Messages[0].Text != "What changed?" {
		t.Fatalf("first turn: %#v", got.Messages[0])
	}
	if got.Messages[1].Role != llm.RoleAssistant {
		t.Fatalf("second turn: %#v", got.Messages[1])
	}
	if got.Messages[2].Role != llm.RoleUser || got.Messages[2].Text != "Is 18 enough?" {
		t.Fatalf("question: %#v", got.Messages[2])
	}
	if !hasValue(facts, "question.number", 18) {
		t.Fatal("question number was not recorded")
	}
}

func TestBuildRichSeriesAndGrid(t *testing.T) {
	series := goldenSeries()
	series.MannKendall = &analysisv1.MannKendallResult{
		Variant:            analysisv1.MannKendallVariant_MANN_KENDALL_VARIANT_HAMED_RAO,
		Tau:                0.3,
		Z:                  2.1,
		PValue:             0.02,
		Significant:        true,
		SensSlopePerDecade: -1.2,
		SensCiLow:          -2,
		SensCiHigh:         -0.4,
		N:                  24,
	}
	series.Agreement = &analysisv1.Agreement{SameDirection: true, SameSignificance: true}
	series.Pettitt = &analysisv1.PettittResult{
		ChangeTime:  timestamppb.New(time.Date(2012, 6, 1, 0, 0, 0, 0, time.UTC)),
		PValue:      0.2,
		Significant: false,
		MeanBefore:  10,
		MeanAfter:   8,
	}
	series.ResidualChecks = &analysisv1.ResidualChecksResult{
		Autocorrelation:     analysisv1.CheckStatus_CHECK_STATUS_WARN,
		Lag1Autocorrelation: 0.35,
		Normality:           analysisv1.CheckStatus_CHECK_STATUS_PASS,
		NormalityPValue:     0.4,
	}
	series.Outliers = &analysisv1.OutliersResult{
		RobustZThreshold: 3.5,
		Outliers: []*analysisv1.Outlier{{
			Time:    timestamppb.New(time.Date(2010, 1, 15, 0, 0, 0, 0, time.UTC)),
			Value:   40,
			RobustZ: 4.1,
		}},
	}
	series.Shuffle = &analysisv1.ShuffleResult{
		Permutations:            100,
		BlockLength:             3,
		ObservedSlopePerDecade:  -1.5,
		PValue:                  0.04,
		ShuffledSlopesPerDecade: []float64{-0.2, 0.2},
	}
	series.TrendTriangle = &analysisv1.TrendTriangleResult{
		MinYears:        8,
		StartYears:      []int32{2001, 2002},
		EndYears:        []int32{2010, 2011},
		SlopesPerDecade: []float64{-1, math.NaN(), 0.5},
		PValues:         []float64{0.01, math.NaN(), 0.5},
	}
	series.MonthlyTrends = []*analysisv1.MonthlyTrend{{
		Month: 6, SlopePerDecade: -2, PValue: 0.03, Significant: true,
	}}
	series.YearsToSignificance = &analysisv1.YearsToSignificanceResult{
		YearsNeeded: 30, YearsSoFar: 20, NoiseSd: 1.5, TrendPerYear: -0.15, Phi: 0.2, AlreadySignificant: true,
	}
	series.Regression.Model = &analysisv1.FittedModel{
		TimeOrigin:          timestamppb.New(time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)),
		Intercept:           10,
		Slope:               -0.15,
		SlopeStderr:         0.02,
		ResidualSd:          1.1,
		Lag1Autocorrelation: 0.35,
	}
	grid := &analysisv1.GridAnalysis{
		Rows: 2, Cols: 2, Alpha: 0.05, FdrQ: 0.1, Units: "mm",
		Lats: []float64{4, 2}, Lons: []float64{1, 3},
		Layers: []*analysisv1.GridLayer{{
			Test:            &analysisv1.GridTest{Test: &analysisv1.GridTest_Regression{Regression: &analysisv1.Regression{}}},
			SlopesPerDecade: []float64{1, math.NaN(), -3, 2},
			Significant:     []bool{true, false, false, true},
			PThreshold:      0.04,
		}},
	}
	req, facts, err := NewBuilder().Build(Input{Req: goldenReq("why?"), Series: series, Grid: grid})
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{
		"### Mann-Kendall", "hamed_rao", "### Agreement", "### Pettitt",
		"pettitt.change_time.time: 2012-06-01", "### Residual checks", "warn",
		"### Outliers", "outliers.0.value: 40", "### Shuffle",
		"### Trend triangle", "### Monthly trends", "monthly_trends.6.slope_per_decade: -2",
		"### Years to significance", "years_to_significance.detection_probability_percent: 90",
		"### Grid", "grid.layer.0.test: regression", "grid.lat_north: 4",
		"regression.model.slope_per_year: -0.15",
		"pettitt.change_time.month: 6",
		"outliers.0.time: 2010-01-15", "outliers.0.year: 2010",
		"regression.total_change_per_century: -15 mm per century",
		"mann_kendall.total_change_per_century: -12 mm per century",
		"grid.layer.0.significant_increase_cells: 2",
		"grid.layer.0.significant_decrease_cells: 0",
		"grid.layer.0.strongest_increase.lat: 2",
		"grid.layer.0.strongest_increase.lon: 3",
		"grid.layer.0.strongest_increase.slope_per_decade: 2",
	} {
		if !strings.Contains(req.System, section) {
			t.Errorf("missing %q", section)
		}
	}
	if strings.Contains(req.System, "NaN") {
		t.Fatal("NaN leaked into the prompt")
	}
	if strings.Contains(req.System, "outliers.0.month") || strings.Contains(req.System, "outliers.0.day") {
		t.Fatal("outlier day or month was published")
	}
	if strings.Contains(req.System, "strongest_decrease") {
		t.Fatal("a non-significant decrease was published as strongest")
	}
	for _, f := range facts {
		if math.IsNaN(f.Value) || math.IsInf(f.Value, 0) {
			t.Fatalf("non-finite fact %#v", f)
		}
	}
	if !hasValue(facts, "mann_kendall.sens_slope_per_decade", -1.2) {
		t.Fatal("missing Sen slope")
	}
	if !hasValue(facts, "grid.layer.0.significant_cells", 2) || !hasValue(facts, "grid.layer.0.slope_min_per_decade", -3) {
		t.Fatal("grid summary facts missing")
	}
	if !hasValue(facts, "trend_triangle.finite_windows", 2) {
		t.Fatal("triangle dropped the finite-window count")
	}
	years, ok := recordYears(goldenReq("").GetDates())
	if !ok {
		t.Fatal("record years")
	}
	if !hasFact(facts, Fact{Name: "regression.total_change_over_record", Value: -1.5 * (years / 10), Units: "mm"}) {
		t.Fatal("missing regression total change")
	}
	if !hasFact(facts, Fact{Name: "mann_kendall.total_change_over_record", Value: -1.2 * (years / 10), Units: "mm"}) {
		t.Fatal("missing Sen total change")
	}
	if !hasFact(facts, Fact{Name: "grid.layer.0.strongest_increase.slope_per_decade", Value: 2, Units: "mm per decade"}) {
		t.Fatal("missing strongest increase slope")
	}
}

func goldenReq(q string) *narratorv1.NarrateRequest {
	return &narratorv1.NarrateRequest{
		Question: q,
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
	}
}

func goldenSeries() *analysisv1.SeriesAnalysis {
	return &analysisv1.SeriesAnalysis{
		VariableId: "rain",
		Units:      "mm",
		Alpha:      0.05,
		Field:      analysisv1.SeriesField_SERIES_FIELD_ANOMALIES,
		NValid:     24,
		NTotal:     24,
		Warnings:   []string{"Only 2 years of data"},
		Regression: &analysisv1.RegressionResult{
			SlopePerDecade: -1.5,
			SlopeCiLow:     -2.5,
			SlopeCiHigh:    -0.5,
			PValue:         0.01,
			Significant:    true,
			RSquared:       0.4,
			N:              24,
			NEffective:     20,
		},
	}
}

func hasFact(facts []Fact, want Fact) bool {
	for _, f := range facts {
		if f.Name == want.Name && f.Units == want.Units && math.Abs(f.Value-want.Value) < 1e-9 {
			return true
		}
	}
	return false
}

func hasValue(facts []Fact, name string, v float64) bool {
	for _, f := range facts {
		if f.Name == name && math.Abs(f.Value-v) < 1e-9 {
			return true
		}
	}
	return false
}

func TestBuildHistoryStartsWithAssistant(t *testing.T) {
	req := goldenReq("Is that a lot?")
	req.History = []*narratorv1.ChatMessage{
		nil,
		{Role: narratorv1.Role_ROLE_ASSISTANT, Text: "The slope is a decrease of 1.5 mm per decade."},
	}
	got, _, err := NewBuilder().Build(Input{Req: req, Series: goldenSeries()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages: %#v", got.Messages)
	}
	if got.Messages[0].Role != llm.RoleUser || got.Messages[0].Text != overviewQuestion {
		t.Fatalf("inserted overview turn: %#v", got.Messages[0])
	}
	if got.Messages[1].Role != llm.RoleAssistant || got.Messages[1].Text != "The slope is a decrease of 1.5 mm per decade." {
		t.Fatalf("overview answer: %#v", got.Messages[1])
	}
	if got.Messages[2].Role != llm.RoleUser || got.Messages[2].Text != "Is that a lot?" {
		t.Fatalf("question: %#v", got.Messages[2])
	}
}

func TestRecordYears(t *testing.T) {
	y, ok := recordYears(&commonv1.DateRange{
		Start: &commonv1.Date{Year: 2001, Month: 1, Day: 1},
		End:   &commonv1.Date{Year: 2020, Month: 12, Day: 31},
	})
	if !ok || y != 20 {
		t.Fatalf("got %v ok=%v, want 20", y, ok)
	}
	y, ok = recordYears(&commonv1.DateRange{
		Start: &commonv1.Date{Year: 2020, Month: 12, Day: 31},
		End:   &commonv1.Date{Year: 2001, Month: 1, Day: 1},
	})
	if !ok || y != 20 {
		t.Fatalf("swapped got %v ok=%v, want 20", y, ok)
	}
	if _, ok = recordYears(&commonv1.DateRange{
		Start: &commonv1.Date{Year: 2001, Month: 2, Day: 31},
		End:   &commonv1.Date{Year: 2002, Month: 1, Day: 1},
	}); ok {
		t.Fatal("31 February should not be a record length")
	}
}

func TestTotalChangeFacts(t *testing.T) {
	req := goldenReq("")
	req.Dates = &commonv1.DateRange{
		Start: &commonv1.Date{Year: 2001, Month: 1, Day: 1},
		End:   &commonv1.Date{Year: 2020, Month: 12, Day: 31},
	}
	series := goldenSeries()
	series.MannKendall = &analysisv1.MannKendallResult{SensSlopePerDecade: -1.2}
	_, facts, err := NewBuilder().Build(Input{Req: req, Series: series})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []Fact{
		{Name: "dates.record_years", Value: 20, Units: "years"},
		{Name: "regression.total_change_over_record", Value: -3, Units: "mm"},
		{Name: "regression.total_change_per_century", Value: -15, Units: "mm per century"},
		{Name: "mann_kendall.total_change_over_record", Value: -2.4, Units: "mm"},
		{Name: "mann_kendall.total_change_per_century", Value: -12, Units: "mm per century"},
	} {
		if !hasFact(facts, want) {
			t.Errorf("missing %#v", want)
		}
	}

	req.Dates = nil
	_, facts, err = NewBuilder().Build(Input{Req: req, Series: series})
	if err != nil {
		t.Fatal(err)
	}
	if hasValue(facts, "dates.record_years", 20) || hasValue(facts, "regression.total_change_over_record", -3) {
		t.Fatal("record change was published without dates")
	}
	if !hasFact(facts, Fact{Name: "regression.total_change_per_century", Value: -15, Units: "mm per century"}) {
		t.Fatal("per-century change does not need the record length")
	}
	if !hasFact(facts, Fact{Name: "mann_kendall.total_change_per_century", Value: -12, Units: "mm per century"}) {
		t.Fatal("Sen per-century change missing")
	}
}

func TestOutliersCapSkipsDayMonth(t *testing.T) {
	items := make([]*analysisv1.Outlier, 12)
	for i := range items {
		items[i] = &analysisv1.Outlier{
			Time:    timestamppb.New(time.Date(2010, time.Month(1+i%12), 10+i%15, 0, 0, 0, 0, time.UTC)),
			Value:   float64(100 + i),
			RobustZ: 4,
		}
	}
	series := goldenSeries()
	series.Outliers = &analysisv1.OutliersResult{
		RobustZThreshold: 3.5,
		Outliers:         items,
		Highest:          items[11],
		Lowest:           items[0],
	}
	req, facts, err := NewBuilder().Build(Input{Req: goldenReq(""), Series: series})
	if err != nil {
		t.Fatal(err)
	}
	if !hasValue(facts, "outliers.count", 12) || !hasValue(facts, "outliers.listed", 10) {
		t.Fatal("count or listed cap missing")
	}
	if !strings.Contains(req.System, "outliers.9.value: 109") {
		t.Fatal("the 10th outlier was not listed")
	}
	if strings.Contains(req.System, "outliers.10.") || strings.Contains(req.System, "outliers.11.") {
		t.Fatal("outliers past the cap were listed")
	}
	for _, f := range facts {
		if strings.HasPrefix(f.Name, "outliers.") && (strings.HasSuffix(f.Name, ".month") || strings.HasSuffix(f.Name, ".day")) {
			t.Fatalf("published %s", f.Name)
		}
	}
	if !hasValue(facts, "outliers.0.year", 2010) || !hasValue(facts, "outliers.highest.year", 2010) {
		t.Fatal("outlier year was dropped")
	}
	if !hasValue(facts, "outliers.highest.value", 111) || !hasValue(facts, "outliers.0.value", 100) {
		t.Fatal("highest or first outlier value missing")
	}
	if !strings.Contains(req.System, "outliers.0.time: 2010-01-10") {
		t.Fatal("outlier date string missing")
	}
}

func TestGridWhere(t *testing.T) {
	grid := &analysisv1.GridAnalysis{
		Rows: 2, Cols: 2, Units: "mm",
		Lats: []float64{10, 8},
		Lons: []float64{20, 22},
		Layers: []*analysisv1.GridLayer{{
			Test:            &analysisv1.GridTest{Test: &analysisv1.GridTest_Regression{Regression: &analysisv1.Regression{}}},
			SlopesPerDecade: []float64{1, 1, -4, 9},
			Significant:     []bool{true, true, true, false},
		}},
	}
	_, facts, err := NewBuilder().Build(Input{Req: goldenReq(""), Series: goldenSeries(), Grid: grid})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []Fact{
		{Name: "grid.layer.0.significant_increase_cells", Value: 2, Units: "count"},
		{Name: "grid.layer.0.significant_decrease_cells", Value: 1, Units: "count"},
		{Name: "grid.layer.0.strongest_increase.lat", Value: 10, Units: "degrees"},
		{Name: "grid.layer.0.strongest_increase.lon", Value: 20, Units: "degrees"},
		{Name: "grid.layer.0.strongest_increase.slope_per_decade", Value: 1, Units: "mm per decade"},
		{Name: "grid.layer.0.strongest_decrease.lat", Value: 8, Units: "degrees"},
		{Name: "grid.layer.0.strongest_decrease.lon", Value: 20, Units: "degrees"},
		{Name: "grid.layer.0.strongest_decrease.slope_per_decade", Value: -4, Units: "mm per decade"},
	} {
		if !hasFact(facts, want) {
			t.Errorf("missing %#v", want)
		}
	}
	if hasValue(facts, "grid.layer.0.strongest_increase.slope_per_decade", 9) {
		t.Fatal("the non-significant cell won the strongest increase")
	}

	flat := &analysisv1.GridAnalysis{
		Rows: 1, Cols: 2, Units: "mm",
		Lats: []float64{10}, Lons: []float64{20, 22},
		Layers: []*analysisv1.GridLayer{{
			SlopesPerDecade: []float64{0, math.NaN()},
			Significant:     []bool{true, true},
		}},
	}
	_, facts, err = NewBuilder().Build(Input{Req: goldenReq(""), Series: goldenSeries(), Grid: flat})
	if err != nil {
		t.Fatal(err)
	}
	if !hasValue(facts, "grid.layer.0.significant_increase_cells", 0) || !hasValue(facts, "grid.layer.0.significant_decrease_cells", 0) {
		t.Fatal("zero and NaN slopes should not count as a direction")
	}
	for _, f := range facts {
		if strings.Contains(f.Name, "strongest_") {
			t.Fatalf("unexpected %s", f.Name)
		}
	}
}
