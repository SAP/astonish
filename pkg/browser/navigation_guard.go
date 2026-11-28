package browser

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// NavigationGuard checks URLs before browser navigation to prevent SSRF.
type NavigationGuard struct {
	BlockPrivateNetworks bool
	AllowLoopback        bool     // permits only literal loopback IPs and exact localhost
	AllowedHostnames     []string // overrides private network block
}

// DefaultNavigationGuard returns a guard that blocks private networks.
func DefaultNavigationGuard() *NavigationGuard {
	return &NavigationGuard{
		BlockPrivateNetworks: true,
	}
}

// Check validates a URL for browser navigation. Returns nil if allowed.
func (g *NavigationGuard) Check(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("empty URL")
	}

	// Allow about:blank
	if rawURL == "about:blank" {
		return nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Only allow http and https
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("blocked: protocol %q not allowed (only http/https)", parsed.Scheme)
	}

	if !g.BlockPrivateNetworks {
		return nil
	}

	host := parsed.Hostname()

	// Code mode may reach local dev servers, but never grant this exception
	// to arbitrary DNS names that resolve to loopback/private addresses.
	if g.AllowLoopback {
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}

	// Check hostname allowlist
	for _, allowed := range g.AllowedHostnames {
		if strings.EqualFold(host, allowed) {
			return nil
		}
	}

	// Block localhost
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("blocked: navigation to localhost")
	}

	// Resolve and check IPs
	ips, err := net.LookupIP(host)
	if err != nil {
		// Can't resolve — allow (might be a valid external host temporarily unreachable)
		return nil
	}

	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("blocked: %s resolves to private IP %s", host, ip)
		}
	}

	return nil
}

// isPrivateIP returns true for loopback, private, and link-local addresses.
func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	privateRanges := []struct {
		network *net.IPNet
	}{
		{parseCIDR("10.0.0.0/8")},
		{parseCIDR("172.16.0.0/12")},
		{parseCIDR("192.168.0.0/16")},
		{parseCIDR("169.254.0.0/16")},
		{parseCIDR("fc00::/7")},
		{parseCIDR("::1/128")},
	}

	for _, r := range privateRanges {
		if r.network.Contains(ip) {
			return true
		}
	}

	return false
}

func parseCIDR(s string) *net.IPNet {
	_, n, _ := net.ParseCIDR(s)
	return n
}
