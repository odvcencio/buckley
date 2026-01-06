package giturl

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ClonePolicy controls which git clone URLs Buckley will accept when it needs to
// clone a repository (headless sessions, batch workers, etc).
//
// Empty allow-lists mean "allow all" (subject to deny lists).
type ClonePolicy struct {
	AllowedSchemes       []string `yaml:"allowed_schemes"`
	AllowedHosts         []string `yaml:"allowed_hosts"`
	DeniedHosts          []string `yaml:"denied_hosts"`
	DenyPrivateNetworks  bool     `yaml:"deny_private_networks"`
	ResolveDNS           bool     `yaml:"resolve_dns"`
	DenySCPSyntax        bool     `yaml:"deny_scp_syntax"`
	DNSResolveTimeoutSec int      `yaml:"dns_resolve_timeout_seconds"`
}

type parsedCloneURL struct {
	Scheme   string
	HostPort string
	Host     string
}

func ValidateCloneURL(policy ClonePolicy, raw string) error {
	return ValidateCloneURLWithContext(context.Background(), policy, raw)
}

func ValidateCloneURLWithContext(ctx context.Context, policy ClonePolicy, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("git URL is empty")
	}

	parsed, err := parseCloneURL(raw, !policy.DenySCPSyntax)
	if err != nil {
		return err
	}

	allowedSchemes := normalizeList(policy.AllowedSchemes)
	if len(allowedSchemes) > 0 && !contains(allowedSchemes, parsed.Scheme) {
		return fmt.Errorf("git URL scheme %q is not allowed", parsed.Scheme)
	}

	allowedHosts := normalizeList(policy.AllowedHosts)
	deniedHosts := normalizeList(policy.DeniedHosts)

	if parsed.Host != "" {
		for _, pat := range deniedHosts {
			if hostMatchesPattern(pat, parsed.Host) {
				return fmt.Errorf("git URL host %q is denied", parsed.Host)
			}
		}
		if len(allowedHosts) > 0 {
			allowed := false
			for _, pat := range allowedHosts {
				if hostMatchesPattern(pat, parsed.Host) {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("git URL host %q is not in allowed_hosts", parsed.Host)
			}
		}
	}

	if policy.DenyPrivateNetworks && len(allowedHosts) == 0 {
		if err := denyPrivateNetworks(ctx, policy, parsed); err != nil {
			return err
		}
	}

	return nil
}

func parseCloneURL(raw string, allowSCP bool) (parsedCloneURL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return parsedCloneURL{}, fmt.Errorf("git URL is empty")
	}

	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return parsedCloneURL{}, fmt.Errorf("invalid git URL: %w", err)
		}
		scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
		if scheme == "" {
			return parsedCloneURL{}, fmt.Errorf("git URL scheme is required")
		}
		hostPort := strings.TrimSpace(u.Host)
		host := strings.TrimSpace(hostnameFromHostPort(hostPort))
		if scheme != "file" && host == "" {
			return parsedCloneURL{}, fmt.Errorf("git URL host is required for scheme %q", scheme)
		}
		return parsedCloneURL{
			Scheme:   scheme,
			HostPort: hostPort,
			Host:     host,
		}, nil
	}

	if !allowSCP {
		return parsedCloneURL{}, fmt.Errorf("scp-style git URLs are not allowed")
	}

	// scp-style: [user@]host:org/repo(.git)
	colon := strings.Index(raw, ":")
	if colon <= 0 || colon >= len(raw)-1 {
		return parsedCloneURL{}, fmt.Errorf("invalid git URL")
	}
	hostPart := strings.TrimSpace(raw[:colon])
	pathPart := strings.TrimSpace(raw[colon+1:])
	if hostPart == "" || pathPart == "" {
		return parsedCloneURL{}, fmt.Errorf("invalid git URL")
	}
	if strings.ContainsAny(hostPart, "/\\") || strings.ContainsAny(raw, " \t\r\n") {
		return parsedCloneURL{}, fmt.Errorf("invalid git URL")
	}
	if idx := strings.LastIndex(hostPart, "@"); idx >= 0 {
		hostPart = hostPart[idx+1:]
	}
	host := strings.TrimSpace(hostnameFromHostPort(hostPart))
	if host == "" {
		return parsedCloneURL{}, fmt.Errorf("git URL host is required")
	}
	return parsedCloneURL{
		Scheme:   "ssh",
		HostPort: hostPart,
		Host:     host,
	}, nil
}

func hostnameFromHostPort(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.TrimSpace(host)
	}
	if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
		return strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
	}
	return hostport
}

func normalizeList(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}

func contains(list []string, value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, v := range list {
		if strings.EqualFold(v, value) {
			return true
		}
	}
	return false
}

func hostMatchesPattern(pattern string, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}

	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}

	pattern = hostnameFromHostPort(pattern)
	host = hostnameFromHostPort(host)
	if pattern == "" || host == "" {
		return false
	}

	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*")
		if !strings.HasSuffix(host, suffix) {
			return false
		}
		trimmed := strings.TrimPrefix(suffix, ".")
		return host != trimmed
	}

	if strings.HasPrefix(pattern, ".") {
		return strings.HasSuffix(host, pattern)
	}

	return host == pattern
}

func denyPrivateNetworks(ctx context.Context, policy ClonePolicy, parsed parsedCloneURL) error {
	host := strings.TrimSpace(parsed.Host)
	if host == "" {
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("git URL host %q is not allowed (private network)", host)
	}

	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("git URL host %q is not allowed (private network)", host)
		}
		return nil
	}

	if !policy.ResolveDNS {
		return nil
	}

	timeout := 2 * time.Second
	if policy.DNSResolveTimeoutSec > 0 {
		timeout = time.Duration(policy.DNSResolveTimeoutSec) * time.Second
	}
	resolveCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupIPAddr(resolveCtx, host)
	if err != nil {
		return fmt.Errorf("failed to resolve git host %q: %w", host, err)
	}
	for _, addr := range addrs {
		if isBlockedIP(addr.IP) {
			return fmt.Errorf("git URL host %q resolves to a private network address", host)
		}
	}
	return nil
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	// Treat non-global-unicast as blocked for cloning.
	return !ip.IsGlobalUnicast()
}
