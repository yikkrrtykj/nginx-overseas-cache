package fanout

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type syntheticOrigin struct {
	mu         sync.Mutex
	active     map[int]*websocket.Conn
	epochs     map[int]int
	duplicates int
}

func (o *syntheticOrigin) accept(w http.ResponseWriter, r *http.Request) {
	grp, err := strconv.Atoi(strings.TrimPrefix(r.URL.Query().Get("token"), "isolated-token-"))
	if err != nil {
		http.Error(w, "invalid fixture group", 400)
		return
	}
	u := websocket.Upgrader{CheckOrigin: func(req *http.Request) bool { return req.Header.Get("Origin") == "https://origin.example" }}
	conn, err := u.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	_, input, err := conn.ReadMessage()
	if err != nil {
		return
	}
	msg := parse(input)
	if msg.Action != "join" || scalar(msg.joinData().Channel) != strconv.Itoa(grp) {
		return
	}
	o.mu.Lock()
	if o.active[grp] != nil {
		o.duplicates++
		o.mu.Unlock()
		return
	}
	o.epochs[grp]++
	epoch := o.epochs[grp]
	o.active[grp] = conn
	o.mu.Unlock()
	defer func() { o.mu.Lock(); delete(o.active, grp); o.mu.Unlock() }()
	if err := conn.WriteJSON(map[string]any{"action": "join_res", "trxId": scalar(msg.TrxID), "labEpoch": epoch}); err != nil {
		return
	}
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (o *syntheticOrigin) view() (map[int]int, int, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	epochs := make(map[int]int, len(o.epochs))
	for k, v := range o.epochs {
		epochs[k] = v
	}
	return epochs, len(o.active), o.duplicates
}

func (o *syntheticOrigin) peer(group int) *websocket.Conn {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.active[group]
}

type soakRun struct {
	t                        *testing.T
	endpoint                 string
	origin                   *syntheticOrigin
	peers                    [][]*websocket.Conn
	sequence                 uint64
	delivered                uint64
	expected                 uint64
	samples                  *json.Encoder
	readback                 *http.Client
	start                    time.Time
	last                     time.Time
	data                     []time.Duration
	all                      []time.Duration
	faults                   int
	churn                    int
	large                    int
	slow                     int
	expectedFaultDisconnects int
}

func (r *soakRun) join(group int, slow bool) *websocket.Conn {
	r.t.Helper()
	trx := strconv.FormatUint(r.sequence, 10)
	r.sequence++
	query := url.Values{"token": {"isolated-token-" + strconv.Itoa(group)}}
	dial := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	if slow {
		dial.NetDialContext = func(_ context.Context, _, addr string) (net.Conn, error) {
			conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err == nil {
				if err := conn.(*net.TCPConn).SetReadBuffer(1024); err != nil {
					conn.Close()
					return nil, err
				}
			}
			return conn, err
		}
	}
	conn, _, err := dial.Dial("ws://"+r.endpoint+"/ws?"+query.Encode(), http.Header{
		"X-Fanout-Upstream-Base": {"wss://origin.example/ws"}, "X-Fanout-Upstream-Origin": {"https://origin.example"},
	})
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { conn.Close() })
	if err := conn.WriteJSON(map[string]any{"action": "join", "trxId": trx, "data": map[string]any{"channelId": strconv.Itoa(group), "rendererMode": "onair"}}); err != nil {
		r.t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	var ack struct {
		Action, TrxID string
		LabEpoch      int
	}
	if err := conn.ReadJSON(&ack); err != nil {
		r.t.Fatal(err)
	}
	epochs, _, _ := r.origin.view()
	if ack.Action != "join_res" || ack.TrxID != trx || ack.LabEpoch != epochs[group] || ack.LabEpoch < 1 {
		r.t.Fatalf("bad acknowledgement: %+v", ack)
	}
	return conn
}

func (r *soakRun) send(group, kind int, payload []byte) {
	r.t.Helper()
	upstream := r.origin.peer(group)
	if upstream == nil {
		r.t.Fatalf("missing upstream for group %d", group)
	}
	started := time.Now()
	upstream.SetWriteDeadline(started.Add(6 * time.Second))
	if err := upstream.WriteMessage(kind, payload); err != nil {
		r.t.Fatal(err)
	}
	type delivery struct {
		delay time.Duration
		err   error
	}
	received := make(chan delivery, len(r.peers[group]))
	r.expected += uint64(len(r.peers[group]))
	for _, peer := range r.peers[group] {
		go func() {
			peer.SetReadDeadline(started.Add(6 * time.Second))
			frame, body, err := peer.ReadMessage()
			if err == nil && (frame != kind || !bytes.Equal(body, payload)) {
				err = fmt.Errorf("unexpected frame type or bytes (length=%d)", len(body))
			}
			received <- delivery{time.Since(started), err}
		}()
	}
	var firstError error
	for range r.peers[group] {
		item := <-received
		if item.err != nil {
			firstError = item.err
			continue
		}
		r.data = append(r.data, item.delay)
		r.all = append(r.all, item.delay)
		r.delivered++
	}
	if firstError != nil {
		r.t.Fatalf("healthy peer group %d: %v", group, firstError)
	}
}

func (r *soakRun) binary(group, size int) {
	payload := bytes.Repeat([]byte{0x49}, size)
	binary.BigEndian.PutUint64(payload, r.sequence)
	r.sequence++
	r.send(group, websocket.BinaryMessage, payload)
	if size == 256<<10 {
		r.large++
	}
}

func quantiles(a []time.Duration) []float64 {
	if len(a) == 0 {
		return nil
	}
	slices.Sort(a)
	out := make([]float64, 0, 3)
	for _, p := range []int{50, 95, 99} {
		n := (len(a)*p + 99) / 100
		out = append(out, float64(a[n-1])/float64(time.Millisecond))
	}
	return out
}

func (r *soakRun) snapshot() soakSample {
	r.t.Helper()
	response, err := r.readback.Get("http://" + r.endpoint + "/sample")
	if err != nil {
		r.t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		data, _ := io.ReadAll(response.Body)
		r.t.Fatalf("resource sample %d: %s", response.StatusCode, data)
	}
	var out soakSample
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		r.t.Fatal(err)
	}
	if out.QueueBudget <= 0 || out.GlobalQueue < 0 || out.GlobalQueue > out.QueueBudget {
		r.t.Fatalf("global queue budget breach: %d/%d", out.GlobalQueue, out.QueueBudget)
	}
	for _, q := range out.Queue {
		if q.Messages > out.PeerLimit || q.Bytes < 0 || q.Bytes > out.PeerBudget {
			r.t.Fatalf("peer queue budget breach: %+v", q)
		}
	}
	return out
}

func (r *soakRun) sample(phase string) soakSample {
	r.t.Helper()
	item := r.snapshot()
	now := time.Now()
	epochs, active, duplicates := r.origin.view()
	window := now.Sub(r.last).Seconds()
	row := map[string]any{"phase": phase, "elapsedSeconds": now.Sub(r.start).Seconds(), "windowSeconds": window,
		"verifiedWindow": len(r.data), "verifiedTotal": r.delivered, "expectedTotal": r.expected,
		"latencyP50P95P99Ms": quantiles(r.data), "fixtureActive": active, "fixtureEpochs": epochs, "duplicateUpstreams": duplicates,
		"reconnectEvents": r.faults, "expectedFaultDisconnects": r.expectedFaultDisconnects, "churnCycles": r.churn,
		"largeFrames": r.large, "slowEvents": r.slow, "trace": item}
	if err := r.samples.Encode(row); err != nil {
		r.t.Fatal(err)
	}
	r.data = r.data[:0]
	r.last = now
	if duplicates != 0 {
		r.t.Fatalf("duplicate upstream attempts: %d", duplicates)
	}
	return item
}

func (r *soakRun) settled(clients, groups int) {
	r.t.Helper()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		v := r.snapshot()
		_, active, duplicates := r.origin.view()
		if duplicates > 0 {
			r.t.Fatalf("duplicate upstream attempts: %d", duplicates)
		}
		if v.Status.LocalClients == clients && v.Status.AcknowledgedClients == clients &&
			v.Status.ActiveHubs == groups && v.Status.UpstreamConnections == groups && active == groups &&
			len(v.Queue) == clients {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	r.t.Fatalf("did not settle at %d clients and %d groups", clients, groups)
}

func (r *soakRun) reconnect(group int) {
	r.t.Helper()
	before, _, _ := r.origin.view()
	stale := []byte(fmt.Sprintf("{\"action\":\"call\",\"labStale\":%d}", r.sequence))
	r.sequence++
	r.send(group, websocket.TextMessage, stale)
	if err := r.origin.peer(group).Close(); err != nil {
		r.t.Fatal(err)
	}
	finish := make(chan error, len(r.peers[group]))
	for _, peer := range r.peers[group] {
		go func() {
			peer.SetReadDeadline(time.Now().Add(6 * time.Second))
			_, _, err := peer.ReadMessage()
			if err == nil {
				finish <- fmt.Errorf("old generation delivered another frame")
				return
			}
			if e, ok := err.(net.Error); ok && e.Timeout() {
				finish <- fmt.Errorf("old generation socket remained open")
				return
			}
			peer.Close()
			finish <- nil
		}()
	}
	for range r.peers[group] {
		if err := <-finish; err != nil {
			r.t.Fatal(err)
		}
	}
	r.expectedFaultDisconnects += len(r.peers[group])
	for i := range r.peers[group] {
		r.peers[group][i] = r.join(group, false)
	}
	after, _, _ := r.origin.view()
	if after[group] != before[group]+1 {
		r.t.Fatalf("unexpected upstream epochs: %d -> %d", before[group], after[group])
	}
	r.faults++
	r.binary(group, 16<<10)
}

func (r *soakRun) slowPeer(group, clients int) {
	r.t.Helper()
	peer := r.join(group, true)
	defer peer.Close()
	initial := r.sample("slow-joined").Status.SlowDisconnects
	for i := 0; i < 96; i++ {
		r.binary(group, 256<<10)
		if len(r.peers) > 1 {
			r.binary((group+1)%len(r.peers), 16<<10)
		}
		v := r.snapshot()
		if v.Status.SlowDisconnects > initial {
			if v.Status.SlowDisconnects != initial+1 {
				r.t.Fatal("more than the intended slow peer was disconnected")
			}
			r.slow++
			r.settled(clients, len(r.peers))
			r.sample("slow-eviction")
			return
		}
	}
	r.t.Fatal("slow socket did not exercise queue-overflow isolation")
}

func TestIsolatedHourlySoak(t *testing.T) {
	dir := os.Getenv("SOAK_OUTPUT")
	if dir == "" {
		t.Skip("set SOAK_OUTPUT to run the isolated integration soak")
	}
	n := func(key string) int {
		i, err := strconv.Atoi(os.Getenv(key))
		if err != nil || i <= 0 {
			t.Fatalf("invalid %s", key)
		}
		return i
	}
	clients, groups, seconds := n("SOAK_CLIENTS"), n("SOAK_GROUPS"), n("SOAK_SECONDS")
	sampleSeconds, eventSeconds := n("SOAK_SAMPLE_SECONDS"), n("SOAK_EVENT_SECONDS")
	if clients < 16 || clients > 300 || groups > 5 || clients%groups != 0 {
		t.Fatal("unexpected fixture dimensions")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.Create(filepath.Join(dir, "samples.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	log, err := os.Create(filepath.Join(dir, "relay.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	origin := &syntheticOrigin{active: make(map[int]*websocket.Conn), epochs: make(map[int]int)}
	fixture := httptest.NewTLSServer(http.HandlerFunc(origin.accept))
	defer fixture.Close()
	tmp := t.TempDir()
	cert := filepath.Join(tmp, "fixture.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(tmp, "listener")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSoakRelayProcess$", "-test.v", "-test.timeout=0")
	cmd.Env = append(os.Environ(), "SOAK_FIXTURE_ADDRESS="+strings.TrimPrefix(fixture.URL, "https://"), "SOAK_FIXTURE_CERT="+cert, "SOAK_RELAY_READY="+ready)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waiting := make(chan error, 1)
	go func() { waiting <- cmd.Wait() }()
	defer func() { cmd.Process.Kill() }()
	var endpoint string
	for until := time.Now().Add(10 * time.Second); time.Now().Before(until); {
		b, err := os.ReadFile(ready)
		if err == nil {
			endpoint = string(b)
			break
		}
		select {
		case err := <-waiting:
			t.Fatalf("relay fixture exited: %v", err)
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	if endpoint == "" {
		t.Fatal("relay readiness timed out")
	}
	now := time.Now()
	r := soakRun{t: t, endpoint: endpoint, origin: origin, peers: make([][]*websocket.Conn, groups),
		samples: json.NewEncoder(raw), readback: &http.Client{Timeout: 4 * time.Second}, start: now, last: now}
	idle := r.sample("idle")
	for i := range clients {
		g := i % groups
		r.peers[g] = append(r.peers[g], r.join(g, false))
	}
	r.settled(clients, groups)
	r.sample("joined")
	workloadStart := time.Now()
	r.start = workloadStart
	r.last = workloadStart
	deadline := workloadStart.Add(time.Duration(seconds) * time.Second)
	nextFrame := workloadStart
	nextSample := workloadStart.Add(time.Duration(sampleSeconds) * time.Second)
	nextEvent := workloadStart.Add(time.Duration(eventSeconds) * time.Second)
	event, messages := 0, 0
	for time.Now().Before(deadline) {
		r.binary(messages%groups, 16<<10)
		messages++
		if !time.Now().Before(nextEvent) {
			group := event % groups
			// Do not interleave a deliberate topology change with an unverified frame.
			r.sample("before-event")
			r.reconnect(group)
			r.peers[group][0].Close()
			r.settled(clients-1, groups)
			r.peers[group][0] = r.join(group, false)
			r.churn++
			r.settled(clients, groups)
			r.binary(group, 256<<10)
			if event == 0 || event%4 == 0 {
				r.slowPeer(group, clients)
			}
			r.sample("after-event")
			event++
			nextEvent = time.Now().Add(time.Duration(eventSeconds) * time.Second)
		}
		if !time.Now().Before(nextSample) {
			v := r.sample("steady")
			if v.Status.LocalClients != clients || v.Status.UpstreamConnections != groups || v.Status.ActiveHubs != groups {
				t.Fatal("unexpected steady connection count")
			}
			nextSample = time.Now().Add(time.Duration(sampleSeconds) * time.Second)
		}
		nextFrame = nextFrame.Add(100 * time.Millisecond)
		if nextFrame.Before(time.Now()) {
			nextFrame = time.Now()
		}
		time.Sleep(time.Until(nextFrame))
	}
	final := r.sample("end-loaded")
	elapsed := time.Since(workloadStart).Seconds()
	if r.expected != r.delivered || r.faults < 1 || r.churn < 1 || r.large < 1 || r.slow < 1 ||
		final.Status.SlowDisconnects != uint64(r.slow) || final.AdmissionRejected != idle.AdmissionRejected || final.ProtocolErrors != idle.ProtocolErrors {
		t.Fatal("delivery or fault counters inconsistent")
	}
	for _, group := range r.peers {
		for _, peer := range group {
			peer.Close()
		}
	}
	r.settled(0, 0)
	time.Sleep(300 * time.Millisecond)
	released := r.sample("released")
	if released.GlobalQueue != 0 || released.ReadingBudget != 0 || released.Goroutines > idle.Goroutines+2 ||
		(idle.OS.OpenFDs != nil && released.OS.OpenFDs != nil && *released.OS.OpenFDs > *idle.OS.OpenFDs+2) {
		t.Fatalf("retained resources after final disconnect: queue=%d read=%d goroutines=%d->%d", released.GlobalQueue, released.ReadingBudget, idle.Goroutines, released.Goroutines)
	}
	response, err := r.readback.Post("http://"+endpoint+"/stop", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal("relay shutdown refused")
	}
	select {
	case err := <-waiting:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("relay exit timed out")
	}
	summary := map[string]any{"main": os.Getenv("SOAK_MAIN"), "tree": os.Getenv("SOAK_TREE"), "platform": runtime.GOOS + "/" + runtime.GOARCH,
		"go": runtime.Version(), "clients": clients, "groups": groups, "targetSeconds": seconds, "elapsedSeconds": elapsed,
		"steadyMessages": messages, "steadyRateTargetHz": 10, "verifiedDeliveries": r.delivered, "expectedDeliveries": r.expected,
		"deliveryRatePerSecond": float64(r.delivered) / elapsed, "queueBudgetBytes": final.QueueBudget,
		"latencyP50P95P99Ms": quantiles(r.all), "reconnectEvents": r.faults, "expectedFaultDisconnects": r.expectedFaultDisconnects,
		"churnCycles": r.churn, "largeFrames": r.large, "slowEvents": r.slow,
		"idle": idle, "endLoaded": final, "released": released, "cleanExit": true}
	b, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("verified %d/%d deliveries, %.1fs, faults=%d, churn=%d, 256KiB frames=%d, slow evictions=%d", r.delivered, r.expected, elapsed, r.faults, r.churn, r.large, r.slow)
}
