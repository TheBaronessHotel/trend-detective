package power

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
)

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

func task() *ingestv1.IngestTask {
	return &ingestv1.IngestTask{
		VariableId: "air-temperature-power",
		Dates: &commonv1.DateRange{
			Start: &commonv1.Date{Year: 2001, Month: 3, Day: 1},
			End:   &commonv1.Date{Year: 2003, Month: 6, Day: 30},
		},
		Adapter: &ingestv1.IngestTask_Power{Power: &ingestv1.Power{
			Parameters: []string{"T2M", "PRECTOTCORR"},
			Community:  "AG",
			Point:      &commonv1.LatLon{Lat: 38.75, Lon: -9.25},
		}},
	}
}

func fakePower(t *testing.T, status int, body string, gotQuery *url.Values) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/temporal/monthly/point" {
			http.NotFound(w, r)
			return
		}
		if gotQuery != nil {
			*gotQuery = r.URL.Query()
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL + "/")
}

func TestFetchStoresTheResponse(t *testing.T) {
	var q url.Values
	a := fakePower(t, http.StatusOK, `{"type":"Feature","properties":{"parameter":{"T2M":{"200101":10.5}}}}`, &q)
	s := &sink{}
	got, err := a.Fetch(context.Background(), task(), s)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"parameters": "T2M,PRECTOTCORR", "community": "AG", "latitude": "38.75", "longitude": "-9.25",
		"start": "2001", "end": "2003", "format": "JSON",
	} {
		if q.Get(key) != want {
			t.Errorf("query %s = %q, want %q", key, q.Get(key), want)
		}
	}
	if len(s.files) != 1 || s.files[0].Name != "power.json" || !strings.Contains(s.data[0], "200101") {
		t.Fatalf("files: %+v", s.files)
	}
	if c := s.files[0].Coverage; c.GetStart().GetMonth() != 1 || c.GetEnd().GetYear() != 2003 || c.GetEnd().GetMonth() != 12 {
		t.Errorf("coverage should be whole years: %v", c)
	}
	if got.Format != ingestv1.Format_FORMAT_POWER_JSON || got.Point.GetLat() != 38.75 || len(got.Variables) != 2 {
		t.Errorf("fetched: %+v", got)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		code      gatewayv1.ErrorCode
		transient bool
		message   string
	}{
		{"out of range", 422, `{"messages":["Please provide a correct end date. The data is available to 2026/11/30."]}`,
			gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false, "available to 2026/11/30"},
		{"rate limited", 429, ``, gatewayv1.ErrorCode_ERROR_CODE_QUOTA_EXCEEDED, true, "rate-limiting"},
		{"server error", 503, ``, gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true, "had a problem"},
		{"not JSON", 200, `<html>maintenance</html>`, gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true, "unexpected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fakePower(t, tt.status, tt.body, nil).Fetch(context.Background(), task(), &sink{})
			e := core.AsError(err)
			if e.Code != tt.code || e.Transient != tt.transient || !strings.Contains(e.UserMessage, tt.message) {
				t.Errorf("got %v transient=%v %q", e.Code, e.Transient, e.UserMessage)
			}
		})
	}
}

func TestIncompleteTaskIsRejected(t *testing.T) {
	a := fakePower(t, http.StatusOK, `{}`, nil)
	tk := task()
	tk.GetPower().Point = nil
	_, err := a.Fetch(context.Background(), tk, &sink{})
	if e := core.AsError(err); e.Code != gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST || e.Transient {
		t.Errorf("missing point: %v transient=%v", e.Code, e.Transient)
	}
}
