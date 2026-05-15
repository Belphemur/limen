// Package oauthproxy hosts the Phase 5 thin OAuth surface Limen exposes on
// behalf of Zitadel: AS-metadata, the DCR proxy + RFC 7592 management
// endpoints, and the authorize/token/userinfo redirectors. The bulk of
// OAuth/OIDC behaviour lives in Zitadel; this package only contains the
// translation, rewriting, and per-tenant policy layers Limen owns.
package oauthproxy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// ValidateRedirectURI applies the global redirect-URI floor described in
// docs/phases/phase-05-authorization-server.md. It is intentionally strict:
// only HTTPS exact URIs, RFC 8252 §7.3 loopback HTTP, and reverse-DNS-shaped
// custom schemes are accepted. Fragments, IDN hosts, IP-literal HTTPS hosts,
// and userinfo are always rejected.
//
// This is the *floor* — every DCR'd redirect URI must pass it. A non-empty
// tenant allowlist (see PatternSet) further narrows what the floor allows;
// it cannot relax it.
func ValidateRedirectURI(raw string) error {
	if raw == "" {
		return errors.New("redirect_uri is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("redirect_uri %q is not a valid URI: %w", raw, err)
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("redirect_uri %q must not contain a fragment", raw)
	}
	if u.User != nil {
		return fmt.Errorf("redirect_uri %q must not contain userinfo", raw)
	}
	switch u.Scheme {
	case "":
		return fmt.Errorf("redirect_uri %q is missing a scheme", raw)
	case "https":
		return validateHTTPSRedirect(u, raw)
	case "http":
		return validateLoopbackRedirect(u, raw)
	default:
		return validateCustomSchemeRedirect(u, raw)
	}
}

func validateHTTPSRedirect(u *url.URL, raw string) error {
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("redirect_uri %q is missing a host", raw)
	}
	if ip := net.ParseIP(host); ip != nil {
		return fmt.Errorf("redirect_uri %q must not use an IP literal under https", raw)
	}
	if !isASCIIHost(host) {
		return fmt.Errorf("redirect_uri %q host must be ASCII (no IDN)", raw)
	}
	return nil
}

func validateLoopbackRedirect(u *url.URL, raw string) error {
	host := u.Hostname()
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return nil
	default:
		return fmt.Errorf("redirect_uri %q uses http but is not a loopback host (RFC 8252 §7.3)", raw)
	}
}

var customSchemeRE = regexp.MustCompile(`^[a-z][a-z0-9+\-.]*$`)

var disallowedCustomSchemes = map[string]struct{}{
	"data":       {},
	"javascript": {},
	"file":       {},
	"vbscript":   {},
	"about":      {},
}

func validateCustomSchemeRedirect(u *url.URL, raw string) error {
	scheme := strings.ToLower(u.Scheme)
	if !customSchemeRE.MatchString(scheme) {
		return fmt.Errorf("redirect_uri %q has an invalid custom scheme", raw)
	}
	if _, banned := disallowedCustomSchemes[scheme]; banned {
		return fmt.Errorf("redirect_uri %q uses a disallowed scheme", raw)
	}
	return nil
}

func isASCIIHost(host string) bool {
	for _, r := range host {
		if r > 0x7f {
			return false
		}
	}
	return true
}

// Pattern is a compiled tenant-allowlist entry. Patterns are written in a
// small glob syntax (see phase-05 spec) and validated at save time so they
// can be matched cheaply on every DCR request.
type Pattern struct {
	raw       string
	scheme    string
	hostParts []string // nil for custom schemes
	port      string   // "", literal, or "*"; only meaningful for http/https
	pathParts []string // segments; segment may be "*" or "**" (only as last segment)
	custom    bool
}

// String returns the canonical (lower-cased scheme, otherwise verbatim)
// pattern text.
func (p Pattern) String() string { return p.raw }

// CompilePattern parses a single allowlist glob. It enforces the structural
// rules — including the "≥2 fixed suffix labels" rule for wildcard hosts —
// so callers can persist only patterns that the matcher will accept.
func CompilePattern(raw string) (Pattern, error) {
	if raw == "" {
		return Pattern{}, errors.New("pattern is empty")
	}
	idx := strings.Index(raw, "://")
	if idx <= 0 {
		return Pattern{}, fmt.Errorf("pattern %q is missing a scheme://", raw)
	}
	scheme := strings.ToLower(raw[:idx])
	rest := raw[idx+3:]

	p := Pattern{raw: raw, scheme: scheme}
	switch scheme {
	case "https", "http":
		if err := compileHTTPPattern(&p, rest); err != nil {
			return Pattern{}, err
		}
	default:
		if !customSchemeRE.MatchString(scheme) {
			return Pattern{}, fmt.Errorf("pattern %q has an invalid custom scheme", raw)
		}
		if _, banned := disallowedCustomSchemes[scheme]; banned {
			return Pattern{}, fmt.Errorf("pattern %q uses a disallowed scheme", raw)
		}
		p.custom = true
		if err := compilePathSegments(&p, rest); err != nil {
			return Pattern{}, fmt.Errorf("pattern %q: %w", raw, err)
		}
	}
	return p, nil
}

func compileHTTPPattern(p *Pattern, rest string) error {
	// rest = host[:port][/path...]
	hostport, path, _ := strings.Cut(rest, "/")
	if hostport == "" {
		return fmt.Errorf("pattern %q is missing a host", p.raw)
	}

	host, portStr := splitHostPort(hostport)
	if host == "" {
		return fmt.Errorf("pattern %q is missing a host", p.raw)
	}

	labels := strings.Split(host, ".")
	wildcard := false
	for _, l := range labels {
		if l == "" {
			return fmt.Errorf("pattern %q has an empty host label", p.raw)
		}
		if l == "*" {
			wildcard = true
			continue
		}
		if strings.Contains(l, "*") {
			return fmt.Errorf("pattern %q host labels must be exactly `*` or a literal, no partial wildcards", p.raw)
		}
	}
	if wildcard {
		// Count fixed suffix labels (trailing run of non-"*" labels).
		fixed := 0
		for i := len(labels) - 1; i >= 0; i-- {
			if labels[i] == "*" {
				break
			}
			fixed++
		}
		if fixed < 2 {
			return fmt.Errorf("pattern %q wildcard host needs ≥2 fixed suffix labels (e.g. `*.acme.com`, not `*.com`)", p.raw)
		}
	}
	p.hostParts = labels

	if portStr != "" {
		if portStr != "*" {
			if _, err := strconv.ParseUint(portStr, 10, 16); err != nil {
				return fmt.Errorf("pattern %q port must be a number or `*`", p.raw)
			}
		}
		p.port = portStr
	}

	if path == "" {
		// No path component → match URIs whose path is empty or "/".
		p.pathParts = nil
		return nil
	}
	return compilePathSegments(p, path)
}

func compilePathSegments(p *Pattern, path string) error {
	// path here is everything *after* the leading "/" (compileHTTPPattern
	// strips the slash) or, for custom schemes, the opaque rest after "://".
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if s == "**" && i != len(segs)-1 {
			return fmt.Errorf("pattern %q: `**` must be the last path segment", p.raw)
		}
		if s != "*" && s != "**" && strings.Contains(s, "*") {
			return fmt.Errorf("pattern %q: path segments must be `*`, `**`, or a literal — no partial wildcards", p.raw)
		}
	}
	p.pathParts = segs
	return nil
}

func splitHostPort(hp string) (host, port string) {
	// Accept "host", "host:port", "*.acme.com:*". Not full RFC host:port
	// (no IPv6 brackets needed here — bare 127.0.0.1 / localhost only).
	if i := strings.LastIndex(hp, ":"); i >= 0 {
		return hp[:i], hp[i+1:]
	}
	return hp, ""
}

// Matches reports whether the redirect URI matches this pattern. The caller
// is expected to have already run ValidateRedirectURI on `raw` — Matches
// performs structural comparison only.
func (p Pattern) Matches(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if strings.ToLower(u.Scheme) != p.scheme {
		return false
	}
	if p.custom {
		// Custom schemes: treat everything after `scheme://` as opaque path.
		_, after, ok := strings.Cut(raw, "://")
		if !ok {
			return false
		}
		rest := after
		return matchPath(p.pathParts, strings.Split(rest, "/"))
	}
	if !matchHostLabels(p.hostParts, strings.Split(u.Hostname(), ".")) {
		return false
	}
	if !matchPort(p.port, u.Port()) {
		return false
	}
	uriPath := strings.TrimPrefix(u.EscapedPath(), "/")
	if uriPath == "" {
		// URI path is "" or "/" → pattern must also be empty (nil pathParts)
		// or a single "**" / "*" segment that accepts empty.
		if len(p.pathParts) == 0 {
			return true
		}
	}
	return matchPath(p.pathParts, strings.Split(uriPath, "/"))
}

func matchHostLabels(pattern, host []string) bool {
	if len(pattern) != len(host) {
		return false
	}
	for i := range pattern {
		if pattern[i] == "*" {
			if host[i] == "" {
				return false
			}
			continue
		}
		if !strings.EqualFold(pattern[i], host[i]) {
			return false
		}
	}
	return true
}

func matchPort(pattern, port string) bool {
	if pattern == "" {
		// Pattern omitted port → URI must have no port either.
		return port == ""
	}
	if pattern == "*" {
		return true
	}
	return pattern == port
}

func matchPath(pattern, segs []string) bool {
	if len(pattern) == 0 {
		// No path in pattern → only empty-path URIs match.
		return len(segs) == 1 && segs[0] == ""
	}
	for i, p := range pattern {
		if p == "**" {
			return true // last segment by construction; trailing match.
		}
		if i >= len(segs) {
			return false
		}
		if p == "*" {
			if segs[i] == "" {
				return false
			}
			continue
		}
		if p != segs[i] {
			return false
		}
	}
	return len(pattern) == len(segs)
}

// PatternSet is a tenant's compiled allowlist. The zero value is an empty
// (allow-floor-only) set.
type PatternSet struct {
	patterns []Pattern
}

// CompilePatternSet parses and deduplicates a list of raw glob strings,
// returning a PatternSet ready for Match. Errors are aggregated so the
// caller (e.g. the tenant-admin save handler) can surface every invalid
// pattern at once.
func CompilePatternSet(raws []string) (PatternSet, error) {
	if len(raws) == 0 {
		return PatternSet{}, nil
	}
	seen := make(map[string]struct{}, len(raws))
	out := make([]Pattern, 0, len(raws))
	var errs []error
	for _, r := range raws {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		p, err := CompilePattern(r)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, p)
	}
	if len(errs) > 0 {
		return PatternSet{}, errors.Join(errs...)
	}
	return PatternSet{patterns: out}, nil
}

// Empty reports whether the set carries any patterns. Callers use this to
// short-circuit the "floor only" path.
func (s PatternSet) Empty() bool { return len(s.patterns) == 0 }

// Len returns the number of compiled patterns.
func (s PatternSet) Len() int { return len(s.patterns) }

// Match reports whether the URI matches at least one pattern in the set.
// An empty set never matches — callers should check Empty() first.
func (s PatternSet) Match(raw string) bool {
	for _, p := range s.patterns {
		if p.Matches(raw) {
			return true
		}
	}
	return false
}

// CheckRedirectURI runs the global floor and, if the set is non-empty, the
// per-tenant glob allowlist. This is the single entrypoint the DCR proxy
// should call.
func (s PatternSet) CheckRedirectURI(raw string) error {
	if err := ValidateRedirectURI(raw); err != nil {
		return err
	}
	if s.Empty() {
		return nil
	}
	if !s.Match(raw) {
		return fmt.Errorf("redirect_uri %q is not permitted by the tenant allowlist", raw)
	}
	return nil
}
