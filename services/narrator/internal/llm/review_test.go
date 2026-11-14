package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStreamTruncation(t *testing.T) {
	for _, p := range providers {
		for _, tt := range []struct {
			name      string
			openAI    string
			anthropic string
			truncated bool
		}{
			{"token limit", "length", "max_tokens", true},
			{"normal stop", "stop", "end_turn", false},
			{"stop sequence", "stop", "stop_sequence", false},
			{"no reason", "", "", false},
		} {
			for _, withText := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/text=%t", p.name, tt.name, withText), func(t *testing.T) {
					client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, p.start)
						if withText {
							_, _ = io.WriteString(w, p.text)
						}
						if p.name == "openai" {
							// Ignore finish reasons belonging to other choices.
							_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":1,\"finish_reason\":\"length\"},{\"index\":0,\"finish_reason\":%q}]}\n\n", tt.openAI)
							// Usage metadata may arrive after the finish reason.
							_, _ = io.WriteString(w, "data: {\"model\":\"resolved-model\",\"choices\":[],\"usage\":{\"completion_tokens\":77}}\n\ndata: [DONE]\n\n")
						} else {
							_, _ = fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":77}}\n\n", tt.anthropic)
							_, _ = io.WriteString(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n"+p.stop)
						}
					}, "")
					ctx := streamContext(t)
					ch, err := client.Stream(ctx, testRequest(77))
					if err != nil {
						t.Fatal(err)
					}
					got := collect(t, ctx, ch)
					want := []Delta{{Model: "resolved-model"}}
					if withText {
						want = append(want, Delta{Text: "Rain 🌧"})
					}
					if p.name == "openai" {
						want = append(want, Delta{Model: "resolved-model"})
					}
					if tt.truncated {
						want = append(want, Delta{Truncated: true})
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("deltas = %+v, want %+v", got, want)
					}
				})
			}
		}
	}
}

func TestAuthenticationErrorDoesNotEchoBody(t *testing.T) {
	// A partial fake credential cannot be removed by full-credential replacement.
	partial := testCredential[:4] + "..." + testCredential[len(testCredential)-4:]
	for _, p := range providers {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			for _, body := range []struct{ name, text string }{
				{"json", fmt.Sprintf(`{"error":{"message":"Incorrect API key provided: %s"}}`, partial)},
				{"plain text", "Incorrect API key provided: " + partial},
				{"html", "<html>Permission denied for " + partial + "</html>"},
				{"empty", ""},
			} {
				t.Run(fmt.Sprintf("%s/%d/%s", p.name, status, body.name), func(t *testing.T) {
					client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(status)
						_, _ = io.WriteString(w, body.text)
					}, testCredential)
					ch, err := client.Stream(streamContext(t), testRequest(0))
					if ch != nil || !errors.Is(err, ErrUnauthorized) {
						t.Fatal("expected synchronous authentication error")
					}
					want := fmt.Sprintf("%s: HTTP %d: provider authentication failed", ErrUnauthorized, status)
					if err.Error() != want || strings.Contains(err.Error(), partial) {
						t.Fatal("authentication error must not contain provider response text")
					}
				})
			}
		}
	}
}

func TestDeadlineBeforeHeaders(t *testing.T) {
	for _, p := range providers {
		for _, tt := range []struct {
			name         string
			contextLimit time.Duration
			clientLimit  time.Duration
		}{
			{"context deadline", 250 * time.Millisecond, 0},
			{"HTTP client timeout", 5 * time.Second, 250 * time.Millisecond},
		} {
			t.Run(p.name+"/"+tt.name, func(t *testing.T) {
				started, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					close(started)
					select {
					case <-r.Context().Done():
						close(stopped)
					case <-release:
					}
				}))
				t.Cleanup(srv.Close)
				t.Cleanup(func() { close(release) })
				httpClient := srv.Client()
				httpClient.Timeout = tt.clientLimit
				client, err := New(p.provider, Config{BaseURL: srv.URL + p.basePath}, httpClient)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), tt.contextLimit)
				defer cancel()
				ch, err := client.Stream(ctx, testRequest(0))
				// The server checks ErrUpstream before context errors, so a deadline
				// must not also match ErrUpstream or it will map to unavailable.
				if ch != nil || !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUpstream) {
					t.Fatalf("expected synchronous deadline without upstream classification, got %v", err)
				}
				waitSignal(t, started)
				waitSignal(t, stopped)
			})
		}
	}
}
