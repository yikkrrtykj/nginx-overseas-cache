package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

type fixtureDNS []netip.Addr

func (r fixtureDNS) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) { return r, nil }

func TestPinAndRejectMixedAnswers(t *testing.T) {
	visited := ""
	public := netip.MustParseAddr("93.184.215.14")
	d := Dialer{
		Resolver: fixtureDNS{public, netip.MustParseAddr("::ffff:127.0.0.1")},
		DialIP: func(_ context.Context, network, addr string) (net.Conn, error) {
			visited = addr
			return nil, errors.New("fixture stops at dial")
		},
	}
	if _, err := d.DialContext(context.Background(), "tcp", "example.com:443"); err == nil || visited != "" {
		t.Fatal("mixed DNS answers reached TCP")
	}
	d.Resolver = fixtureDNS{public}
	d.DialContext(context.Background(), "tcp", "example.com:443")
	if visited != "93.184.215.14:443" {
		t.Fatalf("dial did not use a pinned numeric address: %q", visited)
	}
	for _, value := range []string{"192.0.2.1", "100.64.0.1", "169.254.169.254", "2001:db8::1", "fc00::1", "64:ff9b::1", "::ffff:10.0.0.1"} {
		if Public(netip.MustParseAddr(value)) {
			t.Fatalf("reserved answer accepted: %s", value)
		}
	}
}

func TestHostnameIDNA(t *testing.T) {
	host, err := Hostname("Bücher.Example.")
	if err != nil || host != "xn--bcher-kva.example" {
		t.Fatalf("IDNA conversion: %q, %v", host, err)
	}
	if _, err := Hostname("１２７。０。０。１"); err == nil {
		t.Fatal("IDNA-mapped address accepted")
	}
}
