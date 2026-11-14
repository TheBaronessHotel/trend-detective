package csvupload

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/blob"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
)

// fakeUploads is an uploads bucket holding one file.
type fakeUploads struct {
	data []byte
	etag string
	// Reported by Stat. Zero means len(data).
	size int64
}

func (f *fakeUploads) Stat(_ context.Context, key string) (blob.Info, error) {
	if f.data == nil {
		return blob.Info{}, blob.ErrNotFound
	}
	size := f.size
	if size == 0 {
		size = int64(len(f.data))
	}
	return blob.Info{Size: size, ETag: f.etag}, nil
}

func (f *fakeUploads) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if f.data == nil {
		return nil, blob.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(f.data)), nil
}

type sink struct {
	files []core.File
	data  []string
}

func (s *sink) Put(_ context.Context, f core.File) error {
	b, err := io.ReadAll(f.Body)
	if err != nil {
		return err
	}
	s.files = append(s.files, f)
	s.data = append(s.data, string(b))
	return nil
}

func task(start, end *commonv1.Date) *ingestv1.IngestTask {
	return &ingestv1.IngestTask{
		VariableId: "user-csv",
		Dates:      &commonv1.DateRange{Start: start, End: end},
		Adapter: &ingestv1.IngestTask_CsvUpload{CsvUpload: &ingestv1.CsvUpload{
			Upload: &commonv1.BlobRef{Bucket: "uploads", Key: "u-1/rain.csv"},
		}},
	}
}

func wide() *ingestv1.IngestTask {
	return task(&commonv1.Date{Year: 1850, Month: 1, Day: 1}, &commonv1.Date{Year: 2100, Month: 12, Day: 31})
}

// months returns rows "2001-01<sep>1" through n months.
func months(n int, sep string) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "%04d-%02d%s%d\n", 2001+i/12, i%12+1, sep, i+1)
	}
	return b.String()
}

func fetch(t *testing.T, csv string, tk *ingestv1.IngestTask) (*core.Fetched, *sink, error) {
	t.Helper()
	s := &sink{}
	got, err := New(&fakeUploads{data: []byte(csv), etag: "e1"}, 1<<20).Fetch(context.Background(), tk, s)
	return got, s, err
}

func TestMonthlyWithHeader(t *testing.T) {
	got, s, err := fetch(t, "date,rain (mm)\n"+months(24, ","), wide())
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != ingestv1.Format_FORMAT_USER_CSV || got.Step != commonv1.TimeStep_TIME_STEP_MONTHLY {
		t.Errorf("format %v, step %v", got.Format, got.Step)
	}
	if len(got.Variables) != 1 || got.Variables[0] != "rain (mm)" {
		t.Errorf("variables %v", got.Variables)
	}
	if len(s.files) != 1 || s.files[0].Name != "user.csv" {
		t.Fatalf("files %+v", s.files)
	}
	lines := strings.Split(strings.TrimSpace(s.data[0]), "\n")
	if lines[0] != "date,value" || lines[1] != "2001-01-01,1" || lines[24] != "2002-12-01,24" || len(lines) != 25 {
		t.Errorf("output starts %q, ends %q, %d lines", lines[:2], lines[len(lines)-1], len(lines))
	}
	if c := s.files[0].Coverage; c.GetStart().GetYear() != 2001 || c.GetEnd().GetYear() != 2002 || c.GetEnd().GetMonth() != 12 || c.GetEnd().GetDay() != 31 {
		t.Errorf("coverage %v", c)
	}
}

func TestDailyEuropeanStyle(t *testing.T) {
	// No header, semicolons, decimal commas, gaps marked NA and -9999, out of
	// order, and a byte order mark from Excel.
	var b strings.Builder
	b.WriteString("\xef\xbb\xbf")
	for i := 800; i >= 0; i-- {
		d := fmt.Sprintf("%s;%d,5", dateAfter(i), i)
		switch i {
		case 10:
			d = dateAfter(i) + ";NA"
		case 11:
			d = dateAfter(i) + ";-9999"
		}
		b.WriteString(d + "\n")
	}
	got, s, err := fetch(t, b.String(), wide())
	if err != nil {
		t.Fatal(err)
	}
	if got.Step != commonv1.TimeStep_TIME_STEP_DAILY || got.Variables[0] != "value" {
		t.Errorf("step %v, variables %v", got.Step, got.Variables)
	}
	lines := strings.Split(strings.TrimSpace(s.data[0]), "\n")
	if lines[1] != "2010-01-01,0.5" || lines[11] != dateAfter(10)+"," || lines[12] != dateAfter(11)+"," || len(lines) != 802 {
		t.Errorf("lines %q ... %q %q, %d lines", lines[1], lines[11], lines[12], len(lines))
	}
}

// dateAfter returns the date i days after 2010-01-01.
func dateAfter(i int) string {
	d := toTime(&commonv1.Date{Year: 2010, Month: 1, Day: 1}).AddDate(0, 0, i)
	return d.Format("2006-01-02")
}

func TestPicksTheValueColumn(t *testing.T) {
	var b strings.Builder
	b.WriteString("date\tstation\tvalue\n")
	for i := range 24 {
		fmt.Fprintf(&b, "%04d-%02d-15\tLisbon\t%d\n", 2001+i/12, i%12+1, i)
	}
	got, s, err := fetch(t, b.String(), wide())
	if err != nil {
		t.Fatal(err)
	}
	// Mid-month dates are monthly data, stored as the first of the month.
	if got.Step != commonv1.TimeStep_TIME_STEP_MONTHLY || !strings.Contains(s.data[0], "\n2001-01-01,0\n") {
		t.Errorf("step %v, output %q", got.Step, s.data[0][:40])
	}
}

func TestGiovanniExports(t *testing.T) {
	// The preambles are copied from real files. The area export leaves
	// missing days empty, and the point export writes its undef value.
	area := "User Start Date:,2005-01-01T00:00:00.000Z\nUser End Date:,2006-12-31T23:59:59.000Z\n" +
		"User Bounding Box:,\"74, 29, 76, 31\"\nFill Value:,-9999.0\n,\n" +
		"time,mean_GLDAS_CLSM025_DA1_D_2_2_GWS_tavg\n"
	point := "prod_name,OMNO2d.003\nunit,molecules/cm^2\nundef,-1.26765e+30\nlat,30.875\nlon,117.125\n\n" +
		"Timestamp (UTC),Data\n"
	for name, tt := range map[string]struct{ head, sep, missing, column string }{
		"area":  {area, "T", "", "mean_GLDAS_CLSM025_DA1_D_2_2_GWS_tavg"},
		"point": {point, " ", "-1.26765e+30", "Data"},
	} {
		t.Run(name, func(t *testing.T) {
			var b strings.Builder
			b.WriteString(tt.head)
			for i := range 800 {
				value := fmt.Sprint(500 + i)
				if i == 3 {
					value = tt.missing
				}
				fmt.Fprintf(&b, "%s%s00:00:00,%s\n", dateAfter(i), tt.sep, value)
			}
			got, s, err := fetch(t, b.String(), wide())
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(s.data[0]), "\n")
			if got.Step != commonv1.TimeStep_TIME_STEP_DAILY || got.Variables[0] != tt.column || len(lines) != 801 {
				t.Errorf("step %v, variables %v, %d lines", got.Step, got.Variables, len(lines))
			}
			if lines[1] != "2010-01-01,500" || lines[4] != "2010-01-04," {
				t.Errorf("rows %q, %q", lines[1], lines[4])
			}
		})
	}
}

func TestTrimsToTheJobsDates(t *testing.T) {
	tk := task(&commonv1.Date{Year: 2002, Month: 1, Day: 1}, &commonv1.Date{Year: 2003, Month: 12, Day: 31})
	_, s, err := fetch(t, months(60, ","), tk)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(s.data[0]), "\n")
	if lines[1] != "2002-01-01,13" || lines[len(lines)-1] != "2003-12-01,36" {
		t.Errorf("kept %q to %q", lines[1], lines[len(lines)-1])
	}

	outside := task(&commonv1.Date{Year: 1990, Month: 1, Day: 1}, &commonv1.Date{Year: 1995, Month: 12, Day: 31})
	_, _, err = fetch(t, months(60, ","), outside)
	if e := core.AsError(err); e.Code != gatewayv1.ErrorCode_ERROR_CODE_NO_DATA || !strings.Contains(e.UserMessage, "outside the dates") {
		t.Errorf("got %v %q", e.Code, e.UserMessage)
	}
}

func TestFileErrors(t *testing.T) {
	tests := []struct {
		name, csv, want string
	}{
		{"not a number", "date,value\n2001-01,1\n2001-02,abc\n", `Row 3: "abc" isn't a number.`},
		{"ambiguous date", "date,value\n03/04/2010,1\n", `could be day/month or month/day`},
		{"years", "year,value\n2010,1\n2011,2\n", `is a whole year`},
		{"not a date", "date,value\nsoon,1\n", `isn't a date`},
		{"impossible date", "date,value\n2010-02-30,1\n", `isn't a real date`},
		{"repeated month", "date,value\n2001-01,1\n2001-02,2\n2001-02,3\n", `Rows 3 and 4 are both for February 2001`},
		{"too short", months(12, ","), `at least two years`},
		{"mostly empty", "date,value\n" + months(4, ",") + emptyMonths(30), `Only 4 rows have a value`},
		{"too many columns", "date,a,b\n2001-01,1,2\n", `Keep two`},
		{"one column", "2001-01\n2001-02\n", `separated by a comma, semicolon or tab`},
		{"uneven dates", every90Days(), `usually 90 days apart`},
		{"not text", "d\x00a\x00t\x00e\x00", `isn't plain text`},
		{"empty", "\n\n", `The file is empty.`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, s, err := fetch(t, tt.csv, wide())
			e := core.AsError(err)
			if e.Code != gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST || e.Transient || !strings.Contains(e.UserMessage, tt.want) {
				t.Errorf("got %v transient=%v %q, want %q", e.Code, e.Transient, e.UserMessage, tt.want)
			}
			if len(s.files) != 0 {
				t.Error("nothing should be stored")
			}
		})
	}
}

// emptyMonths returns 4 months with values, then n-4 without.
func emptyMonths(n int) string {
	var b strings.Builder
	for i := 4; i < n; i++ {
		fmt.Fprintf(&b, "%04d-%02d,\n", 2001+i/12, i%12+1)
	}
	return b.String()
}

func every90Days() string {
	var b strings.Builder
	for i := range 12 {
		fmt.Fprintf(&b, "%s,%d\n", dateAfter(90*i), i)
	}
	return b.String()
}

func TestUploadProblems(t *testing.T) {
	ctx := context.Background()

	big := New(&fakeUploads{data: []byte(months(24, ",")), size: 5 << 20}, 1<<20)
	_, err := big.Fetch(ctx, wide(), &sink{})
	if e := core.AsError(err); e.Code != gatewayv1.ErrorCode_ERROR_CODE_TOO_LARGE || !strings.Contains(e.UserMessage, "1 MB") {
		t.Errorf("over the limit: %v %q", e.Code, e.UserMessage)
	}

	// Stat said it was small, but the file grew before it was read.
	grew := New(&fakeUploads{data: bytes.Repeat([]byte("x"), 2<<20), size: 10}, 1<<20)
	_, err = grew.Fetch(ctx, wide(), &sink{})
	if e := core.AsError(err); e.Code != gatewayv1.ErrorCode_ERROR_CODE_TOO_LARGE {
		t.Errorf("grew past the limit: %v", e.Code)
	}

	gone := New(&fakeUploads{}, 1<<20)
	_, err = gone.Fetch(ctx, wide(), &sink{})
	if e := core.AsError(err); e.Code != gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST || !strings.Contains(e.UserMessage, "wasn't found") {
		t.Errorf("missing upload: %v %q", e.Code, e.UserMessage)
	}

	elsewhere := wide()
	elsewhere.GetCsvUpload().Upload.Bucket = "raw"
	_, err = New(&fakeUploads{data: []byte(months(24, ","))}, 1<<20).Fetch(ctx, elsewhere, &sink{})
	if e := core.AsError(err); e.Code != gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST {
		t.Errorf("another bucket: %v", e.Code)
	}
}

func TestFingerprintFollowsTheFile(t *testing.T) {
	f := &fakeUploads{data: []byte(months(24, ",")), etag: "e1"}
	a := New(f, 1<<20)
	before, err := a.Fingerprint(context.Background(), wide())
	if err != nil {
		t.Fatal(err)
	}
	f.etag = "e2"
	after, _ := a.Fingerprint(context.Background(), wide())
	if before == after {
		t.Error("replacing the file under the same key should change the fingerprint")
	}
}
