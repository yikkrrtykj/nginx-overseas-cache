package fanout

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// This entry point is only present in the separately built Go test executable.
func TestBenchmarkEndpoint(t *testing.T) {
	target := os.Getenv("OVERSEAS_BENCH_LOOPBACK")
	if target == "" {
		t.Skip("standalone benchmark owns this fixture")
	}
	endpoint, err := netip.ParseAddrPort(target)
	if err != nil || !endpoint.Addr().IsLoopback() {
		t.Fatal("fixture origin must bind loopback")
	}
	host := os.Getenv("OVERSEAS_BENCH_HOSTNAME")
	ca, err := os.ReadFile(os.Getenv("SSL_CERT_FILE"))
	roots := x509.NewCertPool()
	if err != nil || !roots.AppendCertsFromPEM(ca) || host == "" {
		t.Fatal("missing synthetic TLS certificate")
	}
	dialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second, EnableCompression: true,
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12},
		NetDialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, endpoint.String())
		},
	}
	s, err := New(DefaultConfig(), dialer)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", os.Getenv("LISTEN_PORT")))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	path := os.Getenv("STATUS_FILE")
	if err := s.Publish(path); err != nil {
		t.Fatal(err)
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if s.Publish(path) != nil {
				return
			}
		}
	}()
	if err := http.Serve(listener, s); err != nil {
		t.Fatal(err)
	}
}
