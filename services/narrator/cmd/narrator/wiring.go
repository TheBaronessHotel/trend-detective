package main

// Constructors for the parts of the narrator built in their own packages:
// the prompt builder and verifier (NAR-2) and the LLM adapters (NAR-1).

import (
	"net"
	"net/http"
	"time"

	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/envx"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/egress"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/llm"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/prompt"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/server"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/verify"
)

func newBuilder() prompt.Builder { return prompt.NewBuilder() }

func newVerifier() verify.Verifier { return verify.NewVerifier() }

// newLLMClient returns a factory for the OpenAI Chat and Anthropic Messages
// adapters. All calls share httpClient's connection pool. headerTimeout
// bounds the wait for response headers (NARRATOR_LLM_HEADER_TIMEOUT).
func newLLMClient(httpClient *http.Client, headerTimeout time.Duration) server.ClientFactory {
	return func(p narratorv1.Provider, cfg llm.Config) (llm.Client, error) {
		cfg.HeaderTimeout = headerTimeout
		return llm.New(p, cfg, httpClient)
	}
}

// defaultHeaderTimeout is how long a provider may take before its response
// headers. Thinking models (Gemini via its OpenAI-compatible API, reasoning
// models on OpenRouter) and a local model loading cold can hold them for
// more than a minute.
const defaultHeaderTimeout = 150 * time.Second

// llmHeaderTimeout reads NARRATOR_LLM_HEADER_TIMEOUT, a positive
// duration. It never exceeds narrate, the whole call's NARRATE_TIMEOUT, when
// that is set.
func llmHeaderTimeout(narrate time.Duration) (time.Duration, error) {
	d, err := envx.Duration("NARRATOR_LLM_HEADER_TIMEOUT", defaultHeaderTimeout)
	if err != nil {
		return 0, err
	}
	if narrate > 0 && d > narrate {
		d = narrate
	}
	return d, nil
}

// newLLMHTTPClient is the HTTP client for LLM calls. It has no overall
// timeout, since answers stream for a while and NARRATE_TIMEOUT bounds them.
// It doesn't follow redirects, so an allowed base_url can't send the
// narrator, and the user's key, to a host the allowlist refuses. guard
// checks every address it dials, and the target of every request that goes
// through a proxy from HTTPS_PROXY or HTTP_PROXY.
func newLLMHTTPClient(guard *egress.Guard) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = guard.Proxy(http.ProxyFromEnvironment)
	tr.DialContext = guard.DialContext(&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second})
	tr.TLSHandshakeTimeout = 10 * time.Second
	// No ResponseHeaderTimeout here: llm.Config.HeaderTimeout does that
	// job and reports it as ErrSlowStart, not as an unreachable provider.
	tr.ResponseHeaderTimeout = 0
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
