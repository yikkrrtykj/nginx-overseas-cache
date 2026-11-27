package fanout

import (
	"context"
	"encoding/json"
	mrand "math/rand/v2"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

func (s *Server) runHub(h *hub) {
	defer s.workers.Done()
	defer func() {
		s.mu.Lock()
		delete(s.hubs, h.info.key)
		close(h.done)
		s.mu.Unlock()
	}()
	delay := s.config.ReconnectMin
	for h.ctx.Err() == nil {
		dialCtx, cancel := context.WithTimeout(h.ctx, s.config.JoinTimeout)
		conn, res, err := s.dialer.DialContext(dialCtx, h.info.uri, http.Header{"Origin": {h.info.origin}})
		cancel()
		if res != nil && err != nil {
			res.Body.Close()
		}
		if err == nil {
			if s.runGeneration(h, conn) {
				delay = s.config.ReconnectMin
			}
		}
		if h.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		h.reconnects++
		s.counters.reconnects++
		s.mu.Unlock()
		wait := delay/2 + time.Duration(mrand.Int64N(max(1, int64(delay/2))))
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-h.ctx.Done():
			timer.Stop()
			return
		}
		delay = min(delay*2, s.config.ReconnectMax)
	}
}

func (s *Server) runGeneration(h *hub, conn *websocket.Conn) bool {
	defer conn.Close()
	ctx, cancel := context.WithCancel(h.ctx)
	up := &connection{ctx: ctx, cancel: cancel, queue: newQueue()}
	defer cancel()
	stopAbort := context.AfterFunc(ctx, func() { abortSocket(conn) })
	defer stopAbort()

	s.prepareReader(conn)
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return false
	}
	h.generation++
	h.connectedAt = time.Now()
	h.upstream = up
	h.seen = make(map[string]struct{})
	s.mu.Unlock()

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		s.writeLoop(ctx, conn, &up.queue, nil)
	}()

	defer func() {
		// Invalidate state before another downstream join can observe it.
		s.mu.Lock()
		h.upstream, h.ack, h.last = nil, nil, packet{}
		h.seen, h.responseIDs = nil, nil
		for c := range h.clients {
			// A response can arrive after its call has already been written.
			// Require a fresh downstream socket whenever a peer could have
			// observed this generation, including reused transaction IDs.
			if c.deliveryGeneration == h.generation {
				s.disconnect(c)
			}
			c.synchronized, c.ackSent = false, false
		}
		h.elect()
		up.cancel()
		s.clearQueue(&up.queue)
		s.mu.Unlock()
		abortSocket(conn)
		<-writerDone
	}()

	s.mu.Lock()
	ok := s.push(&up.queue, h.canonical)
	s.mu.Unlock()
	if !ok {
		return false
	}
	deadline := time.Now().Add(s.config.JoinTimeout)
	acknowledged := false
	for ctx.Err() == nil {
		p, err := s.read(conn, deadline, s.config.MaxMessageBytes)
		if err != nil {
			break
		}
		s.mu.Lock()
		if ctx.Err() != nil {
			s.mu.Unlock()
			break
		}
		ok := s.fromUpstream(h, p)
		acknowledged = len(h.ack) > 0
		s.mu.Unlock()
		if !ok {
			break
		}
		if acknowledged && !deadline.IsZero() {
			deadline = time.Time{}
			s.enableIdle(conn)
		}
	}
	return acknowledged
}

// The server lock orders join acknowledgements, replay and live delivery.
func (s *Server) synchronize(h *hub, c *client) {
	if c.synchronized || c.ctx.Err() != nil {
		return
	}
	var template map[string]json.RawMessage
	if json.Unmarshal(h.ack, &template) != nil {
		s.disconnect(c)
		return
	}
	body, err := renderAck(template, c.trx)
	if err != nil || len(body) > s.config.MaxMessageBytes {
		s.disconnect(c)
		return
	}
	s.downstream(c, packet{kind: websocket.TextMessage, body: body, ack: true})
	if h.last.body != nil {
		last := h.last
		last.received = time.Time{}
		s.downstream(c, last)
	}
	c.synchronized = c.ctx.Err() == nil
	if c.synchronized {
		c.deliveryGeneration = h.generation
	}
}

func (s *Server) fromUpstream(h *hub, p packet) bool {
	s.counters.upMessages++
	s.counters.upBytes += uint64(len(p.body))
	m := parse(p.body)
	switch m.Action {
	case "join_res":
		if len(p.body) > 64<<10 || scalar(m.TrxID) != h.info.trx {
			s.counters.protocolErrors++
			return false
		}
		if len(h.ack) == 0 {
			h.ack = p.body
			for c := range h.clients {
				s.synchronize(h, c)
			}
		}
		return true
	case "call":
		// V1 treats a call as the complete renderer state. Keep only this
		// generation's latest call, in its original text or binary format.
		h.last = p
	}
	if len(h.clients) >= 8 {
		// The live clients share the same wire-format payload. Preparing one
		// frame avoids repeating framing work for each socket.
		p.prepared, _ = websocket.NewPreparedMessage(p.kind, p.body)
	}
	for c := range h.clients {
		if c.synchronized {
			s.downstream(c, p)
		}
	}
	return true
}

func (s *Server) fromClient(h *hub, c *client, p packet) bool {
	m := parse(p.body)
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ctx.Err() != nil {
		return false
	}
	if m.Action == "join" {
		trx := scalar(m.TrxID)
		if len(trx) == 0 || len(trx) > 256 || !h.info.matches(m) {
			return false
		}
		c.trx, c.synchronized, c.ackSent = trx, false, false
		if len(h.ack) > 0 {
			s.synchronize(h, c)
		}
		return true
	}

	if m.Action != "call_res" && h.leader != c {
		return true
	}
	if h.upstream == nil || h.upstream.ctx.Err() != nil || !c.synchronized {
		// A call response belongs to the old generation. Other leader events
		// require explicit rejoin so no critical message is silently discarded.
		return m.Action == "call_res"
	}
	trx := ""
	if m.Action == "call_res" {
		trx = scalar(m.TrxID)
		if len(trx) > 256 {
			return false
		}
		if _, seen := h.seen[trx]; trx != "" && seen {
			return true
		}
	}
	if !s.push(&h.upstream.queue, p) {
		h.upstream.cancel()
		return false
	}
	if trx != "" {
		if len(h.responseIDs) == 2048 {
			delete(h.seen, h.responseIDs[0])
			h.responseIDs[0] = ""
			h.responseIDs = h.responseIDs[1:]
		}
		h.responseIDs = append(h.responseIDs, trx)
		h.seen[trx] = struct{}{}
	}
	return true
}
