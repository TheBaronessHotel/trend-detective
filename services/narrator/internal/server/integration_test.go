package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	analysisv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/analysis/v1"
	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
	"github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1/narratorv1connect"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/llm"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/prompt"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/verify"
	"google.golang.org/protobuf/encoding/protojson"
)

// sseProvider is a fake LLM API for one wire format.
type sseProvider struct {
	name     string
	provider narratorv1.Provider
	path     string // request path under the base URL
	authOK   func(*http.Request) bool
	frames   func(model string, chunks []string) string
}

var sseProviders = []sseProvider{
	{
		name: "openai", provider: narratorv1.Provider_PROVIDER_OPENAI_CHAT, path: "/v1/chat/completions",
		authOK: func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+secretKey },
		frames: func(model string, chunks []string) string {
			var b strings.Builder
			fmt.Fprintf(&b, "data: {\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n", model)
			for _, c := range chunks {
				js, _ := json.Marshal(c)
				fmt.Fprintf(&b, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%s}}]}\n\n", js)
			}
			b.WriteString("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return b.String()
		},
	},
	{
		name: "anthropic", provider: narratorv1.Provider_PROVIDER_ANTHROPIC_MESSAGES, path: "/v1/messages",
		authOK: func(r *http.Request) bool { return r.Header.Get("x-api-key") == secretKey },
		frames: func(model string, chunks []string) string {
			var b strings.Builder
			fmt.Fprintf(&b, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":%q}}\n\n", model)
			for _, c := range chunks {
				js, _ := json.Marshal(c)
				fmt.Fprintf(&b, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", js)
			}
			b.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return b.String()
		},
	},
}

// realisticSeries is a SeriesAnalysis as analysis would write it.
func realisticSeries() *analysisv1.SeriesAnalysis {
	return &analysisv1.SeriesAnalysis{
		VariableId: "gpm_precip", Units: "mm/day", Alpha: 0.05, NValid: 240, NTotal: 240,
		Regression: &analysisv1.RegressionResult{
			SlopePerDecade: 0.42, SlopeCiLow: 0.15, SlopeCiHigh: 0.69,
			PValue: 0.003, Significant: true, RSquared: 0.04, N: 240, NEffective: 180,
		},
		MannKendall: &analysisv1.MannKendallResult{
			Tau: 0.12, Z: 2.8, PValue: 0.005, Significant: true,
			SensSlopePerDecade: 0.39, SensCiLow: 0.11, SensCiHigh: 0.66, N: 240,
		},
		Agreement: &analysisv1.Agreement{SameDirection: true, SameSignificance: true},
		Warnings:  []string{"Only 20 years of data"},
	}
}

// TestNarrateEndToEnd runs the real loader, prompt builder, verifier and LLM
// adapters against a fake object store and a fake SSE provider. Only the
// network endpoints are fake.
func TestNarrateEndToEnd(t *testing.T) {
	seriesJSON, err := protojson.Marshal(realisticSeries())
	if err != nil {
		t.Fatal(err)
	}
	// "0.42" and "0.003" are facts; "7.7" is made up and must be flagged.
	chunks := []string{"Rainfall rose by 0.42 mm/day per decade ", "(p = 0.003), ", "about 7.7 mm more."}

	for _, p := range sseProviders {
		t.Run(p.name, func(t *testing.T) {
			var mu sync.Mutex
			var gotBody map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != p.path {
					http.Error(w, "wrong path "+r.URL.Path, http.StatusNotFound)
					return
				}
				if !p.authOK(r) {
					http.Error(w, `{"error":{"message":"bad key","type":"authentication_error"}}`, http.StatusUnauthorized)
					return
				}
				data, _ := io.ReadAll(r.Body)
				mu.Lock()
				_ = json.Unmarshal(data, &gotBody)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, p.frames("resolved-model-2026", chunks))
			}))
			defer upstream.Close()

			logs, log := newCapture()
			srv, err := New(Deps{
				Loader:  BucketLoader{Name: "analysis", Bucket: &fakeBucket{objects: map[string]string{seriesKey: string(seriesJSON)}}},
				Builder: prompt.NewBuilder(), Verifier: verify.NewVerifier(),
				NewClient: func(pr narratorv1.Provider, cfg llm.Config) (llm.Client, error) {
					return llm.New(pr, cfg, upstream.Client())
				},
				Log: log, Timeout: 5 * time.Second,
				BaseURLAllowlist: ParseAllowlist("127.0.0.1"),
			})
			if err != nil {
				t.Fatal(err)
			}
			cs := connect.NewServer()
			narratorv1connect.RegisterNarratorServiceHandler(cs, srv)
			mux := http.NewServeMux()
			connecthttp.Mount(mux, cs)
			hs := httptest.NewServer(mux)
			defer hs.Close()
			e := &env{client: narratorv1connect.NewNarratorServiceClient(connect.NewClient(connecthttp.NewTransport(hs.Client(), hs.URL)))}

			base := upstream.URL
			if p.provider == narratorv1.Provider_PROVIDER_OPENAI_CHAT {
				base += "/v1" // OpenAI-compatible roots include /v1
			}
			req := &narratorv1.NarrateRequest{
				Result:   &analysisv1.AnalysisResult{Series: &commonv1.BlobRef{Bucket: "analysis", Key: seriesKey}},
				Variable: &narratorv1.VariableContext{Id: "gpm_precip", Name: "Precipitation", Units: "mm/day"},
				Dates: &commonv1.DateRange{
					Start: &commonv1.Date{Year: 2001, Month: 1, Day: 1},
					End:   &commonv1.Date{Year: 2020, Month: 12, Day: 31},
				},
				Llm: &narratorv1.LlmConfig{Provider: p.provider, BaseUrl: base, Model: "requested-model"},
			}
			r := e.call(t, t.Context(), req, secretKey)
			if r.err != nil {
				t.Fatalf("Narrate: %v\nlogs:\n%s", r.err, logs.text())
			}
			if r.order != "tttd" || strings.Join(r.texts, "") != strings.Join(chunks, "") {
				t.Fatalf("events %q, texts %q", r.order, r.texts)
			}
			if r.done.GetModel() != "resolved-model-2026" {
				t.Errorf("done.model = %q", r.done.GetModel())
			}
			if got := r.done.GetUnverifiedNumbers(); len(got) != 1 || got[0] != "7.7" {
				t.Errorf("unverified = %q, want [7.7]", got)
			}

			// The provider got the requested model and a prompt with the facts.
			mu.Lock()
			body, _ := json.Marshal(gotBody)
			mu.Unlock()
			for _, want := range []string{`"requested-model"`, "0.42", "0.003", "Only 20 years of data"} {
				if !strings.Contains(string(body), want) {
					t.Errorf("LLM request body lacks %s", want)
				}
			}
			if strings.Contains(logs.text(), secretKey) {
				t.Errorf("the key leaked into the logs")
			}
		})
	}
}

// Provider error bodies travel through the real adapters and handler: our
// message first, then the provider's own words when it sent a JSON error,
// and nothing from any other body.
func TestNarrateEndToEndProviderErrors(t *testing.T) {
	seriesJSON, err := protojson.Marshal(realisticSeries())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		provider narratorv1.Provider
		status   int
		body     string
		want     connect.Code
		wantMsg  string
	}{
		{
			name: "openai no credits", provider: narratorv1.Provider_PROVIDER_OPENAI_CHAT, status: 429,
			body: `{"error":{"message":"You have no credits remaining.","type":"insufficient_quota","code":"insufficient_quota"}}`,
			want: connect.CodeResourceExhausted, wantMsg: msgQuota + " Provider said: You have no credits remaining.",
		},
		{
			name: "gemini model gone", provider: narratorv1.Provider_PROVIDER_OPENAI_CHAT, status: 404,
			body:    `[{"error":{"code":404,"message":"models/gemini-2.0-flash is no longer available to new users.","status":"NOT_FOUND"}}]`,
			want:    connect.CodeInvalidArgument,
			wantMsg: msgBadRequest + " Provider said: models/gemini-2.0-flash is no longer available to new users.",
		},
		{
			name: "anthropic credit balance", provider: narratorv1.Provider_PROVIDER_ANTHROPIC_MESSAGES, status: 400,
			body:    `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`,
			want:    connect.CodeResourceExhausted,
			wantMsg: msgQuota + " Provider said: Your credit balance is too low to access the Anthropic API.",
		},
		{
			name: "anthropic overloaded", provider: narratorv1.Provider_PROVIDER_ANTHROPIC_MESSAGES, status: 529,
			body: `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			want: connect.CodeUnavailable, wantMsg: msgUpstream + " Provider said: Overloaded",
		},
		{
			name: "non-json body is not shown", provider: narratorv1.Provider_PROVIDER_OPENAI_CHAT, status: 500,
			body: "<html>admin console of an internal service</html>",
			want: connect.CodeUnavailable, wantMsg: msgUpstream,
		},
		{
			name: "key in the body is redacted", provider: narratorv1.Provider_PROVIDER_OPENAI_CHAT, status: 400,
			body:    `{"error":{"message":"model not allowed for key ` + secretKey + `"}}`,
			want:    connect.CodeInvalidArgument,
			wantMsg: msgBadRequest + " Provider said: model not allowed for key [redacted]",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			logs, log := newCapture()
			srv, err := New(Deps{
				Loader:  BucketLoader{Name: "analysis", Bucket: &fakeBucket{objects: map[string]string{seriesKey: string(seriesJSON)}}},
				Builder: prompt.NewBuilder(), Verifier: verify.NewVerifier(),
				NewClient: func(pr narratorv1.Provider, cfg llm.Config) (llm.Client, error) {
					return llm.New(pr, cfg, upstream.Client())
				},
				Log: log, Timeout: 5 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			cs := connect.NewServer()
			narratorv1connect.RegisterNarratorServiceHandler(cs, srv)
			mux := http.NewServeMux()
			connecthttp.Mount(mux, cs)
			hs := httptest.NewServer(mux)
			defer hs.Close()
			e := &env{client: narratorv1connect.NewNarratorServiceClient(connect.NewClient(connecthttp.NewTransport(hs.Client(), hs.URL)))}

			req := &narratorv1.NarrateRequest{
				Result: &analysisv1.AnalysisResult{Series: &commonv1.BlobRef{Bucket: "analysis", Key: seriesKey}},
				Llm:    &narratorv1.LlmConfig{Provider: tc.provider, BaseUrl: upstream.URL + "/v1", Model: "m"},
			}
			r := e.call(t, t.Context(), req, secretKey)
			var ce *connect.Error
			if !errors.As(r.err, &ce) || ce.Code() != tc.want || ce.Message() != tc.wantMsg {
				t.Fatalf("err = %v, want %v %q", r.err, tc.want, tc.wantMsg)
			}
			if strings.Contains(logs.text(), secretKey) {
				t.Errorf("the key leaked into the logs")
			}
		})
	}
}
