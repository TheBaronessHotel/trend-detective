package csvupload

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
)

// fileError is a problem with the user's file. Its message is shown to them,
// so it says what to change.
type fileError struct{ msg string }

func (e *fileError) Error() string { return e.msg }

func fileErrorf(format string, args ...any) error {
	return &fileError{msg: fmt.Sprintf(format, args...)}
}

// table is a cleaned-up upload.
type table struct {
	// The value column's header, or "value" when the file has none.
	valueName string
	step      commonv1.TimeStep
	// Sorted by date, with no repeats.
	rows []row
}

type row struct {
	date  time.Time
	value float64
	// False when the value is missing.
	ok bool
	// The row's number in the file, counting from 1 like a spreadsheet.
	line int
}

var (
	// 2010-03-01, 2010/3/1, 2010.03.01 or 2010-03, with an optional time of day.
	isoDate = regexp.MustCompile(`^(\d{4})[-/.](\d{1,2})(?:[-/.](\d{1,2}))?(?:[T ]\S.*)?$`)
	// 03/04/2010 or 3.4.2010: day first or month first, there's no telling.
	dayMonthYear = regexp.MustCompile(`^\d{1,2}[-/.]\d{1,2}[-/.]\d{2,4}$`)
	yearOnly     = regexp.MustCompile(`^\d{4}$`)
)

// Cells that mean "no value".
var missing = map[string]bool{"": true, "na": true, "n/a": true, "nan": true, "null": true, "none": true, "-": true, "--": true, "?": true}

// Fill values that common datasets use for a missing value.
var fills = []float64{-999, -9999, -99999}

// record is one non-blank CSV row and its row number in the file.
type record struct {
	cells []string
	line  int
}

// parse reads a user's CSV: a date column and a value column, separated by
// commas, semicolons or tabs, with or without a header row.
//
// Rows before the first one that starts with a date are a preamble, like the
// metadata lines at the top of a Giovanni export, and the last of them is
// the header. A fill value the preamble declares, as "undef" or "Fill Value",
// counts as missing.
func parse(data []byte) (*table, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, fileErrorf("The file isn't plain text. Save it as CSV (UTF-8) and upload it again.")
	}
	delim, err := sniff(data)
	if err != nil {
		return nil, err
	}
	recs, err := readRecords(data, delim)
	if err != nil {
		return nil, err
	}
	start := slices.IndexFunc(recs, func(r record) bool { return dateLike(r.cells[0]) })
	if start < 0 {
		// Nothing looks like a date, so report the first row after a header.
		if len(recs) > 1 {
			_, _, err := parseDate(recs[1].cells[0], recs[1].line)
			return nil, err
		}
		return nil, fileErrorf("The file has no rows of data.")
	}

	t := &table{valueName: "value"}
	fillValues := slices.Clone(fills)
	for _, r := range recs[:start] {
		if v, ok := declaredFill(r.cells); ok {
			fillValues = append(fillValues, v)
		}
	}
	columns := recs[start].cells
	var header []string
	if start > 0 && len(recs[start-1].cells) >= 2 {
		header = recs[start-1].cells
		columns = header
	}
	if len(columns) < 2 {
		return nil, fileErrorf("Each row needs a date and a value, separated by a comma, semicolon or tab.")
	}
	valueCol, err := pickValueColumn(columns, header != nil)
	if err != nil {
		return nil, err
	}
	if header != nil {
		if name := strings.TrimSpace(header[valueCol]); name != "" {
			t.valueName = truncate(name, 60)
		}
	}

	monthsOnly := true
	for _, r := range recs[start:] {
		if len(r.cells) <= valueCol {
			return nil, fileErrorf("Row %d has no value.", r.line)
		}
		date, monthOnly, err := parseDate(r.cells[0], r.line)
		if err != nil {
			return nil, err
		}
		monthsOnly = monthsOnly && monthOnly
		value, ok, err := parseValue(r.cells[valueCol], delim, r.line, fillValues)
		if err != nil {
			return nil, err
		}
		t.rows = append(t.rows, row{date: date, value: value, ok: ok, line: r.line})
	}
	slices.SortStableFunc(t.rows, func(a, b row) int { return a.date.Compare(b.date) })

	t.step, err = detectStep(t.rows, monthsOnly)
	if err != nil {
		return nil, err
	}
	if t.step == commonv1.TimeStep_TIME_STEP_MONTHLY {
		for i := range t.rows {
			d := t.rows[i].date
			t.rows[i].date = time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
		}
	}
	for i := 1; i < len(t.rows); i++ {
		if t.rows[i].date.Equal(t.rows[i-1].date) {
			return nil, fileErrorf("Rows %d and %d are both for %s. Keep one value per %s.",
				t.rows[i-1].line, t.rows[i].line, label(t.rows[i].date, t.step), unit(t.step))
		}
	}
	return t, nil
}

// readRecords splits the file into its non-blank rows.
func readRecords(data []byte, delim rune) ([]record, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = true
	r.Comment = '#'
	var recs []record
	for {
		cells, err := r.Read()
		if errors.Is(err, io.EOF) {
			return recs, nil
		}
		if err != nil {
			return nil, fileErrorf("The file couldn't be read as CSV: %v.", err)
		}
		if blank(cells) {
			continue
		}
		line, _ := r.FieldPos(0)
		recs = append(recs, record{cells: cells, line: line})
	}
}

// dateLike reports whether a cell is meant as a date, including the forms
// parseDate refuses with an explanation.
func dateLike(cell string) bool {
	cell = strings.TrimSpace(cell)
	return isoDate.MatchString(cell) || dayMonthYear.MatchString(cell) || yearOnly.MatchString(cell)
}

// Preamble keys that declare the fill value, lowercased without a colon.
var fillKeys = map[string]bool{"undef": true, "fill value": true, "fillvalue": true, "_fillvalue": true, "missing_value": true, "missing value": true}

// declaredFill reads a preamble row like "undef,-9999" or "Fill Value:,-9999.0".
func declaredFill(cells []string) (float64, bool) {
	if len(cells) < 2 {
		return 0, false
	}
	key := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(cells[0]), ":"))
	if !fillKeys[key] {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(cells[1]), 64)
	return v, err == nil
}

// sniff picks the separator from the first line. A tab or semicolon wins
// over a comma, because a comma can also be a decimal point, as in 12,5.
func sniff(data []byte) (rune, error) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tabs, semicolons := strings.Count(line, "\t"), strings.Count(line, ";")
		switch {
		case tabs > 0 && tabs >= semicolons:
			return '\t', nil
		case semicolons > 0:
			return ';', nil
		case strings.Contains(line, ","):
			return ',', nil
		}
		return 0, fileErrorf("Each row needs a date and a value, separated by a comma, semicolon or tab.")
	}
	return 0, fileErrorf("The file is empty.")
}

// pickValueColumn returns the value column: the only one after the date, or
// the one headed "value" when there are more.
func pickValueColumn(rec []string, header bool) (int, error) {
	if len(rec) == 2 {
		return 1, nil
	}
	if header {
		for i, name := range rec[1:] {
			if strings.EqualFold(strings.TrimSpace(name), "value") {
				return i + 1, nil
			}
		}
		return 0, fileErrorf("The file has %d columns (%s). Keep two: the date first, then the value. Or name the value column \"value\".",
			len(rec), strings.Join(rec, ", "))
	}
	return 0, fileErrorf("The file has %d columns. Keep two: the date first, then the value.", len(rec))
}

// parseDate reads a date cell. monthOnly is true for YYYY-MM.
func parseDate(cell string, line int) (date time.Time, monthOnly bool, err error) {
	cell = strings.TrimSpace(cell)
	m := isoDate.FindStringSubmatch(cell)
	if m == nil {
		switch {
		case dayMonthYear.MatchString(cell):
			return time.Time{}, false, fileErrorf("Row %d: %q could be day/month or month/day. Write dates as YYYY-MM-DD, like 2010-03-01.", line, cell)
		case yearOnly.MatchString(cell):
			return time.Time{}, false, fileErrorf("Row %d: %q is a whole year. Yearly values aren't supported yet, so use monthly or daily ones.", line, cell)
		}
		return time.Time{}, false, fileErrorf("Row %d: %q isn't a date. Write dates as YYYY-MM-DD or YYYY-MM, like 2010-03-01 or 2010-03.", line, cell)
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day := 1
	if m[3] != "" {
		day, _ = strconv.Atoi(m[3])
	}
	d := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if month < 1 || month > 12 || d.Day() != day || year < 1800 || year > 2100 {
		return time.Time{}, false, fileErrorf("Row %d: %q isn't a real date.", line, cell)
	}
	return d, m[3] == "", nil
}

// parseValue reads a value cell. ok is false for a missing value, including
// any of fillValues.
func parseValue(cell string, delim rune, line int, fillValues []float64) (value float64, ok bool, err error) {
	cell = strings.TrimSpace(cell)
	if missing[strings.ToLower(cell)] {
		return 0, false, nil
	}
	// With semicolons or tabs between columns, 12,5 is a decimal comma.
	if delim != ',' && strings.Count(cell, ",") == 1 && !strings.Contains(cell, ".") {
		cell = strings.Replace(cell, ",", ".", 1)
	}
	v, err := strconv.ParseFloat(cell, 64)
	if err != nil || math.IsInf(v, 0) {
		return 0, false, fileErrorf("Row %d: %q isn't a number.", line, cell)
	}
	if math.IsNaN(v) || slices.Contains(fillValues, v) {
		return 0, false, nil
	}
	return v, true, nil
}

// detectStep tells daily, weekly and monthly data apart by the usual gap
// between dates.
func detectStep(rows []row, monthsOnly bool) (commonv1.TimeStep, error) {
	if monthsOnly {
		return commonv1.TimeStep_TIME_STEP_MONTHLY, nil
	}
	if len(rows) < 2 {
		return commonv1.TimeStep_TIME_STEP_UNSPECIFIED, fileErrorf("The file has only one row of data.")
	}
	gaps := make([]float64, 0, len(rows)-1)
	for i := 1; i < len(rows); i++ {
		gaps = append(gaps, rows[i].date.Sub(rows[i-1].date).Hours()/24)
	}
	slices.Sort(gaps)
	median := gaps[len(gaps)/2]
	switch {
	case median == 0:
		return commonv1.TimeStep_TIME_STEP_UNSPECIFIED, fileErrorf("Most rows repeat the date before them. Keep one value per day, week or month.")
	case median <= 1:
		return commonv1.TimeStep_TIME_STEP_DAILY, nil
	case median >= 6 && median <= 8:
		return commonv1.TimeStep_TIME_STEP_WEEKLY, nil
	case median >= 28 && median <= 31:
		return commonv1.TimeStep_TIME_STEP_MONTHLY, nil
	}
	return commonv1.TimeStep_TIME_STEP_UNSPECIFIED, fileErrorf(
		"The dates are usually %.0f days apart, so it isn't clear whether the data is daily, weekly or monthly. Use one of those.", median)
}

// write renders the cleaned-up CSV.
func (t *table) write() []byte {
	var b bytes.Buffer
	b.WriteString("date,value\n")
	for _, r := range t.rows {
		b.WriteString(r.date.Format("2006-01-02"))
		b.WriteByte(',')
		if r.ok {
			b.WriteString(strconv.FormatFloat(r.value, 'g', -1, 64))
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func blank(rec []string) bool {
	for _, c := range rec {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func label(d time.Time, step commonv1.TimeStep) string {
	if step == commonv1.TimeStep_TIME_STEP_MONTHLY {
		return d.Format("January 2006")
	}
	return d.Format("2006-01-02")
}

func unit(step commonv1.TimeStep) string {
	switch step {
	case commonv1.TimeStep_TIME_STEP_MONTHLY:
		return "month"
	case commonv1.TimeStep_TIME_STEP_WEEKLY:
		return "week"
	}
	return "day"
}

// truncate keeps the first n characters of s.
func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
