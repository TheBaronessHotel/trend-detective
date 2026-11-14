// Package egress decides which addresses the narrator may call for an LLM.
// llm.base_url comes from the browser, so without these checks anyone who can
// reach the gateway could make the narrator send requests into its own
// network or to a cloud metadata service.
//
// Three rules apply:
//
//   - Some addresses are never dialed: link-local (169.254.0.0/16, which
//     holds the cloud metadata address, and fe80::/10), unspecified,
//     multicast, broadcast, and the known metadata addresses outside those
//     ranges (fd00:ec2::254, 100.100.100.200).
//   - A name that looks public, with a dot and not under .localhost, must
//     resolve to a public address. An internal address (loopback, private,
//     shared 100.64.0.0/10, unique-local) is refused unless the operator
//     listed the name in the allowlist. This catches tricks like
//     127.0.0.1.nip.io and DNS that changes between checks, because the
//     check runs on the address actually dialed.
//   - Local names (localhost, *.localhost, host.docker.internal, single-label
//     names like ollama) and IP literals may be internal, since that is how
//     a local Ollama or LM Studio is reached.
//
// With an outbound proxy, the narrator dials the proxy, not the LLM host. The
// proxy itself is exempt from these checks (the operator configured it), and
// the target host is checked before the request goes to the proxy.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"syscall"
)

// ErrForbidden means the address is one the narrator never calls.
var ErrForbidden = errors.New("egress: address not allowed")

var (
	broadcast = netip.MustParseAddr("255.255.255.255")
	metadata  = map[netip.Addr]bool{
		netip.MustParseAddr("fd00:ec2::254"):   true, // AWS IMDS over IPv6
		netip.MustParseAddr("100.100.100.200"): true, // Alibaba Cloud
	}
	sharedSpace = netip.MustParsePrefix("100.64.0.0/10")
)

// Forbidden reports whether ip must never be dialed.
func Forbidden(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip == broadcast || metadata[ip]
}

// Internal reports whether ip belongs to this machine or a private network.
func Internal(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	return ip.IsLoopback() || ip.IsPrivate() || sharedSpace.Contains(ip)
}

// LocalName reports whether host is a name that only resolves on this
// machine or network: localhost, *.localhost, host.docker.internal, or a
// single label such as a compose service name. It doesn't accept IP literals
// or a trailing dot.
func LocalName(host string) bool {
	host = strings.ToLower(host)
	if host == "" {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "host.docker.internal" {
		return true
	}
	return !strings.Contains(host, ".")
}

// Guard applies the rules to an http.Transport. Build one with NewGuard.
type Guard struct {
	// Listed reports whether the operator listed host in the allowlist. A
	// listed name may resolve to an internal address.
	Listed func(host string) bool
	// Proxies are the proxy addresses (host:port, lowercase) the transport
	// may dial without checks.
	Proxies map[string]bool
	// Lookup resolves a target host when a proxy is used. Nil means
	// net.DefaultResolver.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

// NewGuard returns a Guard that trusts the proxies named in the environment
// (HTTPS_PROXY, HTTP_PROXY and their lowercase forms) and the hosts listed
// reports true for.
func NewGuard(listed func(host string) bool) *Guard {
	return &Guard{Listed: listed, Proxies: ProxiesFromEnv()}
}

// ProxiesFromEnv returns the host:port of each proxy set in the environment.
func ProxiesFromEnv() map[string]bool {
	out := map[string]bool{}
	for _, name := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if addr, ok := proxyAddr(os.Getenv(name)); ok {
			out[addr] = true
		}
	}
	return out
}

// proxyAddr turns a proxy setting into host:port, the way
// http.ProxyFromEnvironment reads it: no scheme means http.
func proxyAddr(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		u, err = url.Parse("http://" + v)
		if err != nil || u.Host == "" {
			return "", false
		}
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return strings.ToLower(net.JoinHostPort(u.Hostname(), port)), true
}

// Check reports whether dialing ip for host is allowed. host is the name in
// the URL, or the IP literal itself.
func (g *Guard) Check(host string, ip netip.Addr) error {
	if Forbidden(ip) {
		return fmt.Errorf("%w: %s", ErrForbidden, ip)
	}
	if !Internal(ip) {
		return nil
	}
	if _, err := netip.ParseAddr(host); err == nil || LocalName(host) || (g.Listed != nil && g.Listed(host)) {
		return nil
	}
	return fmt.Errorf("%w: %s resolves to the internal address %s", ErrForbidden, host, ip)
}

// DialContext wraps d so every address it connects to passes Check. Use it
// as http.Transport.DialContext.
func (g *Guard) DialContext(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if g.Proxies[strings.ToLower(addr)] {
			return d.DialContext(ctx, network, addr)
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		guarded := *d
		guarded.Control = func(network, address string, c syscall.RawConn) error {
			ipText, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(ipText)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrForbidden, address)
			}
			if err := g.Check(host, ip); err != nil {
				return err
			}
			if d.Control != nil {
				return d.Control(network, address, c)
			}
			return nil
		}
		return guarded.DialContext(ctx, network, addr)
	}
}

// Proxy wraps next, usually http.ProxyFromEnvironment, so a request that
// goes through a proxy has its target host checked first: the dial check
// only sees the proxy. A name that can't be resolved here is left to the
// proxy, since some networks only resolve outside names there.
func (g *Guard) Proxy(next func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		proxy, err := next(req)
		if err != nil || proxy == nil {
			return proxy, err
		}
		if err := g.checkTarget(req.Context(), req.URL.Hostname()); err != nil {
			return nil, err
		}
		return proxy, nil
	}
}

func (g *Guard) checkTarget(ctx context.Context, host string) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		return g.Check(host, ip)
	}
	if LocalName(host) {
		return nil
	}
	lookup := g.Lookup
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return nil
	}
	for _, ip := range ips {
		if err := g.Check(host, ip); err != nil {
			return err
		}
	}
	return nil
}
