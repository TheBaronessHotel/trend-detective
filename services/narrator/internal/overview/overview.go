// Package overview writes a plain-language summary of finished analysis
// results without an LLM. The narrator streams it when the user has no API
// key, so the app still explains every result.
//
// The text uses only values from the results, rounded for reading, and never
// computes a new statistic. It is three to five sentences for students:
// direction and rate per decade with the confidence interval, significance,
// record length, then the main caveats, and always a reminder that a trend is
// not proof of a cause.
package overview

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	analysisv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/analysis/v1"
	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
)

// Model is what NarrateDone.model says for a template overview.
const Model = "template"

const (
	maxSentences = 5
	// maxWarnings is how many caveats the text quotes. The app lists all.
	maxWarnings = 2
)

// Build returns the overview. Any argument may be nil. It never fails: when a
// test is missing, the text says so instead of guessing.
func Build(series *analysisv1.SeriesAnalysis, grid *analysisv1.GridAnalysis, variable *narratorv1.VariableContext, dates *commonv1.DateRange) string {
	return strings.Join(BuildSentences(series, grid, variable, dates), "")
}

// BuildSentences returns the overview one sentence per element, each with
// its trailing space except the last, so joining them gives Build's text.
// The server streams one element per text_delta. Splitting Build's text
// instead would break a warning that holds a period of its own.
func BuildSentences(series *analysisv1.SeriesAnalysis, grid *analysisv1.GridAnalysis, variable *narratorv1.VariableContext, dates *commonv1.DateRange) []string {
	o := newOverview(series, variable, dates)
	required := []string{}
	if t, ok := o.trend(); ok {
		required = append(required, o.rateSentence(t), o.significanceSentence(t))
		if s := o.shortRecordSentence(t); s != "" {
			required = append(required, s)
		}
	} else {
		required = append(required, o.noTrendSentence(), o.recordSentence())
	}
	cause := fmt.Sprintf("Remember that a trend is not proof of a cause: this shows how %s changed, not why.", o.name)

	// Caveats fill the remaining room, most important first.
	room := maxSentences - len(required) - 1
	var extra []string
	for _, s := range []string{o.warningsSentence(), o.changePointSentence(), gridSentence(grid)} {
		if s != "" && len(extra) < room {
			extra = append(extra, s)
		}
	}
	all := append(append(required, extra...), cause)
	for i := range all[:len(all)-1] {
		all[i] += " "
	}
	return all
}

type overview struct {
	s     *analysisv1.SeriesAnalysis
	dates *commonv1.DateRange
	name  string
	units string
	alpha float64
}

func newOverview(s *analysisv1.SeriesAnalysis, v *narratorv1.VariableContext, dates *commonv1.DateRange) *overview {
	if s == nil {
		s = &analysisv1.SeriesAnalysis{}
	}
	name := firstNonEmpty(v.GetName(), v.GetId(), s.GetVariableId())
	if name == "" {
		name = "the variable"
	} else {
		name = lowerFirst(name)
	}
	return &overview{
		s:     s,
		dates: dates,
		name:  name,
		units: firstNonEmpty(s.GetUnits(), v.GetUnits()),
		alpha: s.GetAlpha(),
	}
}

const (
	methodRegression = "linear regression"
	methodSen        = "Sen's slope"
)

// trendFacts is the trend estimate one test produced.
type trendFacts struct {
	method      string // methodRegression or methodSen
	slope       float64
	low, high   float64
	p           float64
	significant bool
	// mk is set when the regression is the main estimate and Mann-Kendall
	// ran too, to say whether the two tests agree.
	mk *analysisv1.MannKendallResult
}

// trend picks the regression, or Sen's slope from Mann-Kendall when there is
// no regression.
func (o *overview) trend() (trendFacts, bool) {
	if r := o.s.GetRegression(); r != nil && finite(r.GetSlopePerDecade()) {
		return trendFacts{
			method: methodRegression, slope: r.GetSlopePerDecade(),
			low: r.GetSlopeCiLow(), high: r.GetSlopeCiHigh(),
			p: r.GetPValue(), significant: r.GetSignificant(), mk: o.s.GetMannKendall(),
		}, true
	}
	if m := o.s.GetMannKendall(); m != nil && finite(m.GetSensSlopePerDecade()) {
		return trendFacts{
			method: methodSen, slope: m.GetSensSlopePerDecade(),
			low: m.GetSensCiLow(), high: m.GetSensCiHigh(),
			p: m.GetPValue(), significant: m.GetSignificant(),
		}, true
	}
	return trendFacts{}, false
}

// rateSentence gives the period, the record length, the direction and the
// rate with its confidence interval.
func (o *overview) rateSentence(t trendFacts) string {
	var b strings.Builder
	b.WriteString(o.periodPrefix())
	fmt.Fprintf(&b, "the average %s in the selected region%s ", o.name, o.fieldNote())
	hasCI := finite(t.low) && finite(t.high) && t.low <= t.high && (t.low != 0 || t.high != 0)
	dec := decimalsFor(t.slope, t.low, t.high)
	if hasCI {
		dec = keepBounds(dec, t.low, t.high)
	}
	amount := num(math.Abs(t.slope), dec) + " " + o.perDecade()
	switch {
	case t.slope == 0:
		fmt.Fprintf(&b, "shows no change (%s)", amount)
	case t.slope > 0:
		fmt.Fprintf(&b, "rose by about %s", amount)
	default:
		fmt.Fprintf(&b, "fell by about %s", amount)
	}
	if t.method != methodRegression {
		fmt.Fprintf(&b, " (%s)", t.method)
	}
	if hasCI {
		fmt.Fprintf(&b, ", and the %s for that rate runs from %s to %s", o.ciName(), num(t.low, dec), num(t.high, dec))
	}
	b.WriteString(".")
	return b.String()
}

// significanceSentence says whether the trend is significant, compares p to
// alpha, and says whether Mann-Kendall agrees with the regression.
func (o *overview) significanceSentence(t trendFacts) string {
	var b strings.Builder
	if t.significant {
		b.WriteString("This trend is statistically significant")
	} else {
		b.WriteString("This trend is not statistically significant")
	}
	if finite(t.p) {
		fmt.Fprintf(&b, " (%s%s)", pText(t.p, o.alpha), o.alphaComparison(t.p, t.significant))
	}
	if !t.significant {
		b.WriteString(", so natural ups and downs could explain it")
	}
	if t.mk != nil {
		b.WriteString(o.agreementClause(t))
	}
	b.WriteString(".")
	return b.String()
}

// shortRecordYears is the record length below which a significant result
// isn't called a trend, the same rule as the web app's verdict.
const shortRecordYears = 10

// shortRecordSentence warns, for a significant result on fewer than
// shortRecordYears of monthly record, that it's too short to call a trend.
func (o *overview) shortRecordSentence(t trendFacts) string {
	if !t.significant || o.s.GetNTotal() <= 0 {
		return ""
	}
	years := float64(o.s.GetNTotal()) / 12
	if years >= shortRecordYears {
		return ""
	}
	return fmt.Sprintf("But %s of record is too short to call this a trend: a few unusual months can make one look real, and at least %d years is safer.",
		yearsText(years), shortRecordYears)
}

// yearsText says a record length: "2 years", "1 year", "2.5 years".
func yearsText(y float64) string {
	r := math.Round(y*10) / 10
	if r == 1 {
		return "1 year"
	}
	return trimNum(r) + " years"
}

// agreementClause compares the regression with Mann-Kendall. It trusts the
// analysis's Agreement when set and compares the two results otherwise.
func (o *overview) agreementClause(t trendFacts) string {
	mk := t.mk
	sameDir := sameSign(mk.GetSensSlopePerDecade(), t.slope)
	sameSig := mk.GetSignificant() == t.significant
	if a := o.s.GetAgreement(); a != nil {
		sameDir, sameSig = a.GetSameDirection(), a.GetSameSignificance()
	}
	p := ""
	if finite(mk.GetPValue()) {
		p = " (" + pText(mk.GetPValue(), o.alpha) + ")"
	}
	const mkName = "the Mann-Kendall test, which is less sensitive to extreme months,"
	switch {
	case sameDir && sameSig:
		return ", and " + mkName + " agrees" + p
	case !sameDir:
		return ", but " + mkName + " points the other way" + p + ", so even the direction is uncertain"
	case t.significant:
		return ", but " + mkName + " does not find a significant trend" + p + ", so a few unusual months may be driving the result"
	default:
		return ", but " + mkName + " does find a significant trend" + p + ", so the evidence is mixed"
	}
}

// alphaComparison places p against alpha, but only when that matches the
// significance flag the analysis set.
func (o *overview) alphaComparison(p float64, significant bool) string {
	if o.alpha <= 0 || o.alpha >= 1 {
		return ""
	}
	a := trimNum(o.alpha)
	switch {
	case p < o.alpha && significant:
		return ", below the significance level of " + a
	case p >= o.alpha && !significant:
		return ", above the significance level of " + a
	}
	return ", with a significance level of " + a
}

func (o *overview) noTrendSentence() string {
	return fmt.Sprintf("The results for %s have no trend estimate, because neither the linear regression nor the Mann-Kendall test produced a result, so the rate of change is not available.", o.name)
}

// recordSentence states the record length on its own, for when there is no
// trend sentence to carry it.
func (o *overview) recordSentence() string {
	period := o.periodText()
	values := o.valuesText()
	switch {
	case period != "" && values != "":
		return fmt.Sprintf("The record runs %s with %s.", period, values)
	case period != "":
		return fmt.Sprintf("The record runs %s.", period)
	case values != "":
		return fmt.Sprintf("The record has %s.", values)
	}
	return "The record length is not available either."
}

func (o *overview) changePointSentence() string {
	pt := o.s.GetPettitt()
	if pt == nil || !pt.GetSignificant() {
		return ""
	}
	var b strings.Builder
	b.WriteString("The Pettitt test found a likely sudden shift")
	if ts := pt.GetChangeTime(); ts != nil && ts.IsValid() {
		t := ts.AsTime().UTC()
		fmt.Fprintf(&b, " around %s %d", t.Month(), t.Year())
	}
	if finite(pt.GetPValue()) {
		fmt.Fprintf(&b, " (%s)", pText(pt.GetPValue(), o.alpha))
	}
	before, after := pt.GetMeanBefore(), pt.GetMeanAfter()
	if finite(before) && finite(after) && before != after {
		dec := decimalsFor(before, after)
		fmt.Fprintf(&b, ", when the average moved from %s to %s%s", num(before, dec), num(after, dec), o.unitSuffix())
	}
	b.WriteString(", and a step like that, for example from a new sensor, can look like a trend.")
	return b.String()
}

func (o *overview) warningsSentence() string {
	var ws []string
	for _, w := range o.s.GetWarnings() {
		w = strings.TrimRight(strings.TrimSpace(w), ".;")
		if w != "" {
			ws = append(ws, w)
		}
	}
	if len(ws) == 0 {
		return ""
	}
	shown := ws
	if len(shown) > maxWarnings {
		shown = shown[:maxWarnings]
	}
	label := "Caveat"
	if len(ws) > 1 {
		label = "Caveats"
	}
	s := label + ": " + strings.Join(shown, "; ")
	if more := len(ws) - len(shown); more > 0 {
		s += fmt.Sprintf(" (plus %d more in the app)", more)
	}
	return s + "."
}

// gridSentence counts significant heatmap cells in the first grid layer.
func gridSentence(g *analysisv1.GridAnalysis) string {
	if g == nil || len(g.GetLayers()) == 0 {
		return ""
	}
	l := g.GetLayers()[0]
	cells, sig := 0, 0
	for i, v := range l.GetSlopesPerDecade() {
		if !finite(v) {
			continue
		}
		cells++
		if i < len(l.GetSignificant()) && l.GetSignificant()[i] {
			sig++
		}
	}
	if cells == 0 {
		return ""
	}
	return fmt.Sprintf("On the heatmap, %d of %d grid cells show a significant trend after correcting for testing many cells at once.", sig, cells)
}

// periodPrefix opens the rate sentence, like "From January 2001 to December
// 2020 (240 values), ".
func (o *overview) periodPrefix() string {
	period, values := o.periodText(), o.valuesText()
	switch {
	case period != "" && values != "":
		return "F" + period[1:] + " (" + values + "), "
	case period != "":
		return "F" + period[1:] + ", "
	case values != "":
		return "Over " + values + ", "
	}
	return "Over the record, "
}

// periodText is "from January 2001 to December 2020", or "" without dates.
func (o *overview) periodText() string {
	start, end := dateText(o.dates.GetStart()), dateText(o.dates.GetEnd())
	if start == "" || end == "" {
		return ""
	}
	return "from " + start + " to " + end
}

// valuesText is "240 values" or "228 of 240 time steps with data".
func (o *overview) valuesText() string {
	valid, total := o.s.GetNValid(), o.s.GetNTotal()
	switch {
	case valid > 0 && total > valid:
		return fmt.Sprintf("%d of %d time steps with data", valid, total)
	case valid > 0:
		return fmt.Sprintf("%d values", valid)
	case total > 0:
		return fmt.Sprintf("%d time steps", total)
	}
	return ""
}

func dateText(d *commonv1.Date) string {
	if d == nil || d.GetYear() == 0 {
		return ""
	}
	if m := d.GetMonth(); m >= 1 && m <= 12 {
		return fmt.Sprintf("%s %d", time.Month(m), d.GetYear())
	}
	return strconv.Itoa(int(d.GetYear()))
}

func (o *overview) fieldNote() string {
	if o.s.GetField() == analysisv1.SeriesField_SERIES_FIELD_ANOMALIES {
		return ", with the seasonal cycle removed,"
	}
	return ""
}

func (o *overview) perDecade() string {
	if o.units == "" {
		return "per decade"
	}
	return o.units + " per decade"
}

func (o *overview) unitSuffix() string {
	if o.units == "" {
		return ""
	}
	return " " + o.units
}

// ciName is "95% confidence interval", or "confidence interval" when alpha
// is unknown.
func (o *overview) ciName() string {
	if o.alpha > 0 && o.alpha < 1 {
		return trimNum(math.Round((1-o.alpha)*1e4)/1e2) + "% confidence interval"
	}
	return "confidence interval"
}

// pText is "p = 0.012", or "p < 0.001" for tiny values. It shows up to
// three decimals, without trailing zeros, and more when the rounded value
// would equal alpha or land on its other side: p = 0.0496 with alpha 0.05
// reads "p = 0.0496", never "p = 0.05". An alpha outside (0, 1) is ignored.
func pText(p, alpha float64) string {
	if p < 0.001 {
		return "p < 0.001"
	}
	dec := min(decimalsFor(p), 3)
	s := trimZeros(num(p, dec))
	if alpha > 0 && alpha < 1 {
		for dec < 6 {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil || (v != alpha && (v < alpha) == (p < alpha)) {
				break
			}
			dec++
			s = trimZeros(num(p, dec))
		}
	}
	return "p = " + s
}

func trimZeros(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// keepBounds raises dec until each nonzero bound of an interval keeps a
// significant digit, so an interval like -0.04 to 2.4 doesn't read "0.0 to
// 2.4" and hide that it crosses zero. It stops at 6 decimals.
func keepBounds(dec int, bounds ...float64) int {
	for _, v := range bounds {
		if !finite(v) || v == 0 {
			continue
		}
		need := -int(math.Floor(math.Log10(math.Abs(v))))
		if strings.Trim(num(v, dec), "-0.") == "" {
			dec = max(dec, min(need, 6))
		}
	}
	return dec
}

// decimalsFor returns how many decimals show the first value that is finite
// and not zero with two significant digits. The others are printed with the
// same decimals so a rate and its interval line up.
func decimalsFor(vs ...float64) int {
	for _, v := range vs {
		if !finite(v) || v == 0 {
			continue
		}
		d := 1 - int(math.Floor(math.Log10(math.Abs(v))))
		return min(max(d, 0), 6)
	}
	return 2
}

// num rounds v to dec decimals. Trailing zeros stay, so a rate and its
// interval show the same precision.
func num(v float64, dec int) string {
	s := strconv.FormatFloat(v, 'f', dec, 64)
	if strings.HasPrefix(s, "-") && strings.Trim(s, "-0.") == "" {
		s = s[1:]
	}
	return s
}

func trimNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func sameSign(a, b float64) bool { return (a > 0) == (b > 0) && (a < 0) == (b < 0) }

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// lowerFirst lowercases a capitalized word, like "Precipitation", but leaves
// acronyms and names with more capitals, like "NO2" or "GRACE water", alone.
func lowerFirst(s string) string {
	first, size := utf8.DecodeRuneInString(s)
	next, _ := utf8.DecodeRuneInString(s[size:])
	if unicode.IsUpper(first) && (size == len(s) || !unicode.IsUpper(next) && !unicode.IsDigit(next)) {
		return string(unicode.ToLower(first)) + s[size:]
	}
	return s
}
