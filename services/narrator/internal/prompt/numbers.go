package prompt

import (
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Number is one numeric token copied from free text.
// Raw is the token as written, including its sign and a trailing percent.
// Value is the signed magnitude. When the token is a comma group that can
// also be read as thousands (1,234), HasAlt is set and Alt is that reading.
type Number struct {
	Raw       string
	Value     float64
	Places    int // digits after the decimal mark in the mantissa
	Alt       float64
	AltPlaces int
	HasAlt    bool
	Exp       int
	Sci       bool
	Percent   bool
	Signed    bool
	Neg       bool
	PlainInt  bool // optional sign and digits only
	Start     int
	End       int
}

type chunk struct {
	sep    byte // 0 for the leading digit group
	digits string
}

// ScanNumbers extracts numbers from free text, left to right.
// It accepts integers, decimals, comma decimals, signs, percents ("5%",
// "5 percent", "5 per cent"), scientific notation (1.2e-3, 1.2×10^-3 and
// 1.2×10⁻³), and ranges such as 0.1–0.3 (both ends).
// A hyphen, an en dash or a unicode minus is a minus sign when a digit does
// not precede it, so "–0.21" and "−0.21" are negative. Between digits those
// marks stay a range.
// A token that follows a letter is part of a name, not a number: "MERRA-2",
// "PM2.5", "CO2" and "NO2" are skipped. Letters after a token are a unit and
// the number is kept ("5km", "0.5mm"), except an ordinal or decade suffix
// ("20th", "1990s"). "GPM" has no digit, so it contributes nothing.
func ScanNumbers(s string) []Number {
	var out []Number
	for i := 0; i < len(s); {
		if n, next, ok := matchAt(s, i); ok && next > i {
			if !gluedToLetters(s, n) {
				out = append(out, n)
			}
			i = next
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size < 1 {
			size = 1
		}
		i += size
	}
	return out
}

// gluedToLetters reports whether the token is part of a name: a letter right
// before it, including before a sign dash as in MERRA-2, or an ordinal or
// decade suffix right after it, as in 20th or 1990s. Other letters after a
// number are a unit, so "5km" still counts as 5.
func gluedToLetters(s string, n Number) bool {
	if prev, ok := prevRune(s, n.Start); ok && unicode.IsLetter(prev) {
		return true
	}
	j := n.End
	for j < len(s) {
		r, size := utf8.DecodeRuneInString(s[j:])
		if !unicode.IsLetter(r) {
			break
		}
		j += size
	}
	switch strings.ToLower(s[n.End:j]) {
	case "st", "nd", "rd", "th", "s":
		return true
	}
	return false
}

func matchAt(s string, i int) (Number, int, bool) {
	start := i
	neg, signed, body, hasSign := signAt(s, i)
	if hasSign {
		i = body
	}
	chunks, _, ok := readChunks(s, i)
	if !ok {
		return Number{}, 0, false
	}
	chunks = trimValid(chunks)
	if len(chunks) == 0 {
		return Number{}, 0, false
	}
	end := i + chunksLen(chunks)
	val, places, alt, altPlaces, hasAlt, plain, ok := interpret(chunks)
	if !ok {
		return Number{}, 0, false
	}
	exp := 0
	sci := false
	if e, eend, eok := readExp(s, end); eok {
		exp = e
		sci = true
		end = eend
		val *= math.Pow10(exp)
		if hasAlt {
			alt *= math.Pow10(exp)
		}
	}
	percent := false
	if pend, pok := readPercent(s, end); pok {
		percent = true
		end = pend
	}
	if neg {
		val = -val
		if hasAlt {
			alt = -alt
		}
	}
	if end <= start {
		return Number{}, 0, false
	}
	return Number{
		Raw:       s[start:end],
		Value:     val,
		Places:    places,
		Alt:       alt,
		AltPlaces: altPlaces,
		HasAlt:    hasAlt,
		Exp:       exp,
		Sci:       sci,
		Percent:   percent,
		Signed:    signed,
		Neg:       neg,
		PlainInt:  plain && !sci && !percent,
		Start:     start,
		End:       end,
	}, end, true
}

func signAt(s string, i int) (neg bool, signed bool, next int, ok bool) {
	r, size := utf8.DecodeRuneInString(s[i:])
	if !isSignRune(r) {
		return false, false, i, false
	}
	// A dash after a digit is a range: "0.1-0.3", "0.1–0.3" and "0.1−0.3".
	// The same mark after anything else is a sign, so "–0.21" is negative.
	if r != '+' {
		if prev, has := prevRune(s, i); has && prev >= '0' && prev <= '9' {
			return false, false, i, false
		}
	}
	if i+size >= len(s) {
		return false, false, i, false
	}
	nr, _ := utf8.DecodeRuneInString(s[i+size:])
	if !isNumStart(nr) {
		return false, false, i, false
	}
	return r != '+', true, i + size, true
}

func isSignRune(r rune) bool {
	return r == '+' || r == '-' || r == '\u2212' || r == '\u2013'
}

func isNumStart(r rune) bool {
	return (r >= '0' && r <= '9') || r == '.' || r == ','
}

func prevRune(s string, i int) (rune, bool) {
	if i <= 0 || i > len(s) {
		return 0, false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	if r == utf8.RuneError {
		return 0, false
	}
	return r, true
}

func readChunks(s string, i int) ([]chunk, int, bool) {
	if i >= len(s) {
		return nil, i, false
	}
	var chunks []chunk
	j := i
	if s[j] == ',' {
		// A number never starts with a comma: in "0.1,0.3" the comma
		// separates two numbers.
		return nil, i, false
	}
	if s[j] == '.' {
		sep := s[j]
		j++
		ds, nj, ok := readDigits(s, j)
		if !ok {
			return nil, i, false
		}
		chunks = append(chunks, chunk{sep: sep, digits: ds})
		j = nj
	} else if isDigitByte(s[j]) {
		ds, nj, _ := readDigits(s, j)
		chunks = append(chunks, chunk{digits: ds})
		j = nj
	} else {
		return nil, i, false
	}
	for j < len(s) && (s[j] == '.' || s[j] == ',') {
		sep := s[j]
		ds, nj, ok := readDigits(s, j+1)
		if !ok {
			break
		}
		chunks = append(chunks, chunk{sep: sep, digits: ds})
		j = nj
	}
	return chunks, j, len(chunks) > 0
}

func readDigits(s string, i int) (string, int, bool) {
	j := i
	for j < len(s) && isDigitByte(s[j]) {
		j++
	}
	if j == i {
		return "", i, false
	}
	return s[i:j], j, true
}

func isDigitByte(b byte) bool { return b >= '0' && b <= '9' }

func chunksLen(chunks []chunk) int {
	n := 0
	for _, c := range chunks {
		if c.sep != 0 {
			n++
		}
		n += len(c.digits)
	}
	return n
}

func trimValid(in []chunk) []chunk {
	chunks := in
	for len(chunks) > 0 {
		if _, _, _, _, _, _, ok := interpret(chunks); ok {
			return chunks
		}
		chunks = chunks[:len(chunks)-1]
	}
	return nil
}

func interpret(chunks []chunk) (val float64, places int, alt float64, altPlaces int, hasAlt bool, plain bool, ok bool) {
	if len(chunks) == 0 {
		return 0, 0, 0, 0, false, false, false
	}
	if len(chunks) == 1 && chunks[0].sep == 0 {
		v, err := strconv.ParseFloat(chunks[0].digits, 64)
		if err != nil {
			return 0, 0, 0, 0, false, false, false
		}
		return v, 0, 0, 0, false, true, true
	}
	if len(chunks) == 1 && chunks[0].sep != 0 {
		v, err := strconv.ParseFloat("0."+chunks[0].digits, 64)
		if err != nil {
			return 0, 0, 0, 0, false, false, false
		}
		return v, len(chunks[0].digits), 0, 0, false, false, true
	}
	if len(chunks) == 2 && chunks[0].sep == 0 {
		places = len(chunks[1].digits)
		v, err := strconv.ParseFloat(chunks[0].digits+"."+chunks[1].digits, 64)
		if err != nil {
			return 0, 0, 0, 0, false, false, false
		}
		// "1,234" is a comma decimal and a thousands integer.
		if chunks[1].sep == ',' && places == 3 {
			a, err2 := strconv.ParseFloat(chunks[0].digits+chunks[1].digits, 64)
			if err2 == nil {
				return v, places, a, 0, true, false, true
			}
		}
		return v, places, 0, 0, false, false, true
	}
	if chunks[0].sep != 0 {
		return 0, 0, 0, 0, false, false, false
	}
	last := len(chunks) - 1
	if chunks[last].sep != chunks[1].sep {
		for i := 1; i < last; i++ {
			if chunks[i].sep != chunks[1].sep || len(chunks[i].digits) != 3 {
				return 0, 0, 0, 0, false, false, false
			}
		}
		var b strings.Builder
		b.WriteString(chunks[0].digits)
		for i := 1; i < last; i++ {
			b.WriteString(chunks[i].digits)
		}
		b.WriteByte('.')
		b.WriteString(chunks[last].digits)
		v, err := strconv.ParseFloat(b.String(), 64)
		if err != nil {
			return 0, 0, 0, 0, false, false, false
		}
		return v, len(chunks[last].digits), 0, 0, false, false, true
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i].sep != chunks[1].sep || len(chunks[i].digits) != 3 {
			return 0, 0, 0, 0, false, false, false
		}
	}
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c.digits)
	}
	v, err := strconv.ParseFloat(b.String(), 64)
	if err != nil {
		return 0, 0, 0, 0, false, false, false
	}
	return v, 0, 0, 0, false, false, true
}

func readExp(s string, i int) (exp int, end int, ok bool) {
	if i >= len(s) {
		return 0, i, false
	}
	if s[i] == 'e' || s[i] == 'E' {
		return readE(s, i)
	}
	return readTimes10(s, i)
}

func readE(s string, i int) (int, int, bool) {
	j := i + 1
	sign := 1
	if j < len(s) && (s[j] == '+' || s[j] == '-') {
		if s[j] == '-' {
			sign = -1
		}
		j++
	} else if j < len(s) {
		r, size := utf8.DecodeRuneInString(s[j:])
		if r == '\u2212' {
			sign = -1
			j += size
		}
	}
	ds, nj, ok := readDigits(s, j)
	if !ok {
		return 0, i, false
	}
	n, err := strconv.Atoi(ds)
	if err != nil {
		return 0, i, false
	}
	return sign * n, nj, true
}

func readTimes10(s string, i int) (int, int, bool) {
	j := skipSpace(s, i)
	if j >= len(s) {
		return 0, i, false
	}
	r, size := utf8.DecodeRuneInString(s[j:])
	if r != 'x' && r != 'X' && r != '*' && r != '\u00d7' {
		return 0, i, false
	}
	j += size
	j = skipSpace(s, j)
	if j+1 >= len(s) || s[j] != '1' || s[j+1] != '0' {
		return 0, i, false
	}
	j += 2
	if e, end, ok := readSuperscript(s, j); ok {
		return e, end, true
	}
	j = skipSpace(s, j)
	if j >= len(s) || s[j] != '^' {
		return 0, i, false
	}
	j++
	j = skipSpace(s, j)
	sign := 1
	if j < len(s) && (s[j] == '+' || s[j] == '-') {
		if s[j] == '-' {
			sign = -1
		}
		j++
	} else if j < len(s) {
		rr, sz := utf8.DecodeRuneInString(s[j:])
		if rr == '\u2212' {
			sign = -1
			j += sz
		}
	}
	ds, nj, ok := readDigits(s, j)
	if !ok {
		return 0, i, false
	}
	n, err := strconv.Atoi(ds)
	if err != nil {
		return 0, i, false
	}
	return sign * n, nj, true
}

// superscriptDigit maps ⁰¹²³⁴⁵⁶⁷⁸⁹ to 0-9.
func superscriptDigit(r rune) (int, bool) {
	switch r {
	case '\u2070':
		return 0, true
	case '\u00b9':
		return 1, true
	case '\u00b2':
		return 2, true
	case '\u00b3':
		return 3, true
	}
	if r >= '\u2074' && r <= '\u2079' {
		return int(r-'\u2074') + 4, true
	}
	return 0, false
}

// readSuperscript reads an exponent written in superscript, like the ⁻³ in
// 1.2×10⁻³.
func readSuperscript(s string, i int) (int, int, bool) {
	j := i
	sign := 1
	if j < len(s) {
		r, size := utf8.DecodeRuneInString(s[j:])
		switch r {
		case '\u207b':
			sign = -1
			j += size
		case '\u207a':
			j += size
		}
	}
	n, digits := 0, 0
	for j < len(s) && digits < 4 {
		r, size := utf8.DecodeRuneInString(s[j:])
		d, ok := superscriptDigit(r)
		if !ok {
			break
		}
		n = n*10 + d
		digits++
		j += size
	}
	if digits == 0 {
		return 0, i, false
	}
	return sign * n, j, true
}

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// readPercent reads "%", " %", " percent", " per cent" or " pct" after a
// number. The word must end there, so "percentage points" is not a percent.
func readPercent(s string, i int) (int, bool) {
	j := i
	if j < len(s) && s[j] == ' ' {
		j++
	}
	if j < len(s) && s[j] == '%' {
		return j + 1, true
	}
	if j == i {
		return i, false
	}
	for _, word := range []string{"percent", "per cent", "pct"} {
		end := j + len(word)
		if end > len(s) || !strings.EqualFold(s[j:end], word) {
			continue
		}
		if end < len(s) {
			if r, _ := utf8.DecodeRuneInString(s[end:]); unicode.IsLetter(r) {
				continue
			}
		}
		return end, true
	}
	return i, false
}
