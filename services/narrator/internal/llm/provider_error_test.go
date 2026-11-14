package llm

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Bodies as the providers send them. testCredential stands in for the key.
const (
	openAIQuota     = `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`
	openAINoCredits = `{"error":{"message":"You have no credits remaining.","type":"invalid_request_error","code":"insufficient_quota"}}`
	openRouter402   = `{"error":{"message":"Insufficient credits. Add more using https://openrouter.ai/settings/credits","code":402}}`
	anthropicCredit = `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`
	anthropicBusy   = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	gemini404       = `[{"error":{"code":404,"message":"models/gemini-2.0-flash is no longer available to new users. Please update your code to use a newer model.","status":"NOT_FOUND"}}]`
	geminiRate      = `[{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.","status":"RESOURCE_EXHAUSTED"}}]`
)

func TestProviderErrorBodies(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		want     error
		wantShow string // "" means nothing may be shown
	}{
		{"openai insufficient_quota", 429, openAIQuota, ErrQuotaExhausted, "You exceeded your current quota, please check your plan and billing details."},
		{"openai no credits", 429, openAINoCredits, ErrQuotaExhausted, "You have no credits remaining."},
		{"openrouter 402", 402, openRouter402, ErrQuotaExhausted, "Insufficient credits. Add more using https://openrouter.ai/settings/credits"},
		{"anthropic credit balance", 400, anthropicCredit, ErrQuotaExhausted, "Your credit balance is too low to access the Anthropic API."},
		{"anthropic overloaded", 529, anthropicBusy, ErrUpstream, "Overloaded"},
		{"gemini model gone, array-wrapped", 404, gemini404, ErrBadRequest, "models/gemini-2.0-flash is no longer available"},
		{"gemini per-minute limit stays a rate limit", 429, geminiRate, ErrRateLimited, "You exceeded your current quota"},
		{"plain rate limit", 429, `{"error":{"message":"Rate limit reached for requests","code":"rate_limit_exceeded"}}`, ErrRateLimited, "Rate limit reached"},
		{"402 without a body", 402, ``, ErrQuotaExhausted, ""},
		{"html is never shown", 500, `<html><body>internal admin page, token=abc</body></html>`, ErrUpstream, ""},
		{"plain text is never shown", 404, `404 page not found`, ErrBadRequest, ""},
		{"json without an error object", 400, `{"message":"from some internal service"}`, ErrBadRequest, ""},
		{"error as a string", 400, `{"error":"nope"}`, ErrBadRequest, ""},
		{"error without a message", 500, `{"error":{"code":500}}`, ErrUpstream, ""},
	}
	for _, p := range providers {
		for _, tc := range tests {
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}, testCredential)
				ch, err := client.Stream(streamContext(t), testRequest(0))
				if ch != nil || !errors.Is(err, tc.want) {
					t.Fatalf("err = %v, want %v", err, tc.want)
				}
				shown := ProviderMessage(err)
				if tc.wantShow == "" {
					if shown != "" {
						t.Fatalf("shown %q, want nothing", shown)
					}
					return
				}
				if !strings.Contains(shown, tc.wantShow) {
					t.Fatalf("shown %q, want it to contain %q", shown, tc.wantShow)
				}
			})
		}
	}
}

func TestProviderMessageIsSafe(t *testing.T) {
	long := strings.Repeat("word ", 200)
	body := `{"error":{"message":"bad model for key ` + testCredential + ` and sk-proj-abcdef123456 or AIzaSyA1234567890abcdefghijk.\u001b[31m red\u0000\n` + long + `"}}`
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, body)
			}, testCredential)
			_, err := client.Stream(streamContext(t), testRequest(0))
			shown := ProviderMessage(err)
			if !strings.HasPrefix(shown, "bad model for key [redacted] and [redacted] or [redacted]") {
				t.Fatalf("shown %q", shown)
			}
			for _, bad := range []string{testCredential, "sk-proj", "AIza"} {
				if strings.Contains(shown, bad) || strings.Contains(err.Error(), bad) {
					t.Fatalf("%q leaked in %q", bad, shown)
				}
			}
			for _, r := range shown {
				if unicode.IsControl(r) {
					t.Fatalf("control character %U in %q", r, shown)
				}
			}
			if n := utf8.RuneCountInString(shown); n > maxShownRunes+1 {
				t.Fatalf("shown is %d runes, want at most %d", n, maxShownRunes+1)
			}
		})
	}
}

// 401 and 403 bodies stay hidden even as JSON: they can quote part of the key.
func TestAuthErrorShowsNothing(t *testing.T) {
	for _, p := range providers {
		client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key provided: test****tial"}}`)
		}, testCredential)
		_, err := client.Stream(streamContext(t), testRequest(0))
		if !errors.Is(err, ErrUnauthorized) || ProviderMessage(err) != "" {
			t.Fatalf("%s: err = %v, shown %q", p.name, err, ProviderMessage(err))
		}
	}
}

func TestRedirectIsItsOwnError(t *testing.T) {
	for _, p := range providers {
		for _, status := range []int{301, 302, 307, 308} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://internal.example/secret")
				w.WriteHeader(status)
			}))
			t.Cleanup(srv.Close)
			// The narrator's client doesn't follow redirects (cmd/narrator).
			noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			client, err := New(p.provider, Config{BaseURL: srv.URL + p.basePath, APIKey: testCredential}, noFollow)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Stream(streamContext(t), testRequest(0))
			if !errors.Is(err, ErrRedirect) || strings.Contains(err.Error(), "internal.example") {
				t.Fatalf("%s/%d: err = %v, want ErrRedirect without the Location", p.name, status, err)
			}
		}
	}
}

func TestMidStreamQuotaError(t *testing.T) {
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, p.start+"event: error\ndata: "+openAINoCredits+"\n\n")
			}, testCredential)
			ctx := streamContext(t)
			ch, err := client.Stream(ctx, testRequest(0))
			if err != nil {
				t.Fatal(err)
			}
			got := collect(t, ctx, ch)
			last := got[len(got)-1].Err
			if !errors.Is(last, ErrQuotaExhausted) || ProviderMessage(last) != "You have no credits remaining." {
				t.Fatalf("last = %v, shown %q", last, ProviderMessage(last))
			}
		})
	}
}
