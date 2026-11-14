package verify

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/prompt"
)

// verifier implements Verifier.
type verifier struct{}

// NewVerifier returns a Verifier that checks an answer against facts.
func NewVerifier() Verifier { return verifier{} }

// Unverified lists number tokens in answer, as written, that match no fact.
// Years inside dates.start_year..dates.end_year, dates written inside that
// range, and plain integers from 0 to 10 (counts) are allowed. Matching allows
// rounding to the decimals shown, a percent form of a fraction, per-year as
// per-decade/10, and the absolute value after a decrease word. A threshold
// written after p < or p ≤ is allowed when some p-value fact is <= that
// threshold. "12 months" and "per 10 years" are calendar phrases, not results.
// ScanNumbers already drops tokens glued to letters, such as MERRA-2 and 20th.
func (verifier) Unverified(answer string, facts []prompt.Fact) []string {
	nums := prompt.ScanNumbers(answer)
	b := dateBounds(facts)
	spans := whitelistSpans(answer, b)
	var out []string
	seen := map[string]bool{}
	for _, n := range nums {
		if n.PlainInt && !n.Neg && !n.Percent && n.Value >= 0 && n.Value <= 10 {
			continue
		}
		if yearToken(n, b) || covered(n, spans) || ignoredPhrase(answer, n) || tokenMatches(answer, n, facts) {
			continue
		}
		if seen[n.Raw] {
			continue
		}
		seen[n.Raw] = true
		out = append(out, n.Raw)
	}
	return out
}

func tokenMatches(answer string, n prompt.Number, facts []prompt.Fact) bool {
	if pValueThreshold(answer, n, facts) {
		return true
	}
	dec := decreaseBefore(answer, n.Start)
	for _, f := range facts {
		if factMatches(n, f, dec) {
			return true
		}
	}
	return false
}

// pValueThreshold allows "p < 0.001", "p ≤ 0.05", "p<0.01" and "p <= 0.05"
// when some fact whose name contains "p_value" is <= the threshold. Any
// p-value fact qualifies. A comparison that is not a p-value does not.
func pValueThreshold(answer string, n prompt.Number, facts []prompt.Fact) bool {
	if n.Start < 0 || n.Start > len(answer) || !hasPCompare(answer, n.Start) {
		return false
	}
	for _, f := range facts {
		if !isPValueFact(f) || math.IsNaN(f.Value) || math.IsInf(f.Value, 0) {
			continue
		}
		for _, rd := range readings(n) {
			thr := rd.val
			if n.Percent {
				thr = rd.val / 100
			}
			if f.Value <= thr+1e-12 {
				return true
			}
		}
	}
	return false
}

func isPValueFact(f prompt.Fact) bool {
	return strings.Contains(f.Name, "p_value")
}

// hasPCompare reports whether a p-value comparison operator sits immediately
// before start. It accepts "<", "≤" and "<=".
func hasPCompare(answer string, start int) bool {
	i := trimSpaceLeft(answer, start)
	if i > 0 {
		r, size := utf8.DecodeLastRuneInString(answer[:i])
		if r == '=' {
			i = trimSpaceLeft(answer, i-size)
		}
	}
	if i == 0 {
		return false
	}
	r, size := utf8.DecodeLastRuneInString(answer[:i])
	if r != '<' && r != '\u2264' {
		return false
	}
	return endsWithP(answer[:i-size])
}

func trimSpaceLeft(s string, i int) int {
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:i])
		if r != ' ' && r != '\t' {
			break
		}
		i -= size
	}
	return i
}

func endsWithP(s string) bool {
	s = strings.TrimRight(s, " \t")
	lower := strings.ToLower(s)
	// Longer forms first so "p-value" is not judged only by its final "p".
	for _, suf := range []string{"p-value", "p value", "pvalue", "p"} {
		if !strings.HasSuffix(lower, suf) {
			continue
		}
		head := lower[:len(lower)-len(suf)]
		if head == "" {
			return true
		}
		r, _ := utf8.DecodeLastRuneInString(head)
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// ignoredPhrase skips the calendar wording "12 months" and "per 10 years".
// Other counts still have to be facts.
func ignoredPhrase(answer string, n prompt.Number) bool {
	if !n.PlainInt || n.Neg || n.Percent || n.Sci {
		return false
	}
	switch n.Value {
	case 12:
		return isMonthWord(nextWord(answer[n.End:]))
	case 10:
		word := nextWord(answer[n.End:])
		return (word == "year" || word == "years") && prevWord(answer[:n.Start]) == "per"
	default:
		return false
	}
}

func isMonthWord(word string) bool {
	switch word {
	case "month", "months":
		return true
	default:
		return false
	}
}

func nextWord(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	j := i
	for j < len(s) {
		r, size := utf8.DecodeRuneInString(s[j:])
		if !unicode.IsLetter(r) {
			break
		}
		j += size
	}
	return strings.ToLower(s[i:j])
}

func prevWord(s string) string {
	i := len(s)
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:i])
		if r != ' ' && r != '\t' {
			break
		}
		i -= size
	}
	j := i
	for j > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:j])
		if !unicode.IsLetter(r) {
			break
		}
		j -= size
	}
	return strings.ToLower(s[j:i])
}

func factMatches(n prompt.Number, f prompt.Fact, dec bool) bool {
	if math.IsNaN(f.Value) || math.IsInf(f.Value, 0) {
		return false
	}
	mags := []float64{f.Value}
	if dec && !n.Neg {
		mags = append(mags, math.Abs(f.Value))
	}
	reads := readings(n)
	for _, mag := range mags {
		for _, sc := range scalesOf(f) {
			base := mag * sc
			for _, rd := range reads {
				if n.Percent {
					if samePrec(base*100, rd.val, n.Places) || samePrec(base, rd.val, n.Places) {
						return true
					}
					continue
				}
				if samePrec(base, rd.val, rd.places) {
					return true
				}
			}
		}
	}
	return false
}

type reading struct {
	val    float64
	places int
}

func readings(n prompt.Number) []reading {
	out := []reading{{n.Value, absPlaces(n.Places, n.Exp, n.Sci)}}
	if n.HasAlt {
		out = append(out, reading{n.Alt, absPlaces(n.AltPlaces, n.Exp, n.Sci)})
	}
	return out
}

func absPlaces(places, exp int, sci bool) int {
	if !sci {
		return places
	}
	return places - exp
}

func scalesOf(f prompt.Fact) []float64 {
	s := []float64{1}
	blob := strings.ToLower(f.Name + " " + f.Units)
	if strings.Contains(blob, "per_decade") || strings.Contains(blob, "per decade") || strings.Contains(blob, "/decade") {
		s = append(s, 0.1)
	}
	if strings.Contains(blob, "per_year") || strings.Contains(blob, "per year") || strings.Contains(blob, "/year") {
		s = append(s, 10)
	}
	return s
}

// samePrec reports whether a and b match once both are rounded half away
// from zero to the given decimal places. A negative places value rounds to
// tens, hundreds, and so on (scientific notation).
func samePrec(a, b float64, places int) bool {
	if math.IsNaN(a) || math.IsNaN(b) || math.IsInf(a, 0) || math.IsInf(b, 0) {
		return false
	}
	return formatAt(a, places) == formatAt(b, places)
}

func formatAt(v float64, places int) string {
	sign := ""
	if v < 0 || math.Signbit(v) {
		sign = "-"
		v = -v
	}
	var out string
	if places < 0 {
		if places < -12 {
			places = -12
		}
		step := math.Pow10(-places)
		out = strconv.FormatFloat(math.Round(v/step)*step, 'f', 0, 64)
	} else {
		if places > 15 {
			places = 15
		}
		out = roundHalfAway(strconv.FormatFloat(v, 'f', places+6, 64), places)
	}
	if sign == "-" && !allZero(out) {
		return "-" + out
	}
	return out
}

func roundHalfAway(abs string, places int) string {
	parts := strings.SplitN(abs, ".", 2)
	intPart := parts[0]
	if intPart == "" {
		intPart = "0"
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if places == 0 {
		if len(frac) > 0 && frac[0] >= '5' {
			intPart = incDigits(intPart)
		}
		return intPart
	}
	for len(frac) < places+1 {
		frac += "0"
	}
	keep := frac[:places]
	if frac[places] >= '5' {
		keep, intPart = incFrac(keep, intPart)
	}
	return intPart + "." + keep
}

func incFrac(frac, intPart string) (string, string) {
	b := []byte(frac)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < '9' {
			b[i]++
			return string(b), intPart
		}
		b[i] = '0'
	}
	return string(b), incDigits(intPart)
}

func incDigits(s string) string {
	b := []byte(s)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < '9' {
			b[i]++
			return string(b)
		}
		b[i] = '0'
	}
	return "1" + string(b)
}

func allZero(s string) bool {
	for _, c := range s {
		if c != '0' && c != '.' {
			return false
		}
	}
	return true
}

var decreaseRE = regexp.MustCompile(`(?i)\b(decreas\w*|declin\w*|drop\w*|fall\w*|fell|reduc\w*|shrink\w*|loss|lost|downward|down by|lower by)\b`)

func decreaseBefore(answer string, start int) bool {
	if start < 0 || start > len(answer) {
		return false
	}
	// 80 runes, not bytes, so a phrase with multi-byte characters still fits.
	from := start
	for n := 0; n < 80 && from > 0; n++ {
		_, size := utf8.DecodeLastRuneInString(answer[:from])
		if size < 1 {
			break
		}
		from -= size
	}
	return decreaseRE.MatchString(answer[from:start])
}

type bounds struct {
	y0, m0, d0 int
	y1, m1, d1 int
	ok         bool
}

func dateBounds(facts []prompt.Fact) bounds {
	var b bounds
	have0, have1 := false, false
	b.m0, b.d0 = 1, 1
	b.m1, b.d1 = 12, 31
	for _, f := range facts {
		switch f.Name {
		case "dates.start_year":
			b.y0 = int(math.Round(f.Value))
			have0 = true
		case "dates.end_year":
			b.y1 = int(math.Round(f.Value))
			have1 = true
		case "dates.start_month":
			if f.Value > 0 {
				b.m0 = int(math.Round(f.Value))
			}
		case "dates.start_day":
			if f.Value > 0 {
				b.d0 = int(math.Round(f.Value))
			}
		case "dates.end_month":
			if f.Value > 0 {
				b.m1 = int(math.Round(f.Value))
			}
		case "dates.end_day":
			if f.Value > 0 {
				b.d1 = int(math.Round(f.Value))
			}
		}
	}
	b.ok = have0 && have1
	if b.ok && (b.y0 > b.y1 || (b.y0 == b.y1 && b.m0*100+b.d0 > b.m1*100+b.d1)) {
		b.y0, b.y1 = b.y1, b.y0
		b.m0, b.m1 = b.m1, b.m0
		b.d0, b.d1 = b.d1, b.d0
	}
	return b
}

func yearToken(n prompt.Number, b bounds) bool {
	if !b.ok || n.Percent {
		return false
	}
	for _, rd := range readings(n) {
		if rd.places > 0 {
			continue
		}
		y := math.Round(rd.val)
		if math.Abs(rd.val-y) > 1e-6 {
			continue
		}
		yi := int(y)
		if yi >= b.y0 && yi <= b.y1 {
			return true
		}
	}
	return false
}

var (
	ymdRE = regexp.MustCompile(`\d{4}[-/.]\d{1,2}[-/.]\d{1,2}`)
	ymRE  = regexp.MustCompile(`\d{4}[-/.]\d{1,2}`)
)

func whitelistSpans(answer string, b bounds) [][2]int {
	if !b.ok {
		return nil
	}
	var spans [][2]int
	consider := func(re *regexp.Regexp, ym bool) {
		for _, loc := range re.FindAllStringIndex(answer, -1) {
			if loc[0] > 0 && isDigitByte(answer[loc[0]-1]) {
				continue
			}
			if loc[1] < len(answer) && isDigitByte(answer[loc[1]]) {
				continue
			}
			raw := answer[loc[0]:loc[1]]
			if ym {
				y, m, ok := parseYM(raw)
				if !ok || !monthOverlaps(y, m, b) {
					continue
				}
			} else {
				y, m, d, ok := parseYMD(raw)
				if !ok || !dateInRange(y, m, d, b) {
					continue
				}
			}
			spans = append(spans, [2]int{loc[0], loc[1]})
		}
	}
	consider(ymdRE, false)
	consider(ymRE, true)
	return spans
}

func covered(n prompt.Number, spans [][2]int) bool {
	for _, sp := range spans {
		if n.Start >= sp[0] && n.End <= sp[1] {
			return true
		}
	}
	return false
}

func dateInRange(y, m, d int, b bounds) bool {
	cur := y*10000 + m*100 + d
	lo := b.y0*10000 + b.m0*100 + b.d0
	hi := b.y1*10000 + b.m1*100 + b.d1
	return cur >= lo && cur <= hi
}

func monthOverlaps(y, m int, b bounds) bool {
	ms := y*10000 + m*100 + 1
	me := y*10000 + m*100 + 31
	lo := b.y0*10000 + b.m0*100 + b.d0
	hi := b.y1*10000 + b.m1*100 + b.d1
	return me >= lo && ms <= hi
}

func parseYMD(s string) (int, int, int, bool) {
	parts := splitDate(s)
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	y, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	d, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || m < 1 || m > 12 || d < 1 || d > 31 {
		return 0, 0, 0, false
	}
	return y, m, d, true
}

func parseYM(s string) (int, int, bool) {
	parts := splitDate(s)
	if len(parts) != 2 {
		return 0, 0, false
	}
	y, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || m < 1 || m > 12 {
		return 0, 0, false
	}
	return y, m, true
}

func splitDate(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '-' || r == '/' || r == '.'
	})
}

func isDigitByte(b byte) bool { return b >= '0' && b <= '9' }
