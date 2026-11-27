// Package netguard validates and pins HTTPS/WSS connections to public DNS answers.
package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

var hostnamePattern = regexp.MustCompile("^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$")

func Hostname(value string) (string, error) {
	ascii, err := idna.Lookup.ToASCII(strings.TrimSpace(value))
	if err != nil {
		return "", errors.New("expected a DNS hostname")
	}
	host := strings.TrimSuffix(strings.ToLower(ascii), ".")
	if _, err := netip.ParseAddr(host); err == nil || len(host) > 253 || !hostnamePattern.MatchString(host) {
		return "", errors.New("expected a DNS hostname")
	}
	return host, nil
}

func URL(value, scheme string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != scheme || u.User != nil || u.Opaque != "" ||
		(u.Port() != "" && u.Port() != "443") || strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("expected a standard TLS destination")
	}
	host, err := Hostname(u.Hostname())
	if err != nil {
		return nil, err
	}
	u.Host = host
	return u, nil
}

func prefixes(values ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		out = append(out, netip.MustParsePrefix(value))
	}
	return out
}

var (
	globalV6 = netip.MustParsePrefix("2000::/3")
	special  = prefixes(
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
		"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "224.0.0.0/3",
		"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
	)
)

func Public(address netip.Addr) bool {
	addr := address.Unmap()
	if !addr.IsGlobalUnicast() || addr.Zone() != "" || (addr.Is6() && !globalV6.Contains(addr)) {
		return false
	}
	for _, block := range special {
		if block.Contains(addr) {
			return false
		}
	}
	return true
}

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Dialer struct {
	Resolver Resolver
	DialIP   func(context.Context, string, string) (net.Conn, error)
}

// DialContext resolves once, validates every answer, and passes only numeric
// addresses to the TCP dialer. TLS still authenticates the original name.
func (d Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" || network != "tcp" {
		return nil, errors.New("unexpected outbound destination")
	}
	host, err = Hostname(host)
	if err != nil {
		return nil, err
	}
	resolver := d.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	answers, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(answers) == 0 || len(answers) > 32 {
		return nil, errors.New("unable to resolve public upstream")
	}
	for _, ip := range answers {
		if !Public(ip) {
			return nil, errors.New("upstream resolved to a blocked address")
		}
	}
	dial := d.DialIP
	if dial == nil {
		dial = (&net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	for _, ip := range answers {
		conn, err := dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.New("unable to connect to public upstream")
}
