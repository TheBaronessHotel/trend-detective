package main

// Constructors for the parts of the narrator built in their own packages:
// the prompt builder and verifier (NAR-2) and the LLM adapters (NAR-1).

import (
	"net"
	"net/http"
	"time"

	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/llm"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/prompt"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/server"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/verify"
)

func newBuilder() prompt.Builder { return prompt.NewBuilder() }

func newVerifier() verify.Verifier { return verify.NewVerifier() }

// newLLMClient returns a factory for the OpenAI Chat and Anthropic Messages
// adapters. All calls share httpClient's connection pool.
func newLLMClient(httpClient *http.Client) server.ClientFactory {
	return func(p narratorv1.Provider, cfg llm.Config) (llm.Client, error) {
		return llm.New(p, cfg, httpClient)
	}
}

// newLLMHTTPClient is the HTTP client for LLM calls. It has no overall
// timeout, since answers stream for a while and NARRATE_TIMEOUT bounds them.
// It doesn't follow redirects, so an allowed base_url can't send the
// narrator, and the user's key, to a host the allowlist refuses.
func newLLMHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 10 * time.Second
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
