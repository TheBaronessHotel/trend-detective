package giovanni

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/earthdata"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// A trimmed copy of a real answer, for GLDAS soil moisture near Ludhiana.
const soilCSV = `prod_name,GLDAS_NOAH025_M.2.1
doi,10.5067/SXAVCZFAQLNO
param_short_name,SoilMoi0_10cm_inst
unit,kg m-2
undef,-9999
begin_time,2010-01-01 00:00:00
end_time,2010-03-01 00:00:00
lat,30.625
lon,75.625
lat_resolution,0.25
lon_resolution,0.25
mean,9.17e+00

Timestamp (UTC),Data
2010-01-01 00:00,9.32422
2010-02-01 00:00,-9999
2010-03-01 00:00,7.34181
`

// The same variable asked about a point in the Atlantic.
const oceanCSV = `prod_name,GLDAS_NOAH025_M.2.1
undef,-9999
lat,0.125
lon,-29.875

Timestamp (UTC),Data
2010-01-01 00:00,-9999
2010-02-01 00:00,-9999
`

type sink struct {
	mu    sync.Mutex
	files []core.File
	data  []string
}

func (s *sink) Put(_ context.Context, f core.File) error {
	b, err := io.ReadAll(f.Body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files = append(s.files, f)
	s.data = append(s.data, string(b))
	return nil
}

type fakeGiovanni struct {
	status int
	body   string
	query  url.Values
	auth   string
}

func newAdapter(t *testing.T, f *fakeGiovanni, team string) (*Adapter, *miniredis.Miniredis) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/timeseries" {
			http.NotFound(w, r)
			return
		}
		f.query, f.auth = r.URL.Query(), r.Header.Get("Authorization")
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(srv.Close)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return New(srv.URL+"/", earthdata.NewTokens(rdb, team)), mr
}

func task() *ingestv1.IngestTask {
	return &ingestv1.IngestTask{
		VariableId: "soil-moisture-gldas",
		Dates: &commonv1.DateRange{
			Start: &commonv1.Date{Year: 2010, Month: 1, Day: 1},
			End:   &commonv1.Date{Year: 2010, Month: 3, Day: 31},
		},
		Adapter: &ingestv1.IngestTask_GiovanniPoint{GiovanniPoint: &ingestv1.GiovanniPoint{
			DataFieldId: "GLDAS_NOAH025_M_2_1_SoilMoi0_10cm_inst",
			Point:       &commonv1.LatLon{Lat: 30.5, Lon: 75.5},
		}},
	}
}

func wantCode(t *testing.T, err error, code gatewayv1.ErrorCode, transient bool) *core.Error {
	t.Helper()
	e := core.AsError(err)
	if e.Code != code || e.Transient != transient {
		t.Fatalf("want %v transient=%v, got %v transient=%v: %v", code, transient, e.Code, e.Transient, err)
	}
	return e
}

func TestFetchStoresTheCSV(t *testing.T) {
	f := &fakeGiovanni{status: http.StatusOK, body: soilCSV}
	a, _ := newAdapter(t, f, "team-token")
	s := &sink{}
	got, err := a.Fetch(context.Background(), task(), s)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"data":     "GLDAS_NOAH025_M_2_1_SoilMoi0_10cm_inst",
		"location": "[30.5,75.5]",
		"time":     "2010-01-01T00:00:00/2010-03-31T23:59:59",
	} {
		if f.query.Get(key) != want {
			t.Errorf("query %s = %q, want %q", key, f.query.Get(key), want)
		}
	}
	if f.auth != "Bearer team-token" {
		t.Errorf("auth header %q", f.auth)
	}
	if len(s.files) != 1 || s.files[0].Name != "point.csv" || s.files[0].ContentType != "text/csv" || s.data[0] != soilCSV {
		t.Fatalf("files: %+v", s.files)
	}
	if got.Source != ingestv1.Source_SOURCE_GIOVANNI_POINT || got.Format != ingestv1.Format_FORMAT_GIOVANNI_POINT_CSV {
		t.Errorf("source %v, format %v", got.Source, got.Format)
	}
	// The grid cell Giovanni used, not the point that was asked for.
	if got.Point.GetLat() != 30.625 || got.Point.GetLon() != 75.625 {
		t.Errorf("point %v, want the cell center 30.625, 75.625", got.Point)
	}
	if len(got.Variables) != 1 || got.Variables[0] != "GLDAS_NOAH025_M_2_1_SoilMoi0_10cm_inst" {
		t.Errorf("variables %v", got.Variables)
	}
}

func TestUserTokenWins(t *testing.T) {
	f := &fakeGiovanni{status: http.StatusOK, body: soilCSV}
	a, mr := newAdapter(t, f, "team-token")
	_ = mr.Set("td:job:{j}:earthdata", "user-token")
	tk := task()
	tk.EarthdataTokenKey = "td:job:{j}:earthdata"
	if _, err := a.Fetch(context.Background(), tk, &sink{}); err != nil {
		t.Fatal(err)
	}
	if f.auth != "Bearer user-token" {
		t.Errorf("auth header %q", f.auth)
	}
}

func TestNoTokenAtAll(t *testing.T) {
	a, _ := newAdapter(t, &fakeGiovanni{status: http.StatusOK, body: soilCSV}, "")
	_, err := a.Fetch(context.Background(), task(), &sink{})
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false)
}

func TestOnlyMissingValuesIsNoData(t *testing.T) {
	a, _ := newAdapter(t, &fakeGiovanni{status: http.StatusOK, body: oceanCSV}, "team-token")
	s := &sink{}
	_, err := a.Fetch(context.Background(), task(), s)
	e := wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, false)
	if !strings.Contains(e.UserMessage, "ocean") {
		t.Errorf("message %q should say why", e.UserMessage)
	}
	if len(s.files) != 0 {
		t.Error("nothing should be stored")
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		code      gatewayv1.ErrorCode
		transient bool
	}{
		// Giovanni's real answers wrap the code in a 403.
		{"bad token", 403, `{"code": "401", "message": "Bad or missing token", "context": "Unable to find token in header or in cookie"}`,
			gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false},
		{"unknown variable", 403, `{"code": "400", "message": "Bad or missing input", "context": "Data parameter is invalid"}`,
			gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false},
		{"out of range", 404, `Data, GLDAS_NOAH025_M_2_1_SoilMoi0_10cm_inst, was not found for the requested time range 1990-01-01T00:00:00/1990-12-31T23:59:59.`,
			gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, false},
		{"rate limited", 429, ``, gatewayv1.ErrorCode_ERROR_CODE_QUOTA_EXCEEDED, true},
		{"quota in the message", 403, `{"code": "403", "message": "Monthly quota exceeded"}`,
			gatewayv1.ErrorCode_ERROR_CODE_QUOTA_EXCEEDED, true},
		{"server error", 503, ``, gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true},
		{"other refusal", 403, `<html>Forbidden</html>`, gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, false},
		{"not a point CSV", 200, `<html>maintenance</html>`, gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _ := newAdapter(t, &fakeGiovanni{status: tt.status, body: tt.body}, "team-token")
			_, err := a.Fetch(context.Background(), task(), &sink{})
			wantCode(t, err, tt.code, tt.transient)
		})
	}
}

func TestIncompleteTaskIsRejected(t *testing.T) {
	a, _ := newAdapter(t, &fakeGiovanni{status: http.StatusOK, body: soilCSV}, "team-token")
	tk := task()
	tk.GetGiovanniPoint().Point = nil
	_, err := a.Fetch(context.Background(), tk, &sink{})
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false)
}
