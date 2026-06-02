package services

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const (
	geoSkipReasonInvalidAddress   = "unparseable_address"
	geoSkipReasonInternalReserved = "reserved_or_private_ip"
)

var excludedIPv4Networks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
}

type ipClassification struct {
	SkipGeo bool
	Reason  string
}

// normalizeProxyAddress reduces proxy lines to host:port (drops trailing labels like country names).
func normalizeProxyAddress(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	if at := strings.LastIndex(address, "@"); at >= 0 {
		address = address[at+1:]
	}

	if host, port, err := net.SplitHostPort(address); err == nil {
		return net.JoinHostPort(strings.Trim(host, "[]"), port)
	}

	parts := strings.Split(address, ":")
	if len(parts) >= 3 {
		if ip := net.ParseIP(parts[0]); ip != nil && ip.To4() != nil {
			port, err := strconv.Atoi(parts[1])
			if err == nil && port > 0 && port <= 65535 {
				return parts[0] + ":" + parts[1]
			}
		}
	}

	return address
}

// extractIP returns a public lookup key (IPv4/IPv6 string) from a proxy address line.
func extractIP(address string) string {
	norm := normalizeProxyAddress(address)
	if norm == "" {
		return ""
	}

	host := norm
	if h, _, err := net.SplitHostPort(norm); err == nil {
		host = strings.Trim(h, "[]")
	} else if i := strings.Index(norm, ":"); i > 0 {
		candidate := norm[:i]
		if net.ParseIP(candidate) != nil {
			host = candidate
		}
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

// isSkippableGeoIP reports IPs that should not be sent to ip-api.com (private, loopback, etc.).
func isSkippableGeoIP(ipStr string) bool {
	return classifyIPForGeo(ipStr).SkipGeo
}

func classifyIPForGeo(ipStr string) ipClassification {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ipClassification{SkipGeo: true, Reason: geoSkipReasonInvalidAddress}
	}
	if v4 := ip.To4(); v4 != nil {
		addr, ok := netip.AddrFromSlice(v4)
		if ok {
			for _, prefix := range excludedIPv4Networks {
				if prefix.Contains(addr) {
					return ipClassification{SkipGeo: true, Reason: geoSkipReasonInternalReserved}
				}
			}
		}
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return ipClassification{SkipGeo: true, Reason: geoSkipReasonInternalReserved}
	}
	if ip.IsGlobalUnicast() {
		return ipClassification{}
	}
	// Treat non-global addresses (including reserved ranges like 0.0.0.0/8) as internal.
	return ipClassification{SkipGeo: true, Reason: geoSkipReasonInternalReserved}
}
