package fanout

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"time"

	"github.com/yikkrrtykj/nginx-overseas-cache/internal/atomicfile"
)

var latencyBounds = [...]time.Duration{
	100 * time.Microsecond, 500 * time.Microsecond, time.Millisecond,
	5 * time.Millisecond, 10 * time.Millisecond, 50 * time.Millisecond, 250 * time.Millisecond,
}

type counters struct {
	admissionRejected, queueOverflow, slowDisconnects, reconnects, protocolErrors, writeFailures uint64
	upMessages, upBytes, downMessages, downBytes                                                 uint64
	observations                                                                                 uint64
	latency                                                                                      time.Duration
	buckets                                                                                      [len(latencyBounds)]uint64
}

func (c *counters) observe(elapsed time.Duration) {
	c.observations++
	c.latency += elapsed
	for i, bound := range latencyBounds {
		if elapsed <= bound {
			c.buckets[i]++
		}
	}
}

type GroupStatus struct {
	Origin              string `json:"origin"`
	TokenID             string `json:"tokenId"`
	Channel             string `json:"channel"`
	Mode                string `json:"mode"`
	LocalClients        int    `json:"localClients"`
	AcknowledgedClients int    `json:"acknowledgedClients"`
	UpstreamConnected   bool   `json:"upstreamConnected"`
	UpstreamReady       bool   `json:"upstreamReady"`
	Reconnects          uint64 `json:"reconnects"`
	Generation          uint64 `json:"generation"`
	ConnectedSeconds    int64  `json:"connectedSeconds"`
	QueuedMessages      int    `json:"queuedMessages"`
}

type Status struct {
	Version                  int           `json:"version"`
	GeneratedAt              time.Time     `json:"generatedAt"`
	UptimeSeconds            int64         `json:"uptimeSeconds"`
	LocalClients             int           `json:"localClients"`
	AcknowledgedClients      int           `json:"acknowledgedClients"`
	ActiveHubs               int           `json:"activeHubs"`
	UpstreamConnections      int           `json:"upstreamConnections"`
	SavedUpstreamConnections int           `json:"savedUpstreamConnections"`
	Reconnects               uint64        `json:"reconnects"`
	SlowDisconnects          uint64        `json:"slowDisconnects"`
	QueueOverflows           uint64        `json:"queueOverflows"`
	Goroutines               int           `json:"goroutines"`
	Groups                   []GroupStatus `json:"groups"`
}

func (s *Server) Snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot()
}

func (s *Server) snapshot() Status {
	now := time.Now()
	status := Status{
		Version: 2, GeneratedAt: now.UTC(), UptimeSeconds: int64(now.Sub(s.started) / time.Second),
		Groups: make([]GroupStatus, 0, len(s.hubs)), Goroutines: runtime.NumGoroutine(),
		Reconnects: s.counters.reconnects, SlowDisconnects: s.counters.slowDisconnects, QueueOverflows: s.counters.queueOverflow,
	}
	for _, h := range s.hubs {
		if h.ctx.Err() != nil {
			continue
		}
		mac := hmac.New(sha256.New, s.salt[:])
		mac.Write(h.info.key.token[:])
		id := hex.EncodeToString(mac.Sum(nil)[:6])
		channel, mode := redactIdentity(h.info)
		item := GroupStatus{
			Origin: h.info.host, TokenID: id, Channel: channel, Mode: mode,
			UpstreamConnected: h.upstream != nil, UpstreamReady: len(h.ack) > 0,
			Generation: h.generation, Reconnects: h.reconnects,
		}
		if item.UpstreamConnected {
			status.UpstreamConnections++
			item.ConnectedSeconds = int64(now.Sub(h.connectedAt) / time.Second)
		}
		for c := range h.clients {
			if c.ctx.Err() != nil {
				continue
			}
			item.LocalClients++
			item.QueuedMessages += len(c.queue.packets)
			if c.ackSent {
				item.AcknowledgedClients++
			}
		}
		status.LocalClients += item.LocalClients
		status.AcknowledgedClients += item.AcknowledgedClients
		if item.UpstreamReady {
			status.SavedUpstreamConnections += max(0, item.AcknowledgedClients-1)
		}
		status.Groups = append(status.Groups, item)
	}
	status.ActiveHubs = len(status.Groups)
	sort.Slice(status.Groups, func(i, j int) bool {
		a, b := status.Groups[i], status.Groups[j]
		if a.Origin != b.Origin {
			return a.Origin < b.Origin
		}
		if a.Channel != b.Channel {
			return a.Channel < b.Channel
		}
		if a.Mode != b.Mode {
			return a.Mode < b.Mode
		}
		return a.TokenID < b.TokenID
	})
	return status
}

func (s *Server) Publish(path string) error {
	data, err := json.Marshal(s.Snapshot())
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0644)
}

func (s *Server) metrics(w http.ResponseWriter) {
	s.mu.Lock()
	data, c, queue, connections := s.snapshot(), s.counters, s.retainedQueue, len(s.clients)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	for _, item := range []struct {
		name, kind string
		value      any
	}{
		{"downstream_connections", "gauge", connections},
		{"joined_clients", "gauge", data.LocalClients},
		{"upstream_connections", "gauge", data.UpstreamConnections},
		{"active_groups", "gauge", data.ActiveHubs},
		{"upstream_reconnects_total", "counter", c.reconnects},
		{"upstream_received_messages_total", "counter", c.upMessages},
		{"upstream_received_bytes_total", "counter", c.upBytes},
		{"downstream_sent_messages_total", "counter", c.downMessages},
		{"downstream_sent_bytes_total", "counter", c.downBytes},
		{"queued_bytes", "gauge", queue},
		{"queue_overflows_total", "counter", c.queueOverflow},
		{"slow_client_disconnects_total", "counter", c.slowDisconnects},
		{"admission_rejected_total", "counter", c.admissionRejected},
		{"protocol_errors_total", "counter", c.protocolErrors},
		{"downstream_write_failures_total", "counter", c.writeFailures},
		{"snapshot_timestamp_seconds", "gauge", data.GeneratedAt.Unix()},
	} {
		fmt.Fprintf(w, "# TYPE overseas_fanout_%s %s\noverseas_fanout_%s %v\n", item.name, item.kind, item.name, item.value)
	}
	fmt.Fprintf(w, "# TYPE go_goroutines gauge\ngo_goroutines %d\n", data.Goroutines)
	const name = "overseas_fanout_local_delivery_seconds"
	fmt.Fprintf(w, "# TYPE %s histogram\n", name)
	for i, bound := range latencyBounds {
		fmt.Fprintf(w, "%s_bucket{le=\"%g\"} %d\n", name, bound.Seconds(), c.buckets[i])
	}
	fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n%s_sum %g\n%s_count %d\n", name, c.observations, name, c.latency.Seconds(), name, c.observations)
}
