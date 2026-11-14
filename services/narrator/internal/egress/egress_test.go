package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestForbiddenAndInternal(t *testing.T) {
	tests := []struct {
		ip                  string
		forbidden, internal bool
	}{
		{"169.254.169.254", true, false},
		{"::ffff:169.254.169.254", true, false},
		{"fe80::1", true, false},
		{"fd00:ec2::254", true, true},
		{"100.100.100.200", true, true},
		{"0.0.0.0", true, false},
		{"::", true, false},
		{"224.0.0.1", true, false},
		{"255.255.255.255", true, false},
		{"127.0.0.1", false, true},
		{"::ffff:127.0.0.1", false, true},
		{"::1", false, true},
		{"10.1.2.3", false, true},
		{"172.17.0.1", false, true},
		{"192.168.1.20", false, true},
		{"100.64.0.1", false, true},
		{"fd12::1", false, true},
		{"8.8.8.8", false, false},
		{"2606:4700::1111", false, false},
	}
	for _, tc := range tests {
		ip := netip.MustParseAddr(tc.ip)
		if got := Forbidden(ip); got != tc.forbidden {
			t.Errorf("Forbidden(%s) = %v, want %v", tc.ip, got, tc.forbidden)
		}
		if got := Internal(ip); got != tc.internal {
			t.Errorf("Internal(%s) = %v, want %v", tc.ip, got, tc.internal)
		}
	}
}

func TestLocalName(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "LOCALHOST": true, "lm.localhost": true, "host.docker.internal": true,
		"ollama": true, "seaweedfs": true,
		"": false, "localhost.": false, "api.openai.com": false, "127.0.0.1": false, "::1": false,
		"127.0.0.1.nip.io": false,
	} {
		if got := LocalName(host); got != want {
			t.Errorf("LocalName(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestCheck(t *testing.T) {
	g := &Guard{Listed: func(h string) bool { return h == "llm.home.lan" }}
	tests := []struct {
		host, ip string
		ok       bool
	}{
		{"api.openai.com", "104.18.6.192", true},
		{"api.openai.com", "127.0.0.1", false},
		{"127.0.0.1.nip.io", "127.0.0.1", false},
		{"llm.home.lan", "192.168.1.5", true},
		{"llm.home.lan", "169.254.169.254", false},
		{"localhost", "127.0.0.1", true},
		{"ollama", "172.18.0.4", true},
		{"host.docker.internal", "172.17.0.1", true},
		{"192.168.1.20", "192.168.1.20", true},
		{"169.254.169.254", "169.254.169.254", false},
		{"metadata", "169.254.169.254", false},
	}
	for _, tc := range tests {
		err := g.Check(tc.host, netip.MustParseAddr(tc.ip))
		if (err == nil) != tc.ok {
			t.Errorf("Check(%s, %s) = %v, want ok=%v", tc.host, tc.ip, err, tc.ok)
		}
		if err != nil && !errors.Is(err, ErrForbidden) {
			t.Errorf("Check(%s, %s) = %v, want ErrForbidden", tc.host, tc.ip, err)
		}
	}
}

func TestProxyAddr(t *testing.T) {
	for in, want := range map[string]string{
		"http://host.docker.internal:3128": "host.docker.internal:3128",
		"host.docker.internal:3128":        "host.docker.internal:3128",
		"http://Proxy.Example":             "proxy.example:80",
		"https://proxy.example":            "proxy.example:443",
		"socks5://127.0.0.1":               "127.0.0.1:1080",
		"http://user:pw@proxy:8080":        "proxy:8080",
	} {
		if got, ok := proxyAddr(in); !ok || got != want {
			t.Errorf("proxyAddr(%q) = %q, %v, want %q", in, got, ok, want)
		}
	}
	if _, ok := proxyAddr(" "); ok {
		t.Error("an empty setting is not a proxy")
	}
}

// guardedClient returns a client whose transport dials through g, and through
// proxy when it is set.
func guardedClient(g *Guard, proxy *url.URL) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = g.DialContext(&net.Dialer{Timeout: 2 * time.Second})
	tr.Proxy = nil
	if proxy != nil {
		tr.Proxy = g.Proxy(http.ProxyURL(proxy))
	}
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

func get(c *http.Client, u string) (string, error) {
	resp, err := c.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// "localhost." has a dot, so it looks public, but the hosts file resolves it
// to loopback: the dial check must refuse it, while a local name and an IP
// literal for the same server pass.
func TestDialRefusesPublicNameOnInternalAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	c := guardedClient(&Guard{}, nil)

	for _, u := range []string{srv.URL, "http://localhost:" + itoa(port)} {
		if body, err := get(c, u); err != nil || body != "ok" {
			t.Errorf("GET %s = %q, %v, want ok", u, body, err)
		}
	}
	if _, err := get(c, "http://localhost.:"+itoa(port)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("GET via localhost. = %v, want ErrForbidden", err)
	}
	listed := guardedClient(&Guard{Listed: func(h string) bool { return h == "localhost." }}, nil)
	if body, err := get(listed, "http://localhost.:"+itoa(port)); err != nil || body != "ok" {
		t.Fatalf("a listed name may resolve internally: %q, %v", body, err)
	}
}

// With a proxy, the transport dials the proxy, which is exempt even when its
// own name would fail the check, and the target host is checked instead.
func TestProxyIsExemptButTargetIsChecked(t *testing.T) {
	var hits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "via proxy to "+r.URL.Host)
	}))
	defer proxy.Close()
	port := proxy.Listener.Addr().(*net.TCPAddr).Port
	// A public-looking proxy name that resolves to loopback.
	proxyHostPort := "localhost.:" + itoa(port)
	proxyURL, _ := url.Parse("http://" + proxyHostPort)
	g := &Guard{
		Proxies: map[string]bool{proxyHostPort: true},
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			switch host {
			case "llm.example.com":
				return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
			case "rebind.example.com":
				return []netip.Addr{netip.MustParseAddr("10.0.0.7")}, nil
			}
			return nil, errors.New("no such host")
		},
	}
	c := guardedClient(g, proxyURL)

	if body, err := get(c, "http://llm.example.com/v1"); err != nil || !strings.Contains(body, "via proxy to llm.example.com") {
		t.Fatalf("public target through the proxy: %q, %v", body, err)
	}
	if body, err := get(c, "http://unresolvable.example.com/v1"); err != nil || !strings.Contains(body, "via proxy") {
		t.Fatalf("a target only the proxy can resolve: %q, %v", body, err)
	}
	before := hits.Load()
	for _, u := range []string{"http://169.254.169.254/latest/meta-data", "http://rebind.example.com/v1", "http://[fd00:ec2::254]/"} {
		if _, err := get(c, u); !errors.Is(err, ErrForbidden) {
			t.Errorf("GET %s through the proxy = %v, want ErrForbidden", u, err)
		}
	}
	if hits.Load() != before {
		t.Fatal("a refused target reached the proxy")
	}

	// Without the exemption, the proxy's own name fails the dial check.
	strict := guardedClient(&Guard{Lookup: g.Lookup}, proxyURL)
	if _, err := get(strict, "http://llm.example.com/v1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unlisted proxy = %v, want ErrForbidden", err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
