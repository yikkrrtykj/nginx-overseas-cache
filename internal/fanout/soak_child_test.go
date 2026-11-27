package fanout

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type soakOS struct {
	CPUSeconds float64
	RSSBytes   uint64
	OpenFDs    *int
	Handles    *int
}

type soakQueue struct {
	Peer     uint64
	Messages int
	Bytes    int
}

type soakSample struct {
	At                                                                                    time.Time
	OS                                                                                    soakOS
	HeapAlloc, HeapInuse, HeapReleased                                                    uint64
	GCCount                                                                               uint32
	GCPauseNS                                                                             uint64
	Goroutines                                                                            int
	GlobalQueue, ReadingBudget, QueueBudget, PeerBudget, PeerLimit                        int
	Queue                                                                                 []soakQueue
	Status                                                                                Status
	UpMessages, DownMessages, DownBytes, WriteFailures, ProtocolErrors, AdmissionRejected uint64
}

func TestSoakRelayProcess(t *testing.T) {
	target := os.Getenv("SOAK_FIXTURE_ADDRESS")
	if target == "" {
		t.Skip("isolated soak starts this test process")
	}
	endpoint, err := netip.ParseAddrPort(target)
	if err != nil || !endpoint.Addr().IsLoopback() {
		t.Fatal("expected a loopback upstream")
	}
	cert, err := os.ReadFile(os.Getenv("SOAK_FIXTURE_CERT"))
	roots := x509.NewCertPool()
	if err != nil || !roots.AppendCertsFromPEM(cert) {
		t.Fatal("fixture certificate unavailable")
	}
	dialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second, EnableCompression: true,
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12},
		NetDialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, endpoint.String())
		},
	}
	// httptest's fixture cert is valid for loopback. Verify its loopback SAN
	// and never select a network destination from a renderer-supplied header.
	dialer.TLSClientConfig.ServerName = "127.0.0.1"
	relay, err := New(DefaultConfig(), dialer)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sample", func(w http.ResponseWriter, _ *http.Request) {
		stat, err := readSoakOS()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		snapshot := soakSample{
			At: time.Now().UTC(), OS: stat, HeapAlloc: mem.HeapAlloc, HeapInuse: mem.HeapInuse,
			HeapReleased: mem.HeapReleased, GCCount: mem.NumGC, GCPauseNS: mem.PauseTotalNs,
			Goroutines: runtime.NumGoroutine(), QueueBudget: relay.config.TotalQueueBytes,
			PeerBudget: relay.config.QueueBytes, PeerLimit: relay.config.MaxMessages,
			Queue: make([]soakQueue, 0),
		}
		relay.mu.Lock()
		snapshot.Status = relay.snapshot()
		snapshot.GlobalQueue, snapshot.ReadingBudget = relay.retainedQueue, relay.reading
		counters := relay.counters
		snapshot.UpMessages, snapshot.DownMessages, snapshot.DownBytes = counters.upMessages, counters.downMessages, counters.downBytes
		snapshot.WriteFailures, snapshot.ProtocolErrors, snapshot.AdmissionRejected = counters.writeFailures, counters.protocolErrors, counters.admissionRejected
		for client := range relay.clients {
			snapshot.Queue = append(snapshot.Queue, soakQueue{client.order, len(client.queue.packets), client.queue.bytes})
		}
		relay.mu.Unlock()
		json.NewEncoder(w).Encode(snapshot)
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
		select {
		case stop <- struct{}{}:
		default:
		}
	})
	mux.Handle("/", relay)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ready := os.Getenv("SOAK_RELAY_READY")
	if err := os.WriteFile(ready, []byte(listener.Addr().String()), 0600); err != nil {
		t.Fatal(err)
	}
	serving := make(chan error, 1)
	go func() { serving <- server.Serve(listener) }()
	select {
	case <-stop:
	case err := <-serving:
		t.Fatalf("relay listener exited: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := relay.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if len(relay.clients) != 0 || len(relay.hubs) != 0 || relay.retainedQueue != 0 || relay.reading != 0 {
		t.Fatal("shutdown left relay state allocated")
	}
	fmt.Println("isolated relay shutdown complete")
}
