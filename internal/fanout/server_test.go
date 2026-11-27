package fanout

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type originPeer struct {
	conn  *websocket.Conn
	trx   string
	token string
}

func fixture(t testing.TB, config Config) (*Server, string, <-chan originPeer, *atomic.Int32) {
	t.Helper()
	opened := make(chan originPeer, 512)
	var count atomic.Int32
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_, body, err := conn.ReadMessage()
		if err != nil {
			conn.Close()
			return
		}
		count.Add(1)
		opened <- originPeer{conn: conn, trx: scalar(parse(body).TrxID), token: r.URL.Query().Get("token")}
	}))
	t.Cleanup(origin.Close)
	destination, _ := url.Parse(origin.URL)
	roots := x509.NewCertPool()
	roots.AddCert(origin.Certificate())
	dialer := &websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: destination.Hostname(), MinVersion: tls.VersionTLS12},
		HandshakeTimeout: time.Second, NetDialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, destination.Host)
		}}
	s, err := New(config, dialer)
	if err != nil {
		t.Fatal(err)
	}
	local := httptest.NewServer(s)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
		local.Close()
	})
	return s, "ws" + strings.TrimPrefix(local.URL, "http") + "/ws", opened, &count
}

func join(t testing.TB, base, origin, token, channel, mode, trx string) *websocket.Conn {
	t.Helper()
	query := url.Values{"token": {token}}.Encode()
	headers := http.Header{"X-Fanout-Upstream-Base": {"wss://" + origin + "/ws"}, "X-Fanout-Upstream-Origin": {"https://" + origin}}
	conn, _, err := websocket.DefaultDialer.Dial(base+"?"+query, headers)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.WriteJSON(map[string]any{"action": "join", "trxId": trx, "data": map[string]string{"channelId": channel, "rendererMode": mode}}); err != nil {
		t.Fatal(err)
	}
	return conn
}

func upstream(t testing.TB, opened <-chan originPeer) originPeer {
	t.Helper()
	select {
	case peer := <-opened:
		t.Cleanup(func() { peer.conn.Close() })
		return peer
	case <-time.After(4 * time.Second):
		t.Fatal("missing origin connection")
	}
	return originPeer{}
}
func (p originPeer) ack(t testing.TB) {
	t.Helper()
	if err := p.conn.WriteJSON(map[string]any{"action": "join_res", "trxId": p.trx}); err != nil {
		t.Fatal(err)
	}
}
func receive(t testing.TB, c *websocket.Conn) (int, []byte) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(4 * time.Second))
	kind, body, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	return kind, body
}
func expectAck(t testing.TB, c *websocket.Conn, trx string) {
	t.Helper()
	_, body := receive(t, c)
	m := parse(body)
	if m.Action != "join_res" || scalar(m.TrxID) != trx {
		t.Fatalf("wrong ack: %s", body)
	}
}
func eventually(t testing.TB, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSharedJoinOrderedReplayReconnectionAndHandoff(t *testing.T) {
	config := DefaultConfig()
	config.ReconnectMin, config.ReconnectMax = 20*time.Millisecond, 100*time.Millisecond
	s, base, opened, count := fixture(t, config)
	const key = "synthetic-token-01"
	first := join(t, base, "origin.example", key, "channel", "onair", "first")
	p := upstream(t, opened)
	p.ack(t)
	expectAck(t, first, "first")
	live := []byte("{\"action\":\"call\",\"data\":[42]}")
	if err := p.conn.WriteMessage(websocket.BinaryMessage, live); err != nil {
		t.Fatal(err)
	}
	if kind, got := receive(t, first); kind != websocket.BinaryMessage || string(got) != string(live) {
		t.Fatal("bad live call")
	}
	second := join(t, base, "origin.example", key, "channel", "onair", "second")
	expectAck(t, second, "second")
	if kind, got := receive(t, second); kind != websocket.BinaryMessage || string(got) != string(live) {
		t.Fatal("bad replay")
	}
	if count.Load() != 1 {
		t.Fatal("shared clients created extra upstream")
	}
	for _, c := range []*websocket.Conn{first, second} {
		if err := c.WriteJSON(map[string]string{"action": "call_res", "trxId": "once"}); err != nil {
			t.Fatal(err)
		}
	}
	_, payload := receive(t, p.conn)
	if scalar(parse(payload).TrxID) != "once" {
		t.Fatal("wrong call response")
	}
	// Wait until both clients' reads and writes have drained before the fault.
	eventually(t, func() bool { return s.Snapshot().AcknowledgedClients == 2 })
	first.Close()
	eventually(t, func() bool { return s.Snapshot().LocalClients == 1 })
	if err := second.WriteMessage(websocket.BinaryMessage, []byte{0x00, 0xff}); err != nil {
		t.Fatal(err)
	}
	if kind, body := receive(t, p.conn); kind != websocket.BinaryMessage || string(body) != "\x00\xff" {
		t.Fatalf("leader handoff or duplicate response: %v", body)
	}
	p.conn.Close()
	reconnected := upstream(t, opened)
	third := join(t, base, "origin.example", key, "channel", "onair", "third")
	reconnected.ack(t)
	expectAck(t, second, "second")
	expectAck(t, third, "third")
	fresh := []byte("{\"action\":\"call\",\"data\":[43]}")
	if err := reconnected.conn.WriteMessage(websocket.TextMessage, fresh); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*websocket.Conn{second, third} {
		if kind, got := receive(t, c); kind != websocket.TextMessage || string(got) != string(fresh) {
			t.Fatal("stale state crosses reconnect")
		}
	}
	if err := second.WriteJSON(map[string]string{"action": "call_res", "trxId": "once"}); err != nil {
		t.Fatal(err)
	}
	if _, body := receive(t, reconnected.conn); scalar(parse(body).TrxID) != "once" {
		t.Fatal("dedup carried across generations")
	}
	data, _ := json.Marshal(s.Snapshot())
	if strings.Contains(string(data), key) {
		t.Fatal("token leaked to status")
	}
	second.Close()
	third.Close()
	eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.hubs) == 0 && len(s.clients) == 0 && s.retainedQueue == 0
	})
}

func TestIdentityAndOversize(t *testing.T) {
	config := DefaultConfig()
	config.MaxMessageBytes = 4096
	s, base, opened, count := fixture(t, config)
	groups := [][4]string{
		{"origin.example", "test-token-1", "a", "onair"},
		{"origin.example", "test-token-2", "a", "onair"},
		{"origin.example", "test-token-1", "b", "onair"},
		{"origin.example", "test-token-1", "a", "preview"},
		{"origin2.example", "test-token-1", "a", "onair"},
	}
	var clients []*websocket.Conn
	for id, row := range groups {
		trx := strconv.Itoa(id + 1)
		c := join(t, base, row[0], row[1], row[2], row[3], trx)
		p := upstream(t, opened)
		if p.token != row[1] {
			t.Fatal("wrong outbound token")
		}
		p.ack(t)
		expectAck(t, c, trx)
		clients = append(clients, c)
	}
	if count.Load() != int32(len(groups)) || s.Snapshot().ActiveHubs != len(groups) {
		t.Fatal("groups were merged")
	}
	c := clients[0]
	if err := c.WriteMessage(websocket.BinaryMessage, make([]byte, 4097)); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := c.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Fatalf("expected close 1009: %v", err)
	}
	eventually(t, func() bool { return s.Snapshot().ActiveHubs == len(groups)-1 })
}

func TestSlowClientIsolated(t *testing.T) {
	config := DefaultConfig()
	config.MaxMessages = 4
	config.MaxMessageBytes = 128 << 10
	config.QueueBytes = 384 << 10
	config.WriteTimeout = 3 * time.Second
	s, base, opened, _ := fixture(t, config)
	fast := join(t, base, "origin.example", "synthetic-token", "a", "onair", "fast")
	p := upstream(t, opened)
	p.ack(t)
	expectAck(t, fast, "fast")
	slow := join(t, base, "origin.example", "synthetic-token", "a", "onair", "slow")
	expectAck(t, slow, "slow")
	// Shrink only the slow peer's kernel queue to make the test deterministic.
	s.mu.Lock()
	for c := range s.clients {
		if c.trx == "slow" {
			if conn, ok := c.conn.UnderlyingConn().(*net.TCPConn); ok {
				conn.SetWriteBuffer(1024)
			}
		}
	}
	s.mu.Unlock()
	if conn, ok := slow.UnderlyingConn().(*net.TCPConn); ok {
		conn.SetReadBuffer(1024)
	}
	payload := make([]byte, 100<<10)
	for seq := 0; seq < 45; seq++ {
		payload[0] = byte(seq)
		if err := p.conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
			t.Fatal(err)
		}
		kind, body := receive(t, fast)
		if kind != websocket.BinaryMessage || body[0] != byte(seq) || len(body) != len(payload) {
			t.Fatal("fast stream order or length changed")
		}
		if s.Snapshot().SlowDisconnects > 0 {
			break
		}
	}
	eventually(t, func() bool { return s.Snapshot().LocalClients == 1 && s.Snapshot().SlowDisconnects == 1 })
}

func BenchmarkFanout(b *testing.B) {
	for _, clients := range []int{1, 10, 30, 100, 300} {
		b.Run(fmt.Sprintf("clients=%d", clients), func(b *testing.B) {
			s, base, opened, count := fixture(b, DefaultConfig())
			connections := make([]*websocket.Conn, 0, clients)
			for i := 0; i < clients; i++ {
				trx := strconv.Itoa(i + 1)
				connections = append(connections, join(b, base, "origin.example", "benchmark-token", "benchmark", "onair", trx))
			}
			p := upstream(b, opened)
			p.ack(b)
			for i, conn := range connections {
				expectAck(b, conn, strconv.Itoa(i+1))
			}
			message := make([]byte, 1024)
			b.ReportAllocs()
			b.ResetTimer()
			for sequence := 0; sequence < b.N; sequence++ {
				message[0] = byte(sequence)
				if err := p.conn.WriteMessage(websocket.BinaryMessage, message); err != nil {
					b.Fatal(err)
				}
				for _, conn := range connections {
					_, payload := receive(b, conn)
					if len(payload) != len(message) || payload[0] != byte(sequence) {
						b.Fatal("corrupt delivery")
					}
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(runtime.NumGoroutine()), "goroutines")
			b.ReportMetric(float64(count.Load()), "upstream")
			if s.Snapshot().LocalClients != clients {
				b.Fatal("client count changed")
			}
		})
	}
}
