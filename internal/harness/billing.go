package harness

import (
	"net"
	"net/url"
	"strings"
)

// billableEndpoint reports whether requests to baseURL are paid for.
//
// A model served from this machine, a container, or an SSH tunnel to private
// infrastructure costs nothing per token, so a missing price there is not a gap
// in the table. A public endpoint does bill, so a missing price there is a gap
// and must not be shown as $0.00.
func billableEndpoint(baseURL string) bool {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return false
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".local") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
	}
	// A public hostname we cannot resolve here is assumed to bill, so an
	// unpriced model errs toward "unknown" rather than toward "free".
	return true
}
