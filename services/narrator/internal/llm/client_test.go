package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
)

// A deliberately fake credential, never read from the environment.
const testCredential = "test-only-credential"

type providerFixture struct {
	name     string
	provider narratorv1.Provider
	basePath string
	endpoint string
	start    string
	text     string
	stop     string
}

var providers = []providerFixture{
	{
		name: "openai", provider: narratorv1.Provider_PROVIDER_OPENAI_CHAT,
		basePath: "/compatible/v1", endpoint: "/compatible/v1/chat/completions",
		start: "data: {\"model\":\"resolved-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n",
		text:  "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Rain 🌧\"}}]}\n\n",
		stop:  "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
	},
	{
		name: "anthropic", provider: narratorv1.Provider_PROVIDER_ANTHROPIC_MESSAGES,
		basePath: "/proxy", endpoint: "/proxy/v1/messages",
		start: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"resolved-model\"}}\n\n",
		text:  "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Rain 🌧\"}}\n\n",
		stop:  "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	},
}

func newTestClient(t *testing.T, p providerFixture, handler http.HandlerFunc, key string) Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := New(p.provider, Config{BaseURL: srv.URL + p.basePath + "/", APIKey: key}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func streamContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func collect(t *testing.T, ctx context.Context, ch <-chan Delta) []Delta {
	t.Helper()
	var out []Delta
	for {
		select {
		case d, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, d)
		case <-ctx.Done():
			t.Fatal("stream did not close before deadline")
		}
	}
}

func TestStreamHappyPath(t *testing.T) {
	for _, p := range providers {
		for _, split := range []int{0, 1, 7} {
			for _, key := range []string{"", testCredential} {
				name := fmt.Sprintf("%s/chunk=%d/auth=%t", p.name, split, key != "")
				t.Run(name, func(t *testing.T) {
					client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
						checkRequest(t, p, r, key, 77, 0)
						w.Header().Set("Content-Type", "text/event-stream")
						// CRLF, comments and unknown fields are valid SSE framing.
						wire := ": heartbeat\r\nid: ignored\r\n\r\n" + strings.ReplaceAll(p.start+p.text+p.stop, "\n", "\r\n")
						step := split
						if step == 0 {
							step = len(wire)
						}
						for len(wire) > 0 {
							n := min(step, len(wire))
							if _, err := io.WriteString(w, wire[:n]); err != nil {
								return
							}
							w.(http.Flusher).Flush()
							wire = wire[n:]
						}
					}, key)
					ctx := streamContext(t)
					ch, err := client.Stream(ctx, testRequest(77))
					if err != nil {
						t.Fatal(err)
					}
					got := collect(t, ctx, ch)
					want := []Delta{{Model: "resolved-model"}, {Text: "Rain 🌧"}}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("deltas = %+v, want %+v", got, want)
					}
				})
			}
		}
	}
}

func testRequest(maxTokens int) Request {
	return Request{
		System: "Explain supplied facts.", Model: "requested-model", MaxTokens: maxTokens,
		Messages:    []Message{{RoleUser, "First question"}, {RoleAssistant, "Earlier answer"}, {RoleUser, "Follow-up"}},
		Temperature: 0,
	}
}

func checkRequest(t *testing.T, p providerFixture, r *http.Request, key string, maxTokens int, temperature float64) {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != p.endpoint {
		t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, p.endpoint)
	}
	if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "text/event-stream" {
		t.Error("missing JSON content type or SSE accept header")
	}
	auth := ""
	if key != "" {
		auth = "Bearer " + key
	}
	if p.name == "openai" {
		if r.Header.Get("Authorization") != auth || (key == "" && r.Header.Values("Authorization") != nil) {
			t.Error("incorrect Authorization header")
		}
		if r.Header.Get("x-api-key") != "" {
			t.Error("unexpected Anthropic credential header")
		}
	} else {
		if r.Header.Get("x-api-key") != key || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Error("incorrect Anthropic headers")
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("unexpected Authorization header")
		}
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Error(err)
		return
	}
	want := map[string]any{"model": "requested-model", "stream": true}
	if temperature != 0 {
		want["temperature"] = temperature
	}
	messages := []any{
		map[string]any{"role": "user", "content": "First question"},
		map[string]any{"role": "assistant", "content": "Earlier answer"},
		map[string]any{"role": "user", "content": "Follow-up"},
	}
	if p.name == "openai" {
		messages = append([]any{map[string]any{"role": "system", "content": "Explain supplied facts."}}, messages...)
		if maxTokens != 0 {
			want["max_tokens"] = float64(maxTokens)
		}
	} else {
		want["system"] = "Explain supplied facts."
		if maxTokens == 0 {
			maxTokens = 1500
		}
		want["max_tokens"] = float64(maxTokens)
	}
	want["messages"] = messages
	if !reflect.DeepEqual(body, want) {
		t.Errorf("request body = %#v, want %#v", body, want)
	}
}

func TestStreamRequestOptions(t *testing.T) {
	for _, p := range providers {
		for _, tt := range []struct {
			name        string
			maxTokens   int
			temperature float64
		}{
			{"defaults", 0, 0},
			{"explicit tokens", 77, 0},
			{"explicit temperature", 0, 0.4},
			{"both explicit", 77, 0.4},
		} {
			t.Run(p.name+"/"+tt.name, func(t *testing.T) {
				client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
					checkRequest(t, p, r, "", tt.maxTokens, tt.temperature)
					_, _ = io.WriteString(w, p.stop)
				}, "")
				ctx := streamContext(t)
				req := testRequest(tt.maxTokens)
				req.Temperature = tt.temperature
				ch, err := client.Stream(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				if got := collect(t, ctx, ch); len(got) != 0 {
					t.Fatalf("unexpected deltas: %+v", got)
				}
			})
		}
	}
}

func TestHTTPStatusErrors(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{400, ErrBadRequest}, {401, ErrUnauthorized}, {403, ErrUnauthorized},
		{404, ErrBadRequest}, {429, ErrRateLimited}, {500, ErrUpstream},
		{503, ErrUpstream}, {529, ErrUpstream}, {418, ErrUpstream},
	}
	for _, p := range providers {
		for _, tt := range tests {
			for _, asJSON := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%d/json=%t", p.name, tt.status, asJSON), func(t *testing.T) {
					client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(tt.status)
						message := "provider explanation " + testCredential
						if asJSON {
							_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message}})
						} else {
							_, _ = io.WriteString(w, message)
						}
					}, testCredential)
					ch, err := client.Stream(streamContext(t), testRequest(0))
					if ch != nil || !errors.Is(err, tt.want) {
						t.Fatalf("expected nil channel and %v", tt.want)
					}
					if errors.Is(err, ErrUnauthorized) {
						want := fmt.Sprintf("%s: HTTP %d: provider authentication failed", ErrUnauthorized, tt.status)
						if err.Error() != want {
							t.Fatal("authentication error must use the fixed message")
						}
					} else {
						assertSafeError(t, err, "provider explanation")
					}
				})
			}
		}
	}
}

func assertSafeError(t *testing.T, err error, message string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), testCredential) {
		t.Fatal("error exposed the credential")
	}
	if !strings.Contains(err.Error(), message) {
		t.Errorf("error missing expected explanation %q", message)
	}
}

func TestMidStreamError(t *testing.T) {
	tests := []struct {
		kind string
		code any
		want error
	}{
		{"authentication_error", nil, ErrUnauthorized},
		{"permission_error", nil, ErrUnauthorized},
		{"rate_limit_error", nil, ErrRateLimited},
		{"invalid_request_error", nil, ErrBadRequest},
		{"not_found_error", nil, ErrBadRequest},
		{"overloaded_error", nil, ErrUpstream},
		{"server_error", nil, ErrUpstream},
		{"", "invalid_api_key", ErrUnauthorized},
		{"", "rate_limit_exceeded", ErrRateLimited},
		{"", "model_not_found", ErrBadRequest},
		{"", 429, ErrRateLimited},
	}
	for _, p := range providers {
		for i, tt := range tests {
			t.Run(fmt.Sprintf("%s/%d", p.name, i), func(t *testing.T) {
				client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, p.start+p.text+"event: error\ndata: ")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"type": "error", "error": map[string]any{
							"type": tt.kind, "code": tt.code, "message": "provider explanation " + testCredential,
						},
					})
					// Output after the terminal error must never reach the consumer.
					_, _ = io.WriteString(w, "\n"+p.text+p.stop)
				}, testCredential)
				ctx := streamContext(t)
				ch, err := client.Stream(ctx, testRequest(0))
				if err != nil {
					t.Fatal(err)
				}
				got := collect(t, ctx, ch)
				if len(got) != 3 || got[0].Model != "resolved-model" || got[1].Text != "Rain 🌧" || !errors.Is(got[2].Err, tt.want) {
					t.Fatal("expected model, text, then terminal provider error")
				}
				assertSafeError(t, got[2].Err, "provider explanation")
			})
		}
	}
}

func TestInvalidOrTruncatedStream(t *testing.T) {
	for _, p := range providers {
		for _, tt := range []struct{ name, wire string }{
			{"malformed JSON", "data: {not-json}\n\n"},
			{"premature EOF", p.start + p.text},
			{"empty body", ""},
			{"oversized line", "data: " + strings.Repeat("x", maxEventBytes+1) + "\n\n"},
		} {
			t.Run(p.name+"/"+tt.name, func(t *testing.T) {
				client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.WriteString(w, tt.wire)
				}, testCredential)
				ctx := streamContext(t)
				ch, err := client.Stream(ctx, testRequest(0))
				if err != nil {
					t.Fatal(err)
				}
				got := collect(t, ctx, ch)
				if len(got) == 0 || !errors.Is(got[len(got)-1].Err, ErrUpstream) {
					t.Fatal("expected terminal upstream error")
				}
				assertSafeError(t, got[len(got)-1].Err, "stream")
			})
		}
	}
}

func TestCancellation(t *testing.T) {
	for _, p := range providers {
		for _, phase := range []string{"before headers", "reading body", "blocked send"} {
			t.Run(p.name+"/"+phase, func(t *testing.T) {
				started, stopped := make(chan struct{}), make(chan struct{})
				release := make(chan struct{})
				client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					if phase != "before headers" {
						w.Header().Set("Content-Type", "text/event-stream")
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
					}
					if phase == "blocked send" {
						_, _ = io.WriteString(w, p.start+p.text)
						w.(http.Flusher).Flush()
					}
					close(started)
					select {
					case <-r.Context().Done():
						close(stopped)
					case <-release:
					}
				}, testCredential)
				// Always unblock the fake server before its cleanup, even on failure.
				t.Cleanup(func() { close(release) })
				ctx, cancel := context.WithCancel(streamContext(t))
				t.Cleanup(cancel)
				type result struct {
					ch  <-chan Delta
					err error
				}
				returned := make(chan result, 1)
				go func() {
					ch, err := client.Stream(ctx, testRequest(0))
					returned <- result{ch, err}
				}()
				waitSignal(t, started)
				var got result
				if phase != "before headers" {
					select {
					case got = <-returned:
					case <-time.After(3 * time.Second):
						t.Fatal("Stream did not return after headers")
					}
					if got.err != nil {
						t.Fatal(got.err)
					}
				}
				cancel()
				if phase == "before headers" {
					select {
					case got = <-returned:
					case <-time.After(3 * time.Second):
						t.Fatal("Stream ignored cancellation")
					}
					if got.ch != nil || !errors.Is(got.err, context.Canceled) {
						t.Fatal("expected synchronous context cancellation")
					}
				} else {
					// Leave sends blocked until the server observes request cancellation.
					waitSignal(t, stopped)
					collect(t, streamContext(t), got.ch)
				}
				waitSignal(t, stopped)
			})
		}
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for request lifecycle")
	}
}

func TestTerminalEventClosesRequest(t *testing.T) {
	for _, p := range providers {
		for _, ending := range []string{"done", "provider error", "malformed event"} {
			t.Run(p.name+"/"+ending, func(t *testing.T) {
				stopped, release := make(chan struct{}), make(chan struct{})
				client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, p.text)
					switch ending {
					case "done":
						_, _ = io.WriteString(w, p.stop)
					case "provider error":
						_, _ = io.WriteString(w, "event: error\ndata: {\"error\":{\"message\":\"failed\"}}\n\n")
					case "malformed event":
						_, _ = io.WriteString(w, "data: broken\n\n")
					}
					w.(http.Flusher).Flush()
					// Keep the upstream connection open past its terminal event.
					select {
					case <-r.Context().Done():
						close(stopped)
					case <-release:
					}
				}, "")
				t.Cleanup(func() { close(release) })
				ctx := streamContext(t)
				ch, err := client.Stream(ctx, testRequest(0))
				if err != nil {
					t.Fatal(err)
				}
				got := collect(t, ctx, ch)
				if len(got) == 0 || got[0].Text != "Rain 🌧" {
					t.Fatal("missing initial text")
				}
				if ending == "done" {
					if len(got) != 1 {
						t.Fatal("expected clean completion")
					}
				} else if len(got) != 2 || !errors.Is(got[1].Err, ErrUpstream) {
					t.Fatal("expected terminal stream error")
				}
				waitSignal(t, stopped)
			})
		}
	}
}

func TestSSEFraming(t *testing.T) {
	for _, tt := range []struct {
		name string
		wire string
		want []sseEvent
	}{
		{
			"multiline data and named event", ": heartbeat\nevent: ignored\n\nevent: chunk\ndata: {\ndata: \"model\":\"resolved\"}\n\ndata: [DONE]\n\n",
			[]sseEvent{{name: "chunk", data: "{\n\"model\":\"resolved\"}"}, {data: "[DONE]"}},
		},
		{
			"no spaces or final newline", "event:chunk\ndata:hello\n\ndata:[DONE]",
			[]sseEvent{{name: "chunk", data: "hello"}, {data: "[DONE]"}},
		},
		{
			"line beyond scanner default", "data:" + strings.Repeat("x", 70<<10) + "\n\ndata:[DONE]\n\n",
			[]sseEvent{{data: strings.Repeat("x", 70<<10)}, {data: "[DONE]"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got []sseEvent
			err := readSSE(strings.NewReader(tt.wire), func(e sseEvent) (bool, error) {
				got = append(got, e)
				return e.data == "[DONE]", nil
			})
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatal("SSE framing did not preserve event boundaries and data")
			}
		})
	}
}

func TestAnthropicIgnoresNonTextEvents(t *testing.T) {
	p := providers[1]
	client := newTestClient(t, p, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, p.start+
			"event: ping\ndata: {\"type\":\"ping\"}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\"}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"private\"}}\n\n"+
			p.text+p.stop)
	}, "")
	ctx := streamContext(t)
	ch, err := client.Stream(ctx, testRequest(0))
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(t, ctx, ch); !reflect.DeepEqual(got, []Delta{{Model: "resolved-model"}, {Text: "Rain 🌧"}}) {
		t.Fatal("non-text events produced output")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNew(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider narratorv1.Provider
		base     string
		endpoint string
		wantErr  bool
	}{
		{"default OpenAI", providers[0].provider, "", "https://api.openai.com/v1/chat/completions", false},
		{"default Anthropic", providers[1].provider, "", "https://api.anthropic.com/v1/messages", false},
		{"unspecified", narratorv1.Provider_PROVIDER_UNSPECIFIED, "", "", true},
		{"unknown", narratorv1.Provider(99), "", "", true},
		{"relative", providers[0].provider, "/v1", "", true},
		{"scheme", providers[0].provider, "ftp://localhost", "", true},
		{"missing host", providers[0].provider, "http:///v1", "", true},
		{"malformed", providers[0].provider, "http://[", "", true},
		{"URL credentials", providers[0].provider, "https://user:" + testCredential + "@example.invalid", "", true},
		{"fragment", providers[1].provider, "https://example.invalid/#fragment", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, err := New(tt.provider, Config{BaseURL: tt.base, APIKey: testCredential}, nil)
			if tt.wantErr {
				if client != nil || !errors.Is(err, ErrBadRequest) {
					t.Fatal("expected bad request")
				}
				assertSafeError(t, err, "llm:")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var transport *transport
			switch c := client.(type) {
			case *openAIClient:
				transport = &c.transport
			case *anthropicClient:
				transport = &c.transport
			default:
				t.Fatal("unknown adapter")
			}
			if transport.endpoint != tt.endpoint || transport.http != http.DefaultClient {
				t.Error("incorrect default endpoint or HTTP client")
			}
		})
	}
}

func TestTransportErrors(t *testing.T) {
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("transport explanation " + testCredential)
			})}
			client, err := New(p.provider, Config{APIKey: testCredential}, httpClient)
			if err != nil {
				t.Fatal(err)
			}
			ch, err := client.Stream(streamContext(t), testRequest(0))
			if ch != nil || !errors.Is(err, ErrUpstream) {
				t.Fatal("expected synchronous upstream error")
			}
			assertSafeError(t, err, "transport explanation")
		})
	}
}

func TestRequestEncodingError(t *testing.T) {
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			client, err := New(p.provider, Config{}, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("invalid request reached transport")
				return nil, errors.New("unexpected request")
			})})
			if err != nil {
				t.Fatal(err)
			}
			req := testRequest(0)
			req.Temperature = math.NaN()
			ch, err := client.Stream(streamContext(t), req)
			if ch != nil || !errors.Is(err, ErrBadRequest) {
				t.Fatal("expected request encoding failure")
			}
		})
	}
}
