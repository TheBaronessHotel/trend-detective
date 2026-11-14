package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/egress"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/llm"
)

// An allowed base_url must not be able to redirect the call, and the user's
// key, to another host.
func TestLLMClientDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer allowed.Close()

	newClient := newLLMClient(newLLMHTTPClient(&egress.Guard{}), 0)
	for _, p := range []narratorv1.Provider{narratorv1.Provider_PROVIDER_OPENAI_CHAT, narratorv1.Provider_PROVIDER_ANTHROPIC_MESSAGES} {
		c, err := newClient(p, llm.Config{BaseURL: allowed.URL + "/v1", APIKey: "sk-test"})
		if err != nil {
			t.Fatalf("%v: %v", p, err)
		}
		_, err = c.Stream(t.Context(), llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}})
		if !errors.Is(err, llm.ErrRedirect) {
			t.Errorf("%v: Stream error = %v, want ErrRedirect for a redirect", p, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the redirect target was called %d times", n)
	}
}

func TestLLMClientFactoryRejectsUnknownProvider(t *testing.T) {
	_, err := newLLMClient(newLLMHTTPClient(&egress.Guard{}), 0)(narratorv1.Provider_PROVIDER_UNSPECIFIED, llm.Config{})
	if !errors.Is(err, llm.ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

// The dial check runs on the address actually dialed: a name that looks
// public but resolves to loopback ("localhost." through the hosts file) is
// refused, and the error still says why.
func TestLLMClientRefusesPublicNameOnLoopback(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	_, port, _ := net.SplitHostPort(target.Listener.Addr().String())

	newClient := newLLMClient(newLLMHTTPClient(&egress.Guard{}), 0)
	c, err := newClient(narratorv1.Provider_PROVIDER_OPENAI_CHAT, llm.Config{BaseURL: "http://localhost.:" + port + "/v1", APIKey: "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Stream(t.Context(), llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}})
	if !errors.Is(err, egress.ErrForbidden) {
		t.Fatalf("Stream error = %v, want egress.ErrForbidden", err)
	}
	if strings.Contains(err.Error(), "sk-test") {
		t.Fatal("the error shows the key")
	}
	if hits.Load() != 0 {
		t.Fatal("the refused address was called")
	}
}

func TestLLMHeaderTimeout(t *testing.T) {
	tests := []struct {
		env     string
		narrate time.Duration
		want    time.Duration
		wantErr bool
	}{
		{"", 3 * time.Minute, defaultHeaderTimeout, false},
		{"90s", 3 * time.Minute, 90 * time.Second, false},
		{"10m", 3 * time.Minute, 3 * time.Minute, false},
		{"5m", 0, 5 * time.Minute, false},
		{"0", 3 * time.Minute, 0, true},
		{"-1s", 3 * time.Minute, 0, true},
		{"soon", 3 * time.Minute, 0, true},
	}
	for _, tc := range tests {
		t.Setenv("NARRATOR_LLM_HEADER_TIMEOUT", tc.env)
		got, err := llmHeaderTimeout(tc.narrate)
		if (err != nil) != tc.wantErr || (!tc.wantErr && got != tc.want) {
			t.Errorf("env %q, narrate %s: got %s, %v; want %s, err=%v", tc.env, tc.narrate, got, err, tc.want, tc.wantErr)
		}
	}
}
