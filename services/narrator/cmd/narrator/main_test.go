package main

import (
	"net"
	"net/http"
	"testing"
)

func serveHealth(t *testing.T, network, addr string) string {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Skipf("can't listen on %s: %v", addr, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

func TestHealthcheckWildcardHosts(t *testing.T) {
	port := serveHealth(t, "tcp4", "127.0.0.1:0")
	for _, addr := range []string{":" + port, "0.0.0.0:" + port, "127.0.0.1:" + port} {
		if got := healthcheckAddr(addr); got != 0 {
			t.Errorf("healthcheckAddr(%q) = %d, want 0", addr, got)
		}
	}
	if got := healthcheckAddr("not an address"); got != 1 {
		t.Errorf("a bad address must fail, got %d", got)
	}
}

func TestHealthcheckIPv6Wildcard(t *testing.T) {
	port := serveHealth(t, "tcp6", "[::1]:0")
	if got := healthcheckAddr("[::]:" + port); got != 0 {
		t.Fatalf("healthcheckAddr([::]:%s) = %d, want 0", port, got)
	}
}
