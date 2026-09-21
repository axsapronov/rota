package proxy

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	"github.com/alpkeskin/rota/core/internal/models"
)

// hcBodyLimit caps how much of a health-check response body is read for
// body validation. IP-echo endpoints answer in under 100 bytes; anything
// larger (HTML pages, JSON blobs) is truncated before the match, so a
// full-page body can never accidentally satisfy a short pattern.
const hcBodyLimit = 64 * 1024

var (
	bodyPatternMu    sync.RWMutex
	bodyPatternCache = make(map[string]*regexp.Regexp)
)

// ipPatterns detect an IPv4/IPv6 address anywhere in the body (the built-in
// "ip" strategy). The boundary groups keep the patterns RE2-safe (no
// lookarounds) and stop matches inside longer dotted/hex sequences, so a
// version string like "1.2.3.4.5" does not count as an IP.
var (
	ipV4Pattern = regexp.MustCompile(`(^|[^0-9.])((?:\d{1,3}\.){3}\d{1,3})([^0-9.]|$)`)
	ipV6Pattern = regexp.MustCompile(`(^|[^0-9a-fA-F:])((?:[0-9a-fA-F]{0,4}:){2,7}[0-9a-fA-F]{0,4})([^0-9a-fA-F:]|$)`)
)

// BodyStrategy describes how a health-check response body is validated after
// the status-code check.
type BodyStrategy struct {
	Strategy string // one of the models.Strategy* values
	Value    string // substring / Go regex for the contains / regex strategies
}

// Normalize resolves an empty strategy to the built-in IP check.
func (s BodyStrategy) Normalize() BodyStrategy {
	if s.Strategy == "" {
		s.Strategy = models.StrategyIP
	}
	return s
}

// Valid reports whether the strategy is one of the known values.
func (s BodyStrategy) Valid() bool {
	switch s.Strategy {
	case models.StrategyStatus, models.StrategyIP, models.StrategyContains, models.StrategyRegex:
		return true
	default:
		return false
	}
}

// bodyContainsIP reports whether body contains an IPv4 or IPv6 address.
func bodyContainsIP(body []byte) bool {
	return ipV4Pattern.Match(body) || ipV6Pattern.Match(body)
}

// Validate checks an (already truncated) health-check response body. It
// returns a user-facing error describing the mismatch.
func (s BodyStrategy) Validate(body []byte) error {
	switch s.Strategy {
	case models.StrategyStatus:
		return nil
	case models.StrategyIP:
		if bodyContainsIP(body) {
			return nil
		}
		return fmt.Errorf("body does not contain an IP address (got %q)", bodySnippet(body))
	case models.StrategyContains:
		if strings.Contains(string(body), s.Value) {
			return nil
		}
		return fmt.Errorf("body does not contain %q (got %q)", s.Value, bodySnippet(body))
	case models.StrategyRegex:
		re, err := CompileBodyPattern(s.Value)
		if err != nil {
			return fmt.Errorf("invalid body pattern %q: %v", s.Value, err)
		}
		if re.Match(body) {
			return nil
		}
		return fmt.Errorf("body does not match pattern %q (got %q)", s.Value, bodySnippet(body))
	default:
		// Unknown strategy (stale config): fall back to the built-in IP
		// check instead of failing every proxy on a config typo.
		return BodyStrategy{Strategy: models.StrategyIP}.Validate(body)
	}
}

// bodySnippet returns a short one-line preview of a body for error messages.
func bodySnippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// CompileBodyPattern compiles a health-check body pattern, caching the result
// process-wide (the pattern is a global setting that changes rarely, while
// checks run thousands at a time).
func CompileBodyPattern(pattern string) (*regexp.Regexp, error) {
	bodyPatternMu.RLock()
	re, ok := bodyPatternCache[pattern]
	bodyPatternMu.RUnlock()
	if ok {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	bodyPatternMu.Lock()
	bodyPatternCache[pattern] = re
	bodyPatternMu.Unlock()
	return re, nil
}

// ReadHCBody reads at most hcBodyLimit bytes from r for body validation.
func ReadHCBody(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, hcBodyLimit))
}
