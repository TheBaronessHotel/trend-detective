package prompt

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	analysisv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/analysis/v1"
	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/llm"
)

// systemRules is the instruction block. It must not contain an em dash.
// The digits 2, 3 and 10 are counts the verifier allows. 200 is only the
// answer-length cap, not a sample result.
const systemRules = `You explain Earth-system trends to students and the public in plain language.

Rules:
- Use only the numbers in the facts block. Never compute a new number and never invent one.
- You may restate a fact with fewer decimal places. A fraction or p-value may be stated as a percent. A per-decade slope may be stated per year by dividing by 10, and a per-year slope may be stated per decade by multiplying by 10. A negative slope may be stated as a decrease of the positive amount. Do not produce any other derived number.
- The change over the whole record and the change per century are already in the facts. Quote those instead of multiplying the slope by a number of years.
- State the uncertainty: the confidence interval, the p-value, and whether the result is significant at the given alpha.
- Mention every warning in the facts. If the facts list no warnings, do not invent one.
- Do not claim that the trend causes anything, or that anything causes the trend.
- Answer in the language of the user's question. When there is no question, give an overview in English, unless the variable context or llm_hints name another language.
- Answer in at most about 200 words, in 2–3 short paragraphs, unless the user asks for more detail.
- If a test was not run, say that it is not available. Do not guess its result.`

const overviewQuestion = "Give an overview of this trend for students and the public."

// builder implements Builder.
type builder struct{}

// NewBuilder returns a Builder that renders analysis facts for the model.
func NewBuilder() Builder { return builder{} }

// Build assembles the system prompt, chat history, and the user turn.
// An empty question asks for an overview. Every number written into the facts
// block is returned as a Fact. Dates are also published as dates.start_year
// and dates.end_year so the verifier can allow years inside the request.
// Numbers in the user's question are not facts: an answer that repeats them
// is checked like any other number.
//
// When the browser sends the overview back as the first turn, that turn is an
// assistant message with no user message before it. Build inserts the overview
// question so a provider that requires a user-first history does not reject it.
func (builder) Build(in Input) (llm.Request, []Fact, error) {
	if in.Req == nil {
		return llm.Request{}, nil, errors.New("prompt: nil request")
	}
	var d doc
	d.b.WriteString(systemRules)
	d.b.WriteString("\n\n## Facts\n")
	d.variable(in.Req.GetVariable())
	d.region(in.Req.GetBbox())
	years, hasYears := d.dates(in.Req.GetDates())
	u := unitsOf(in.Series, in.Req.GetVariable())
	d.series(in.Series, u, years, hasYears)
	d.grid(in.Grid, u)

	msgs := ensureUserFirst(mapHistory(in.Req.GetHistory()))
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Text: userText(in.Req.GetQuestion())})
	return llm.Request{
		System:      d.b.String(),
		Messages:    msgs,
		Model:       in.Req.GetLlm().GetModel(),
		MaxTokens:   0,
		Temperature: 0,
	}, d.facts, nil
}

type doc struct {
	b     strings.Builder
	facts []Fact
}

func (d *doc) h(title string) {
	d.b.WriteString("\n### ")
	d.b.WriteString(title)
	d.b.WriteByte('\n')
}

func (d *doc) lineStr(name, v string) {
	fmt.Fprintf(&d.b, "%s: %s\n", name, v)
}

func (d *doc) lineBool(name string, v bool) {
	if v {
		d.lineStr(name, "true")
		return
	}
	d.lineStr(name, "false")
}

// lineNum records a fact and prints it. NaN and Inf are omitted.
func (d *doc) lineNum(name, units string, v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		d.lineStr(name, "not available")
		return
	}
	d.facts = append(d.facts, Fact{Name: name, Value: v, Units: units})
	fmt.Fprintf(&d.b, "%s: %s\n", name, formatFloat(v))
}

// lineMeasured records a fact and prints the units after the value, so a
// derived change reads as a quantity.
func (d *doc) lineMeasured(name, units string, v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		d.lineStr(name, "not available")
		return
	}
	d.facts = append(d.facts, Fact{Name: name, Value: v, Units: units})
	if units == "" {
		fmt.Fprintf(&d.b, "%s: %s\n", name, formatFloat(v))
		return
	}
	fmt.Fprintf(&d.b, "%s: %s %s\n", name, formatFloat(v), units)
}

// prose prints text and records every number inside it.
func (d *doc) prose(name, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	d.lineStr(name, text)
	for i, n := range ScanNumbers(text) {
		d.facts = append(d.facts, Fact{Name: fmt.Sprintf("%s#%d", name, i), Value: n.Value, Units: "text"})
		if n.HasAlt {
			d.facts = append(d.facts, Fact{Name: fmt.Sprintf("%s#%d.alt", name, i), Value: n.Alt, Units: "text"})
		}
	}
}

func (d *doc) variable(v *narratorv1.VariableContext) {
	if v == nil {
		return
	}
	if v.GetId() == "" && v.GetName() == "" && v.GetDefinition() == "" && v.GetUnits() == "" && v.GetLlmHints() == "" {
		return
	}
	d.h("Variable")
	d.prose("variable.id", v.GetId())
	d.prose("variable.name", v.GetName())
	d.prose("variable.definition", v.GetDefinition())
	d.prose("variable.units", v.GetUnits())
	d.prose("variable.llm_hints", v.GetLlmHints())
}

func (d *doc) region(bb *commonv1.BBox) {
	if bb == nil {
		return
	}
	d.h("Region")
	d.lineNum("bbox.west", "degrees", bb.GetWest())
	d.lineNum("bbox.south", "degrees", bb.GetSouth())
	d.lineNum("bbox.east", "degrees", bb.GetEast())
	d.lineNum("bbox.north", "degrees", bb.GetNorth())
}

func (d *doc) dates(r *commonv1.DateRange) (years float64, ok bool) {
	if r == nil {
		return 0, false
	}
	if r.GetStart() == nil && r.GetEnd() == nil {
		return 0, false
	}
	d.h("Dates")
	if s := r.GetStart(); s != nil {
		d.lineNum("dates.start_year", "year", float64(s.GetYear()))
		d.lineNum("dates.start_month", "month", float64(s.GetMonth()))
		d.lineNum("dates.start_day", "day", float64(s.GetDay()))
	}
	if e := r.GetEnd(); e != nil {
		d.lineNum("dates.end_year", "year", float64(e.GetYear()))
		d.lineNum("dates.end_month", "month", float64(e.GetMonth()))
		d.lineNum("dates.end_day", "day", float64(e.GetDay()))
	}
	years, ok = recordYears(r)
	if ok {
		d.lineMeasured("dates.record_years", "years", years)
	}
	return years, ok
}

func (d *doc) series(s *analysisv1.SeriesAnalysis, fallback string, years float64, hasYears bool) {
	if s == nil {
		return
	}
	u := s.GetUnits()
	if u == "" {
		u = fallback
	}
	d.h("Series")
	d.prose("series.variable_id", s.GetVariableId())
	d.prose("series.units", s.GetUnits())
	d.alpha("series", s.GetAlpha())
	if s.GetField() != analysisv1.SeriesField_SERIES_FIELD_UNSPECIFIED {
		d.lineStr("series.field", fieldName(s.GetField()))
	}
	d.lineNum("series.n_valid", "count", float64(s.GetNValid()))
	d.lineNum("series.n_total", "count", float64(s.GetNTotal()))
	d.regression(s.GetRegression(), u, years, hasYears)
	d.mannKendall(s.GetMannKendall(), u, years, hasYears)
	d.agreement(s.GetAgreement())
	d.pettitt(s.GetPettitt(), u)
	d.residuals(s.GetResidualChecks())
	d.outliers(s.GetOutliers(), u)
	d.shuffle(s.GetShuffle(), u)
	d.triangle(s.GetTrendTriangle(), u)
	d.monthly(s.GetMonthlyTrends(), u)
	d.yearsToSig(s.GetYearsToSignificance(), u)
	d.warnings(s.GetWarnings())
}

func (d *doc) alpha(prefix string, a float64) {
	if a == 0 {
		return
	}
	d.lineNum(prefix+".alpha", "1", a)
	if p, ok := confidencePercent(a); ok {
		d.lineNum(prefix+".confidence_percent", "percent", p)
	}
}

func (d *doc) regression(r *analysisv1.RegressionResult, u string, years float64, hasYears bool) {
	if r == nil {
		return
	}
	d.h("Regression")
	d.lineNum("regression.slope_per_decade", perDecade(u), r.GetSlopePerDecade())
	d.lineNum("regression.slope_ci_low", perDecade(u), r.GetSlopeCiLow())
	d.lineNum("regression.slope_ci_high", perDecade(u), r.GetSlopeCiHigh())
	d.lineNum("regression.p_value", "1", r.GetPValue())
	d.lineBool("regression.significant", r.GetSignificant())
	d.lineNum("regression.r_squared", "1", r.GetRSquared())
	d.lineNum("regression.n", "count", float64(r.GetN()))
	d.lineNum("regression.n_effective", "count", r.GetNEffective())
	d.model(r.GetModel(), u)
	d.derivedChanges("regression", r.GetSlopePerDecade(), u, years, hasYears)
}

func (d *doc) model(m *analysisv1.FittedModel, u string) {
	if m == nil {
		return
	}
	d.ymd("regression.model.time_origin", m.GetTimeOrigin())
	d.lineNum("regression.model.intercept", u, m.GetIntercept())
	d.lineNum("regression.model.intercept_stderr", u, m.GetInterceptStderr())
	d.lineNum("regression.model.slope_per_year", perYear(u), m.GetSlope())
	d.lineNum("regression.model.slope_stderr_per_year", perYear(u), m.GetSlopeStderr())
	d.lineNum("regression.model.residual_sd", u, m.GetResidualSd())
	d.lineNum("regression.model.lag1_autocorrelation", "1", m.GetLag1Autocorrelation())
}

func (d *doc) mannKendall(m *analysisv1.MannKendallResult, u string, years float64, hasYears bool) {
	if m == nil {
		return
	}
	d.h("Mann-Kendall")
	d.lineStr("mann_kendall.variant", variantName(m.GetVariant()))
	d.lineNum("mann_kendall.tau", "1", m.GetTau())
	d.lineNum("mann_kendall.z", "1", m.GetZ())
	d.lineNum("mann_kendall.p_value", "1", m.GetPValue())
	d.lineBool("mann_kendall.significant", m.GetSignificant())
	d.lineNum("mann_kendall.sens_slope_per_decade", perDecade(u), m.GetSensSlopePerDecade())
	d.lineNum("mann_kendall.sens_ci_low", perDecade(u), m.GetSensCiLow())
	d.lineNum("mann_kendall.sens_ci_high", perDecade(u), m.GetSensCiHigh())
	d.lineNum("mann_kendall.n", "count", float64(m.GetN()))
	d.derivedChanges("mann_kendall", m.GetSensSlopePerDecade(), u, years, hasYears)
}

// derivedChanges publishes the natural phrasings of a per-decade slope.
// total_change_over_record = slope_per_decade * (record_years / 10).
// total_change_per_century = slope_per_decade * 10.
// The model quotes these. It does not multiply the slope itself.
func (d *doc) derivedChanges(prefix string, slope float64, u string, years float64, hasYears bool) {
	if math.IsNaN(slope) || math.IsInf(slope, 0) {
		return
	}
	if hasYears {
		d.lineMeasured(prefix+".total_change_over_record", changeUnits(u), slope*(years/10))
	}
	d.lineMeasured(prefix+".total_change_per_century", perCentury(u), slope*10)
}

func (d *doc) agreement(a *analysisv1.Agreement) {
	if a == nil {
		return
	}
	d.h("Agreement")
	d.lineBool("agreement.same_direction", a.GetSameDirection())
	d.lineBool("agreement.same_significance", a.GetSameSignificance())
}

func (d *doc) pettitt(p *analysisv1.PettittResult, u string) {
	if p == nil {
		return
	}
	d.h("Pettitt")
	d.ymd("pettitt.change_time", p.GetChangeTime())
	d.lineNum("pettitt.p_value", "1", p.GetPValue())
	d.lineBool("pettitt.significant", p.GetSignificant())
	d.lineNum("pettitt.mean_before", u, p.GetMeanBefore())
	d.lineNum("pettitt.mean_after", u, p.GetMeanAfter())
}

func (d *doc) residuals(r *analysisv1.ResidualChecksResult) {
	if r == nil {
		return
	}
	d.h("Residual checks")
	d.lineStr("residual_checks.autocorrelation", checkName(r.GetAutocorrelation()))
	d.lineNum("residual_checks.lag1_autocorrelation", "1", r.GetLag1Autocorrelation())
	d.lineStr("residual_checks.normality", checkName(r.GetNormality()))
	d.lineNum("residual_checks.normality_p_value", "1", r.GetNormalityPValue())
}

// maxListedOutliers caps the individual outlier rows in the facts block.
// outliers.count still reports the full number.
const maxListedOutliers = 10

func (d *doc) outliers(o *analysisv1.OutliersResult, u string) {
	if o == nil {
		return
	}
	d.h("Outliers")
	d.lineNum("outliers.robust_z_threshold", "1", o.GetRobustZThreshold())
	items := o.GetOutliers()
	d.lineNum("outliers.count", "count", float64(len(items)))
	limit := len(items)
	if limit > maxListedOutliers {
		limit = maxListedOutliers
		d.lineNum("outliers.listed", "count", float64(maxListedOutliers))
	}
	for i := 0; i < limit; i++ {
		d.outlier(fmt.Sprintf("outliers.%d", i), items[i], u)
	}
	d.outlier("outliers.highest", o.GetHighest(), u)
	d.outlier("outliers.lowest", o.GetLowest(), u)
}

func (d *doc) outlier(prefix string, o *analysisv1.Outlier, u string) {
	if o == nil {
		return
	}
	// Day and month are not facts. A day like 15 would otherwise verify any
	// "15" in the answer. The year and the ISO date string stay.
	d.ymdYear(prefix, o.GetTime())
	d.lineNum(prefix+".value", u, o.GetValue())
	d.lineNum(prefix+".robust_z", "1", o.GetRobustZ())
}

func (d *doc) shuffle(sh *analysisv1.ShuffleResult, u string) {
	if sh == nil {
		return
	}
	d.h("Shuffle")
	d.lineNum("shuffle.permutations", "count", float64(sh.GetPermutations()))
	d.lineNum("shuffle.block_length", "count", float64(sh.GetBlockLength()))
	d.lineNum("shuffle.observed_slope_per_decade", perDecade(u), sh.GetObservedSlopePerDecade())
	d.lineNum("shuffle.p_value", "1", sh.GetPValue())
	slopes := sh.GetShuffledSlopesPerDecade()
	d.lineNum("shuffle.shuffled_count", "count", float64(len(slopes)))
	if len(slopes) <= 12 {
		for i, v := range slopes {
			d.lineNum(fmt.Sprintf("shuffle.shuffled_slopes_per_decade.%d", i), perDecade(u), v)
		}
		return
	}
	if mn, mx, n := finiteMinMax(slopes); n > 0 {
		d.lineNum("shuffle.shuffled_min_per_decade", perDecade(u), mn)
		d.lineNum("shuffle.shuffled_max_per_decade", perDecade(u), mx)
	}
}

func (d *doc) triangle(tr *analysisv1.TrendTriangleResult, u string) {
	if tr == nil {
		return
	}
	d.h("Trend triangle")
	d.lineNum("trend_triangle.min_years", "years", float64(tr.GetMinYears()))
	for i, y := range tr.GetStartYears() {
		d.lineNum(fmt.Sprintf("trend_triangle.start_years.%d", i), "year", float64(y))
	}
	for i, y := range tr.GetEndYears() {
		d.lineNum(fmt.Sprintf("trend_triangle.end_years.%d", i), "year", float64(y))
	}
	slopes := tr.GetSlopesPerDecade()
	if mn, mx, n := finiteMinMax(slopes); n > 0 {
		d.lineNum("trend_triangle.slope_min_per_decade", perDecade(u), mn)
		d.lineNum("trend_triangle.slope_max_per_decade", perDecade(u), mx)
		d.lineNum("trend_triangle.finite_windows", "count", float64(n))
	}
	if len(slopes) > 0 && len(slopes) <= 12 {
		for i, v := range slopes {
			d.lineNum(fmt.Sprintf("trend_triangle.slopes_per_decade.%d", i), perDecade(u), v)
		}
		for i, v := range tr.GetPValues() {
			d.lineNum(fmt.Sprintf("trend_triangle.p_values.%d", i), "1", v)
		}
	} else if ps := tr.GetPValues(); len(ps) > 12 {
		if mn, mx, n := finiteMinMax(ps); n > 0 {
			d.lineNum("trend_triangle.p_min", "1", mn)
			d.lineNum("trend_triangle.p_max", "1", mx)
		}
	}
}

func (d *doc) monthly(ms []*analysisv1.MonthlyTrend, u string) {
	if len(ms) == 0 {
		return
	}
	d.h("Monthly trends")
	for _, m := range ms {
		if m == nil {
			continue
		}
		p := fmt.Sprintf("monthly_trends.%d", m.GetMonth())
		d.lineNum(p+".month", "month", float64(m.GetMonth()))
		d.lineNum(p+".slope_per_decade", perDecade(u), m.GetSlopePerDecade())
		d.lineNum(p+".p_value", "1", m.GetPValue())
		d.lineBool(p+".significant", m.GetSignificant())
	}
}

func (d *doc) yearsToSig(y *analysisv1.YearsToSignificanceResult, u string) {
	if y == nil {
		return
	}
	d.h("Years to significance")
	d.lineNum("years_to_significance.years_needed", "years", y.GetYearsNeeded())
	d.lineNum("years_to_significance.years_so_far", "years", y.GetYearsSoFar())
	d.lineNum("years_to_significance.noise_sd", u, y.GetNoiseSd())
	d.lineNum("years_to_significance.trend_per_year", perYear(u), y.GetTrendPerYear())
	d.lineNum("years_to_significance.phi", "1", y.GetPhi())
	d.lineBool("years_to_significance.already_significant", y.GetAlreadySignificant())
	// The statistic is defined as a 90 percent detection probability.
	d.lineNum("years_to_significance.detection_probability_percent", "percent", 90)
}

func (d *doc) warnings(ws []string) {
	if len(ws) == 0 {
		return
	}
	d.h("Warnings")
	for i, w := range ws {
		d.prose(fmt.Sprintf("warnings.%d", i), w)
	}
}

// grid writes the spatial facts. fallback is the series units, used when the
// grid doesn't name its own.
func (d *doc) grid(g *analysisv1.GridAnalysis, fallback string) {
	if g == nil {
		return
	}
	d.h("Grid")
	d.prose("grid.variable_id", g.GetVariableId())
	d.prose("grid.units", g.GetUnits())
	d.alpha("grid", g.GetAlpha())
	if g.GetFdrQ() != 0 {
		d.lineNum("grid.fdr_q", "1", g.GetFdrQ())
	}
	d.lineNum("grid.rows", "count", float64(g.GetRows()))
	d.lineNum("grid.cols", "count", float64(g.GetCols()))
	if g.GetRows() > 0 && g.GetCols() > 0 {
		d.lineNum("grid.cells", "count", float64(g.GetRows())*float64(g.GetCols()))
	}
	if bb := g.GetBbox(); bb != nil {
		d.lineNum("grid.bbox.west", "degrees", bb.GetWest())
		d.lineNum("grid.bbox.south", "degrees", bb.GetSouth())
		d.lineNum("grid.bbox.east", "degrees", bb.GetEast())
		d.lineNum("grid.bbox.north", "degrees", bb.GetNorth())
	}
	if lats := g.GetLats(); len(lats) > 0 {
		d.lineNum("grid.lat_north", "degrees", lats[0])
		d.lineNum("grid.lat_south", "degrees", lats[len(lats)-1])
	}
	if lons := g.GetLons(); len(lons) > 0 {
		d.lineNum("grid.lon_west", "degrees", lons[0])
		d.lineNum("grid.lon_east", "degrees", lons[len(lons)-1])
	}
	if vs := g.GetValidSteps(); len(vs) > 0 {
		mn, mx := vs[0], vs[0]
		for _, v := range vs[1:] {
			if v < mn {
				mn = v
			}
			if v > mx {
				mx = v
			}
		}
		d.lineNum("grid.valid_steps_min", "count", float64(mn))
		d.lineNum("grid.valid_steps_max", "count", float64(mx))
	}
	u := g.GetUnits()
	if u == "" {
		u = fallback
	}
	for i, layer := range g.GetLayers() {
		if layer == nil {
			continue
		}
		p := fmt.Sprintf("grid.layer.%d", i)
		d.lineStr(p+".test", gridTestName(layer.GetTest()))
		d.lineNum(p+".p_threshold", "1", layer.GetPThreshold())
		d.lineNum(p+".significant_cells", "count", float64(countTrue(layer.GetSignificant())))
		if mn, mx, n := finiteMinMax(layer.GetSlopesPerDecade()); n > 0 {
			d.lineNum(p+".finite_cells", "count", float64(n))
			d.lineNum(p+".slope_min_per_decade", perDecade(u), mn)
			d.lineNum(p+".slope_max_per_decade", perDecade(u), mx)
		}
		d.gridWhere(p, g, layer, u)
	}
}

// gridWhere counts significant cells that increase and that decrease, and
// locates the strongest of each. Cells are row-major, row 0 at the north and
// column 0 at the west. Lat and lon are the cell center. A tie keeps the
// earlier cell. A significant slope of zero is neither an increase nor a
// decrease.
func (d *doc) gridWhere(prefix string, g *analysisv1.GridAnalysis, layer *analysisv1.GridLayer, u string) {
	slopes := layer.GetSlopesPerDecade()
	sig := layer.GetSignificant()
	n := len(slopes)
	if len(sig) < n {
		n = len(sig)
	}
	cols := int(g.GetCols())
	lats, lons := g.GetLats(), g.GetLons()
	incN, decN := 0, 0
	incAt, decAt := -1, -1
	var incSlope, decSlope float64
	for i := 0; i < n; i++ {
		if !sig[i] {
			continue
		}
		v := slopes[i]
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		switch {
		case v > 0:
			incN++
			if incAt < 0 || v > incSlope {
				incAt, incSlope = i, v
			}
		case v < 0:
			decN++
			if decAt < 0 || v < decSlope {
				decAt, decSlope = i, v
			}
		}
	}
	d.lineNum(prefix+".significant_increase_cells", "count", float64(incN))
	d.lineNum(prefix+".significant_decrease_cells", "count", float64(decN))
	d.cellExtreme(prefix+".strongest_increase", incAt, incSlope, cols, lats, lons, u)
	d.cellExtreme(prefix+".strongest_decrease", decAt, decSlope, cols, lats, lons, u)
}

func (d *doc) cellExtreme(prefix string, idx int, slope float64, cols int, lats, lons []float64, u string) {
	if idx < 0 {
		return
	}
	if cols > 0 {
		row, col := idx/cols, idx%cols
		if row < len(lats) && col < len(lons) {
			d.lineNum(prefix+".lat", "degrees", lats[row])
			d.lineNum(prefix+".lon", "degrees", lons[col])
		}
	}
	d.lineNum(prefix+".slope_per_decade", perDecade(u), slope)
}

func (d *doc) ymd(prefix string, ts *timestamppb.Timestamp) {
	d.writeYMD(prefix, ts, true)
}

// ymdYear publishes the year only. Outlier days and months stay in the ISO
// date string and are not facts.
func (d *doc) ymdYear(prefix string, ts *timestamppb.Timestamp) {
	d.writeYMD(prefix, ts, false)
}

func (d *doc) writeYMD(prefix string, ts *timestamppb.Timestamp, monthDay bool) {
	if ts == nil {
		return
	}
	t := ts.AsTime().UTC()
	d.lineStr(prefix+".time", t.Format("2006-01-02"))
	d.lineNum(prefix+".year", "year", float64(t.Year()))
	if !monthDay {
		return
	}
	d.lineNum(prefix+".month", "month", float64(int(t.Month())))
	d.lineNum(prefix+".day", "day", float64(t.Day()))
}

func unitsOf(s *analysisv1.SeriesAnalysis, v *narratorv1.VariableContext) string {
	if s != nil && s.GetUnits() != "" {
		return s.GetUnits()
	}
	if v != nil {
		return v.GetUnits()
	}
	return ""
}

func perDecade(u string) string {
	if u == "" {
		return "per decade"
	}
	return u + " per decade"
}

func perYear(u string) string {
	if u == "" {
		return "per year"
	}
	return u + " per year"
}

func perCentury(u string) string {
	if u == "" {
		return "per century"
	}
	return u + " per century"
}

func changeUnits(u string) string {
	if u == "" {
		return "change"
	}
	return u
}

// meanGregorianYear is the average year length. Both ends of the date range
// count, so 1 Jan 2001 through 31 Dec 2020 is 7305 days, exactly 20 years.
const meanGregorianYear = 365.25

// recordYears is the inclusive length of the requested record in years.
func recordYears(r *commonv1.DateRange) (float64, bool) {
	if r == nil || r.GetStart() == nil || r.GetEnd() == nil {
		return 0, false
	}
	start, ok1 := civilDate(r.GetStart())
	end, ok2 := civilDate(r.GetEnd())
	if !ok1 || !ok2 {
		return 0, false
	}
	if end.Before(start) {
		start, end = end, start
	}
	days := end.Sub(start).Hours()/24 + 1
	if days <= 0 {
		return 0, false
	}
	return days / meanGregorianYear, true
}

func civilDate(d *commonv1.Date) (time.Time, bool) {
	if d == nil {
		return time.Time{}, false
	}
	y, m, day := int(d.GetYear()), int(d.GetMonth()), int(d.GetDay())
	if y < 1 || m < 1 || m > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != m || t.Day() != day {
		return time.Time{}, false
	}
	return t, true
}

func confidencePercent(alpha float64) (float64, bool) {
	if alpha <= 0 || alpha >= 1 {
		return 0, false
	}
	return math.Round((1-alpha)*100*1e6) / 1e6, true
}

func formatFloat(v float64) string {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return "0"
	}
	abs := math.Abs(v)
	if abs >= 1e-4 && abs < 1e7 {
		s := strconv.FormatFloat(v, 'f', 8, 64)
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
		if s == "" || s == "-" {
			return "0"
		}
		return s
	}
	return strconv.FormatFloat(v, 'e', -1, 64)
}

func finiteMinMax(vs []float64) (min, max float64, n int) {
	min, max = math.Inf(1), math.Inf(-1)
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		n++
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return min, max, n
}

func countTrue(bs []bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

func fieldName(f analysisv1.SeriesField) string {
	switch f {
	case analysisv1.SeriesField_SERIES_FIELD_ANOMALIES:
		return "anomalies"
	case analysisv1.SeriesField_SERIES_FIELD_VALUES:
		return "values"
	default:
		return "unspecified"
	}
}

func variantName(v analysisv1.MannKendallVariant) string {
	switch v {
	case analysisv1.MannKendallVariant_MANN_KENDALL_VARIANT_ORIGINAL:
		return "original"
	case analysisv1.MannKendallVariant_MANN_KENDALL_VARIANT_HAMED_RAO:
		return "hamed_rao"
	case analysisv1.MannKendallVariant_MANN_KENDALL_VARIANT_SEASONAL:
		return "seasonal"
	default:
		return "unspecified"
	}
}

func checkName(c analysisv1.CheckStatus) string {
	switch c {
	case analysisv1.CheckStatus_CHECK_STATUS_PASS:
		return "pass"
	case analysisv1.CheckStatus_CHECK_STATUS_WARN:
		return "warn"
	default:
		return "unspecified"
	}
}

func gridTestName(t *analysisv1.GridTest) string {
	if t == nil {
		return "unknown"
	}
	switch t.GetTest().(type) {
	case *analysisv1.GridTest_Regression:
		return "regression"
	case *analysisv1.GridTest_MannKendall:
		return "mann_kendall"
	default:
		return "unknown"
	}
}

func mapHistory(h []*narratorv1.ChatMessage) []llm.Message {
	out := make([]llm.Message, 0, len(h))
	for _, m := range h {
		if m == nil || m.GetText() == "" {
			continue
		}
		role := llm.RoleUser
		if m.GetRole() == narratorv1.Role_ROLE_ASSISTANT {
			role = llm.RoleAssistant
		}
		out = append(out, llm.Message{Role: role, Text: m.GetText()})
	}
	return out
}

// ensureUserFirst inserts the overview question when the first kept turn is
// the assistant. Providers that require a user message first reject the
// history the browser sends after an overview.
func ensureUserFirst(msgs []llm.Message) []llm.Message {
	if len(msgs) == 0 || msgs[0].Role != llm.RoleAssistant {
		return msgs
	}
	out := make([]llm.Message, 0, len(msgs)+1)
	out = append(out, llm.Message{Role: llm.RoleUser, Text: overviewQuestion})
	out = append(out, msgs...)
	return out
}

func userText(q string) string {
	if strings.TrimSpace(q) == "" {
		return overviewQuestion
	}
	return q
}
