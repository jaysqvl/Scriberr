package netpolicy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrPrivateAddress = errors.New("outbound destination resolves to a protected address")

var protectedAddressRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

// ValidatePublicURL validates URL syntax and rejects literal protected IPs.
// Hostname destinations are checked again after DNS resolution at connect time.
func ValidatePublicURL(rawURL string, requireHTTPS bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.Hostname() == "" {
		return nil, fmt.Errorf("destination must be an absolute HTTP URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("destination must not contain credentials or a fragment")
	}
	if requireHTTPS {
		if parsed.Scheme != "https" {
			return nil, fmt.Errorf("destination must use HTTPS")
		}
	} else if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("destination must use HTTP or HTTPS")
	}

	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, ErrPrivateAddress
	}
	if address, err := netip.ParseAddr(host); err == nil && !isPublicAddress(address) {
		return nil, ErrPrivateAddress
	}
	return parsed, nil
}

// NewPublicHTTPClient returns a client that rechecks every resolved address and
// redirect, preventing private-network access and DNS rebinding.
func NewPublicHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = publicDialContext(dialer, net.DefaultResolver)
	transport.MaxConnsPerHost = 8
	transport.MaxIdleConnsPerHost = 4
	transport.IdleConnTimeout = 90 * time.Second

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			_, err := ValidatePublicURL(req.URL.String(), true)
			return err
		},
	}
}

type ipResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

func publicDialContext(dialer *net.Dialer, resolver ipResolver) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid outbound address: %w", err)
		}

		if literal, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
			if !isPublicAddress(literal) {
				return nil, ErrPrivateAddress
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(literal.String(), port))
		}

		addresses, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve outbound destination: %w", err)
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("outbound destination has no addresses")
		}
		for _, resolved := range addresses {
			if !isPublicAddress(resolved) {
				return nil, ErrPrivateAddress
			}
		}

		var lastErr error
		for _, resolved := range addresses {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

func isPublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsUnspecified() || address.IsLoopback() ||
		address.IsPrivate() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsMulticast() {
		return false
	}

	for _, protected := range protectedAddressRanges {
		if protected.Contains(address) {
			return false
		}
	}
	return true
}
