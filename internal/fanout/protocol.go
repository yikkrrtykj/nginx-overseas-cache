package fanout

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/yikkrrtykj/nginx-overseas-cache/internal/netguard"
)

type groupKey struct {
	upstream string
	token    [32]byte
	channel  string
	mode     string
}

type handshake struct {
	key    groupKey
	origin string
	host   string
	uri    string
	trx    string
}

type message struct {
	Action  string          `json:"action"`
	TrxID   json.RawMessage `json:"trxId"`
	Channel json.RawMessage `json:"channel"`
	Data    json.RawMessage `json:"data"`
}

func parse(payload []byte) message {
	var m message
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &m) != nil {
		return message{}
	}
	return m
}

func scalar(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return text
		}
		return ""
	}
	if raw[0] >= '0' && raw[0] <= '9' || raw[0] == '-' {
		return string(raw)
	}
	return ""
}

var publishedID = regexp.MustCompile("^[a-zA-Z0-9._~-]*$")

type joinData struct {
	Channel json.RawMessage `json:"channelId"`
	Mode    json.RawMessage `json:"rendererMode"`
	Token   json.RawMessage `json:"token"`
}

func (m message) joinData() joinData {
	var data joinData
	json.Unmarshal(m.Data, &data)
	if scalar(data.Channel) == "" {
		data.Channel = m.Channel
	}
	return data
}

func (h handshake) matches(m message) bool {
	data := m.joinData()
	channel, mode, token := scalar(data.Channel), scalar(data.Mode), scalar(data.Token)
	return (channel == "" || channel == h.key.channel) && (mode == "" || mode == h.key.mode) &&
		(token == "" || sha256.Sum256([]byte(token)) == h.key.token)
}

func parseHandshake(r *http.Request, m message, fallbackBase, fallbackOrigin string) (handshake, error) {
	if m.Action != "join" {
		return handshake{}, errors.New("join required")
	}
	data := m.joinData()
	channel := scalar(data.Channel)
	if channel == "" {
		channel = scalar(m.Channel)
	}
	mode, trx := scalar(data.Mode), scalar(m.TrxID)
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return handshake{}, errors.New("invalid join query")
	}
	token := query.Get("token")
	if len(query["token"]) > 1 {
		return handshake{}, errors.New("ambiguous token")
	}
	if token == "" {
		token = scalar(data.Token)
	}
	if supplied := scalar(data.Token); supplied != "" && supplied != token {
		return handshake{}, errors.New("conflicting tokens")
	}
	if len(channel) > 128 || !publishedID.MatchString(channel) || len(mode) > 64 ||
		!publishedID.MatchString(mode) || len(trx) == 0 || len(trx) > 256 || len(token) == 0 || len(token) > 512 {
		return handshake{}, errors.New("invalid join fields")
	}
	base, origin := r.Header.Get("X-Fanout-Upstream-Base"), r.Header.Get("X-Fanout-Upstream-Origin")
	if base == "" {
		base = fallbackBase
	}
	if origin == "" {
		origin = fallbackOrigin
	}
	b, err := netguard.URL(base, "wss")
	if err != nil || b.RawQuery != "" || b.ForceQuery || b.Fragment != "" {
		return handshake{}, errors.New("invalid upstream base")
	}
	o, err := netguard.URL(origin, "https")
	if err != nil || (o.Path != "" && o.Path != "/") || o.RawQuery != "" ||
		o.ForceQuery || o.Fragment != "" || o.Host != b.Host {
		return handshake{}, errors.New("invalid upstream origin")
	}
	if b.Path == "" {
		b.Path = "/ws"
	}
	o.Path, o.RawPath = "", ""
	key := groupKey{upstream: b.String(), token: sha256.Sum256([]byte(token)), channel: channel, mode: mode}
	b.RawQuery = url.Values{"token": []string{token}}.Encode()
	return handshake{key: key, origin: o.String(), host: o.Host, uri: b.String(), trx: trx}, nil
}

func renderAck(template map[string]json.RawMessage, trx string) ([]byte, error) {
	copy := make(map[string]json.RawMessage, len(template)+2)
	for key, value := range template {
		copy[key] = value
	}
	copy["trxId"], _ = json.Marshal(trx)
	copy["timestamp"], _ = json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	return json.Marshal(copy)
}

func redactIdentity(h handshake) (string, string) {
	token := strings.Split(h.uri, "?token=")
	if len(token) != 2 {
		return h.key.channel, h.key.mode
	}
	secret, _ := url.QueryUnescape(token[1])
	if strings.Contains(h.key.channel, secret) || strings.Contains(h.key.mode, secret) {
		return "[redacted]", "[redacted]"
	}
	return h.key.channel, h.key.mode
}
