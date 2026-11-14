package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A thinking model holds the headers back. Past HeaderTimeout that is
// ErrSlowStart, not an unreachable provider, while a slow body after quick
// headers is fine.
func TestHeaderTimeout(t *testing.T) {
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			slowHeaders := func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case <-time.After(400 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, p.start+p.text+p.stop)
			}
			slowBody := func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				time.Sleep(400 * time.Millisecond)
				_, _ = io.WriteString(w, p.start+p.text+p.stop)
			}
			run := func(h http.HandlerFunc, timeout time.Duration) ([]Delta, error) {
				srv := httptest.NewServer(h)
				t.Cleanup(srv.Close)
				c, err := New(p.provider, Config{BaseURL: srv.URL + p.basePath, APIKey: testCredential, HeaderTimeout: timeout}, srv.Client())
				if err != nil {
					t.Fatal(err)
				}
				ctx := streamContext(t)
				ch, err := c.Stream(ctx, testRequest(0))
				if err != nil {
					return nil, err
				}
				return collect(t, ctx, ch), nil
			}

			_, err := run(slowHeaders, 100*time.Millisecond)
			if !errors.Is(err, ErrSlowStart) || errors.Is(err, ErrUpstream) || errors.Is(err, context.Canceled) {
				t.Fatalf("slow headers: err = %v, want only ErrSlowStart", err)
			}
			if got, err := run(slowHeaders, 2*time.Second); err != nil || len(got) != 2 || got[1].Text != "Rain 🌧" {
				t.Fatalf("headers within the timeout: %v, %v", got, err)
			}
			if got, err := run(slowBody, 100*time.Millisecond); err != nil || len(got) != 2 || got[1].Err != nil {
				t.Fatalf("slow body after quick headers: %v, %v", got, err)
			}
			if got, err := run(slowHeaders, 0); err != nil || len(got) != 2 {
				t.Fatalf("no header timeout: %v, %v", got, err)
			}
		})
	}
}

// The caller's own deadline still reads as a deadline, even when the header
// timeout is longer.
func TestCallerDeadlineBeatsHeaderTimeout(t *testing.T) {
	p := providers[0]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the body so the server notices when the client goes away.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	c, err := New(p.provider, Config{BaseURL: srv.URL + p.basePath, HeaderTimeout: time.Minute}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = c.Stream(ctx, testRequest(0))
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrSlowStart) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}
