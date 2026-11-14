package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
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

	newClient := newLLMClient(newLLMHTTPClient())
	for _, p := range []narratorv1.Provider{narratorv1.Provider_PROVIDER_OPENAI_CHAT, narratorv1.Provider_PROVIDER_ANTHROPIC_MESSAGES} {
		c, err := newClient(p, llm.Config{BaseURL: allowed.URL + "/v1", APIKey: "sk-test"})
		if err != nil {
			t.Fatalf("%v: %v", p, err)
		}
		_, err = c.Stream(t.Context(), llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}})
		if !errors.Is(err, llm.ErrUpstream) {
			t.Errorf("%v: Stream error = %v, want ErrUpstream for a redirect", p, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the redirect target was called %d times", n)
	}
}

func TestLLMClientFactoryRejectsUnknownProvider(t *testing.T) {
	_, err := newLLMClient(newLLMHTTPClient())(narratorv1.Provider_PROVIDER_UNSPECIFIED, llm.Config{})
	if !errors.Is(err, llm.ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}
