// Package fanout shares one renderer WebSocket per validated join identity.
package fanout

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/yikkrrtykj/nginx-overseas-cache/internal/netguard"
)

type Config struct {
	MaxClients, MaxGroups, MaxMessages                   int
	MaxMessageBytes, QueueBytes, TotalQueueBytes         int
	JoinTimeout, WriteTimeout, IdleTimeout, PingInterval time.Duration
	ReconnectMin, ReconnectMax                           time.Duration
	UpstreamBase, UpstreamOrigin                         string
}

func DefaultConfig() Config {
	return Config{
		MaxClients: 512, MaxGroups: 64, MaxMessages: 32,
		MaxMessageBytes: 1 << 20, QueueBytes: 2 << 20, TotalQueueBytes: 64 << 20,
		JoinTimeout: 15 * time.Second, WriteTimeout: 5 * time.Second,
		IdleTimeout: 60 * time.Second, PingInterval: 20 * time.Second,
		ReconnectMin: time.Second, ReconnectMax: 15 * time.Second,
	}
}

type Server struct {
	config        Config
	dialer        websocket.Dialer
	upgrader      websocket.Upgrader
	mu            sync.Mutex
	workers       sync.WaitGroup
	ctx           context.Context
	cancel        context.CancelFunc
	stopping      bool
	clients       map[*client]struct{}
	hubs          map[groupKey]*hub
	sequence      uint64
	retainedQueue int
	reading       int
	started       time.Time
	salt          [32]byte
	counters      counters
}

type packet struct {
	prepared *websocket.PreparedMessage
	kind     int
	body     []byte
	ack      bool
	received time.Time
}

type queue struct {
	wake     chan struct{}
	packets  []packet
	bytes    int // Includes bytes held by an in-progress write.
	inFlight bool
}

func newQueue() queue { return queue{wake: make(chan struct{}, 1)} }

type client struct {
	conn         *websocket.Conn
	ctx          context.Context
	cancel       context.CancelFunc
	queue        queue
	order        uint64
	trx          string
	synchronized bool
	ackSent      bool
}

type hub struct {
	info        handshake
	canonical   packet
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	clients     map[*client]struct{}
	leader      *client
	upstream    *connection
	last        packet
	ack         []byte
	connectedAt time.Time
	reconnects  uint64
	generation  uint64
	seen        map[string]struct{}
	responseIDs []string
}

type connection struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	queue  queue
}

func New(config Config, dialer *websocket.Dialer) (*Server, error) {
	if config.MaxClients < 1 || config.MaxClients > 4096 ||
		config.MaxGroups < 1 || config.MaxGroups > 128 ||
		config.MaxMessageBytes < 1024 || config.MaxMessageBytes > 4<<20 ||
		config.MaxMessages < 2 || config.MaxMessages > 256 ||
		config.QueueBytes < config.MaxMessageBytes || config.QueueBytes > 8<<20 ||
		config.TotalQueueBytes < config.QueueBytes || config.TotalQueueBytes > 512<<20 ||
		config.JoinTimeout <= 0 || config.WriteTimeout <= 0 ||
		config.PingInterval <= 0 || config.PingInterval >= config.IdleTimeout ||
		config.ReconnectMin <= 0 || config.ReconnectMax < config.ReconnectMin {
		return nil, errors.New("invalid fanout resource limits")
	}
	if dialer == nil {
		dialer = &websocket.Dialer{
			NetDialContext:   (netguard.Dialer{}).DialContext,
			HandshakeTimeout: 10 * time.Second, EnableCompression: true,
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		ctx: ctx, cancel: cancel, config: config, dialer: *dialer,
		clients: make(map[*client]struct{}), hubs: make(map[groupKey]*hub), started: time.Now(),
		upgrader: websocket.Upgrader{
			HandshakeTimeout: config.WriteTimeout,
			// Nginx overwrites the upstream headers and API access is loopback-only.
			// LAN renderers historically send the data-plane Origin via Nginx.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	rand.Read(s.salt[:])
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	address, _ := netip.ParseAddr(host)
	if !address.Unmap().IsLoopback() {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/metrics":
		s.metrics(w)
	case "/healthz":
		w.WriteHeader(http.StatusNoContent)
	case "/ws":
		s.serveWS(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.stopping || len(s.clients) >= s.config.MaxClients {
		s.counters.admissionRejected++
		s.mu.Unlock()
		http.Error(w, "connection limit reached", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	c := &client{ctx: ctx, cancel: cancel, queue: newQueue()}
	s.sequence++
	c.order = s.sequence
	s.clients[c] = struct{}{}
	s.workers.Add(1)
	s.mu.Unlock()
	defer s.workers.Done()
	defer func() {
		s.mu.Lock()
		s.disconnect(c)
		delete(s.clients, c)
		s.mu.Unlock()
	}()

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.mu.Lock()
	c.conn = conn
	s.mu.Unlock()
	defer conn.Close()
	if c.ctx.Err() != nil {
		return
	}
	s.prepareReader(conn)
	first, err := s.read(conn, time.Now().Add(s.config.JoinTimeout), min(s.config.MaxMessageBytes, 16<<10))
	if err != nil {
		s.reject(conn, "join required")
		return
	}
	join, err := parseHandshake(r, parse(first.body), s.config.UpstreamBase, s.config.UpstreamOrigin)
	if err != nil {
		s.reject(conn, "invalid join")
		return
	}

	h := s.attach(c, join, first)
	if h == nil {
		s.reject(conn, "group unavailable")
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.writeLoop(c.ctx, conn, &c.queue, c)
	}()
	defer func() {
		s.mu.Lock()
		s.disconnect(c)
		delete(h.clients, c)
		if h.leader == c {
			h.elect()
		}
		if len(h.clients) == 0 {
			h.cancel()
			if h.upstream != nil {
				h.upstream.conn.Close()
			}
		}
		s.mu.Unlock()
		<-done
	}()
	s.enableIdle(conn)
	for {
		p, err := s.read(conn, time.Time{}, s.config.MaxMessageBytes)
		if err != nil || !s.fromClient(h, c, p) {
			return
		}
	}
}

func (h *hub) elect() {
	h.leader = nil
	for c := range h.clients {
		if c.ctx.Err() == nil && (h.leader == nil || c.order < h.leader.order) {
			h.leader = c
		}
	}
}

func (s *Server) attach(c *client, info handshake, first packet) *hub {
	timer := time.NewTimer(s.config.JoinTimeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		if c.ctx.Err() != nil {
			s.mu.Unlock()
			return nil
		}
		h := s.hubs[info.key]
		if h != nil && h.ctx.Err() != nil {
			// The previous connection owns the identity until its socket is closed.
			s.mu.Unlock()
			select {
			case <-h.done:
				continue
			case <-c.ctx.Done():
			case <-timer.C:
			}
			return nil
		}
		if h == nil {
			if len(s.hubs) >= s.config.MaxGroups {
				s.counters.admissionRejected++
				s.mu.Unlock()
				return nil
			}
			ctx, cancel := context.WithCancel(s.ctx)
			h = &hub{info: info, canonical: first, ctx: ctx, cancel: cancel, done: make(chan struct{}), clients: make(map[*client]struct{})}
			s.hubs[info.key] = h
			s.workers.Add(1)
			go s.runHub(h)
		}

		c.trx = info.trx
		h.clients[c] = struct{}{}
		if h.leader == nil {
			h.leader = c
		}
		if len(h.ack) > 0 {
			s.synchronize(h, c)
		}
		s.mu.Unlock()
		return h
	}
}

func (s *Server) reject(conn *websocket.Conn, reason string) {
	conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason), time.Now().Add(s.config.WriteTimeout))
}

func (s *Server) disconnect(c *client) {
	c.cancel()
	s.clearQueue(&c.queue)
	if c.conn != nil {
		c.conn.Close()
	}
}

func (s *Server) prepareReader(conn *websocket.Conn) {
	conn.SetReadLimit(int64(s.config.MaxMessageBytes))
}

func (s *Server) enableIdle(conn *websocket.Conn) {
	conn.SetReadDeadline(time.Now().Add(s.config.IdleTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(s.config.IdleTimeout))
	})
}

func (s *Server) read(conn *websocket.Conn, deadline time.Time, limit int) (packet, error) {
	if !deadline.IsZero() {
		conn.SetReadDeadline(deadline)
		defer conn.SetReadDeadline(time.Now().Add(s.config.IdleTimeout))
	}
	kind, reader, err := conn.NextReader()
	if err != nil {
		return packet{}, err
	}
	s.mu.Lock()
	if s.reading+limit > s.config.TotalQueueBytes {
		s.counters.admissionRejected++
		s.mu.Unlock()
		return packet{}, errors.New("message read budget exhausted")
	}
	s.reading += limit
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.reading -= limit
		s.mu.Unlock()
	}()
	// SetReadLimit bounds the wire representation. Bound decompressed payloads too.
	body, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return packet{}, err
	}
	if len(body) > limit {
		conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseMessageTooBig, "message exceeds limit"), time.Now().Add(s.config.WriteTimeout))
		return packet{}, errors.New("message exceeds limit")
	}
	if kind == websocket.TextMessage && !utf8.Valid(body) {
		return packet{}, errors.New("invalid websocket text")
	}
	return packet{kind: kind, body: body, received: time.Now()}, nil
}

func (s *Server) push(q *queue, p packet) bool {
	if len(q.packets) >= s.config.MaxMessages || q.bytes+len(p.body) > s.config.QueueBytes ||
		s.retainedQueue+len(p.body) > s.config.TotalQueueBytes {
		s.counters.queueOverflow++
		return false
	}
	q.packets = append(q.packets, p)
	q.bytes += len(p.body)
	s.retainedQueue += len(p.body)
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (s *Server) clearQueue(q *queue) {
	for _, p := range q.packets {
		q.bytes -= len(p.body)
		s.retainedQueue -= len(p.body)
	}
	q.packets = nil
}

func (s *Server) downstream(c *client, p packet) {
	if c.ctx.Err() == nil && !s.push(&c.queue, p) {
		s.counters.slowDisconnects++
		s.disconnect(c)
	}
}

func (s *Server) pop(q *queue) (packet, bool) {
	if len(q.packets) == 0 {
		return packet{}, false
	}
	p := q.packets[0]
	q.packets[0] = packet{}
	q.packets = q.packets[1:]
	q.inFlight = true
	return p, true
}

func (s *Server) writeLoop(ctx context.Context, conn *websocket.Conn, q *queue, c *client) {
	defer conn.Close()
	ticker := time.NewTicker(s.config.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(s.config.WriteTimeout)); err != nil {
				return
			}
		case <-q.wake:
			for ctx.Err() == nil {
				s.mu.Lock()
				p, ok := s.pop(q)
				s.mu.Unlock()
				if !ok {
					break
				}
				conn.SetWriteDeadline(time.Now().Add(s.config.WriteTimeout))
				var err error
				if p.prepared != nil {
					err = conn.WritePreparedMessage(p.prepared)
				} else {
					err = conn.WriteMessage(p.kind, p.body)
				}
				s.mu.Lock()
				q.inFlight = false
				q.bytes -= len(p.body)
				s.retainedQueue -= len(p.body)
				if c != nil && err == nil {
					s.counters.downMessages++
					s.counters.downBytes += uint64(len(p.body))
					if p.ack {
						c.ackSent = true
					} else if !p.received.IsZero() {
						s.counters.observe(time.Since(p.received))
					}
				}
				if c != nil && err != nil && ctx.Err() == nil {
					s.counters.writeFailures++
				}
				s.mu.Unlock()
				if err != nil {
					return
				}
				select {
				case <-ticker.C:
					if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(s.config.WriteTimeout)); err != nil {
						return
					}
				default:
				}
			}
		}
	}
}

func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	s.stopping = true
	s.cancel()
	for c := range s.clients {
		s.disconnect(c)
	}
	for _, h := range s.hubs {
		if h.upstream != nil {
			h.upstream.conn.Close()
		}
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
