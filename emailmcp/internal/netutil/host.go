// Package netutil provides host validation for outbound IMAP/SMTP connections.
package netutil

import (
	"fmt"
	"net"
	"strings"
)

// blockedNetworks are RFC1918, loopback, link-local, and other non-routable
// ranges that must not be used as IMAP/SMTP targets (SSRF protection).
var blockedNetworks = mustParseCIDRs(
	// IPv4 loopback and "this" network
	"127.0.0.0/8",
	"0.0.0.0/8",
	// RFC1918 private
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	// Carrier-grade NAT
	"100.64.0.0/10",
	// Link-local / metadata
	"169.254.0.0/16",
	// IETF protocol assignments / documentation / benchmark
	"192.0.0.0/24",
	"192.0.2.0/24",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"198.18.0.0/15",
	// Multicast / reserved
	"224.0.0.0/4",
	"240.0.0.0/4",
	// IPv6
	"::1/128",
	"::/128",
	"fc00::/7",  // unique local
	"fe80::/10", // link-local
	"ff00::/8",  // multicast
	// IPv4-mapped IPv6 of the above is handled after unmapping
)

// metadataHostnames are well-known cloud metadata endpoints blocked by name
// even when DNS is manipulated.
var metadataHostnames = map[string]struct{}{
	"metadata":                 {},
	"metadata.google.internal": {},
	"metadata.goog":            {},
	"instance-data":            {},
	"kubernetes.default":       {},
	"kubernetes.default.svc":   {},
}

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("netutil: invalid CIDR " + c + ": " + err.Error())
		}
		out = append(out, n)
	}
	return out
}

// IsLocalhost reports whether host is an explicit loopback name or address
// (localhost, 127.0.0.1, ::1). Used for the non-TLS local-dev exception only.
func IsLocalhost(host string) bool {
	h := normalizeHost(host)
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ValidatePublicHost resolves host and rejects private, link-local, and
// metadata addresses to prevent SSRF via IMAP/SMTP configuration.
func ValidatePublicHost(host string) error {
	h := normalizeHost(host)
	if h == "" {
		return fmt.Errorf("host is required")
	}
	if strings.ContainsAny(h, "/\\") || strings.Contains(h, "..") {
		return fmt.Errorf("invalid host %q", host)
	}
	if _, ok := metadataHostnames[h]; ok {
		return fmt.Errorf("host %q is not allowed", host)
	}
	// Block bare "metadata.google.internal" style suffixes.
	if strings.HasSuffix(h, ".internal") || strings.HasSuffix(h, ".local") {
		return fmt.Errorf("host %q is not allowed", host)
	}

	// Literal IP — check directly without DNS.
	if ip := net.ParseIP(h); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("host %q resolves to a blocked address", host)
		}
		return nil
	}

	ips, err := net.LookupIP(h)
	if err != nil {
		return fmt.Errorf("resolve host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("host %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("host %q resolves to a blocked address (%s)", host, ip.String())
		}
	}
	return nil
}

// RequireTLSUnlessLocalhost returns an error when useTLS is false and host is
// not an explicit localhost target.
func RequireTLSUnlessLocalhost(host string, useTLS bool, proto string) error {
	if useTLS {
		return nil
	}
	if IsLocalhost(host) {
		return nil
	}
	return fmt.Errorf("%s without TLS is only allowed for localhost, got %q", proto, host)
}

func normalizeHost(host string) string {
	h := strings.TrimSpace(host)
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	// Strip optional trailing dot from FQDNs.
	h = strings.TrimSuffix(h, ".")
	return strings.ToLower(h)
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// Normalize IPv4-mapped IPv6 to IPv4 for CIDR checks.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, n := range blockedNetworks {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
