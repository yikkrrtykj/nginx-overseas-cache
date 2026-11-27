package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yikkrrtykj/nginx-overseas-cache/internal/configstore"
	"github.com/yikkrrtykj/nginx-overseas-cache/internal/discovery"
	"github.com/yikkrrtykj/nginx-overseas-cache/internal/fanout"
	"github.com/yikkrrtykj/nginx-overseas-cache/web"
)

const fixturePassword = "Local browser fixture password"

type mockTransport struct{}

func (mockTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := "<link href=\"https://cdn.origin.example/templates/layout.woff2\">"
	kind := "text/html"
	if r.URL.Host == "cdn.origin.example" {
		body, kind = "fixture font", "font/woff2"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {kind}},
		Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func stubAdmin(t *testing.T) (*Server, *configstore.Store, string) {
	t.Helper()
	root := t.TempDir()
	upstream := filepath.Join(root, "proxy-cache.conf")
	renderer := filepath.Join(root, "settings.conf")
	pass := filepath.Join(root, "admin-password.json")
	data, err := os.ReadFile("../../nginx/conf.d/proxy-cache.conf")
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{upstream: data, renderer: configstore.RenderSettings("fixture", "renderer", "synthetic-token-01")} {
		if err := os.WriteFile(name, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := InitPassword(pass, "admin", fixturePassword); err != nil {
		t.Fatal(err)
	}
	store := &configstore.Store{
		UpstreamPath: upstream, SettingsPath: renderer, BackupRoot: root, LockPath: filepath.Join(root, "lock"),
		Validate: func(_ context.Context) error {
			settings, err := os.ReadFile(renderer)
			if err == nil && strings.Contains(string(settings), "\"fail-config\"") {
				return fmt.Errorf("synthetic nginx check failure")
			}
			return err
		},
		Reload: func(_ context.Context) error { return nil },
	}
	opts := Options{PasswordFile: pass, StatusFile: filepath.Join(root, "status.json"), TrafficFile: filepath.Join(root, "traffic.log"),
		AllowedNetworks: DefaultNetworks, Config: store, Assets: web.Assets(), Discovery: discovery.New(mockTransport{})}
	server, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return server, store, root
}

func TestAuthAndStatus(t *testing.T) {
	s, _, root := stubAdmin(t)
	send := func(method, path, body, session, csrf, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:23000"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if session != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: session})
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		response := httptest.NewRecorder()
		s.ServeHTTP(response, r)
		return response
	}
	const same = "http://127.0.0.1:23000"
	if got := send("GET", "/api/status", "", "", "", ""); got.Code != 401 {
		t.Fatal(got.Code)
	}
	login := func() string {
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": fixturePassword})
		res := send("POST", "/api/login", string(body), "", "", same)
		if res.Code != 200 {
			t.Fatalf("login: %d %s", res.Code, res.Body.String())
		}
		cookies := res.Result().Cookies()
		if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
			t.Fatal("session cookie flags")
		}
		return cookies[0].Value
	}
	a, b := login(), login()
	got := send("GET", "/api/config", "", a, "", "").Body.String()
	if strings.Contains(got, "synthetic-token") {
		t.Fatal("token leaked to read API")
	}
	var conf struct{ CSRFToken string }
	json.Unmarshal([]byte(got), &conf)
	const change = "{\"currentPassword\":\"Local browser fixture password\",\"newPassword\":\"Distinct fixture passphrase\"}"
	if res := send("POST", "/api/password", change, b, conf.CSRFToken, same); res.Code != 403 {
		t.Fatal("nonce worked across sessions")
	}
	if res := send("POST", "/api/password", change, a, conf.CSRFToken, "http://127.0.0.1:80"); res.Code != 403 {
		t.Fatal("cross-origin write passed")
	}
	if res := send("POST", "/api/password", change, a, conf.CSRFToken, same); res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	if res := send("GET", "/api/status", "", b, "", ""); res.Code != 401 {
		t.Fatal("session survived password change")
	}

	now := time.Now()
	path := filepath.Join(root, "status.json")
	data, _ := json.Marshal(fanout.Status{Version: 1, GeneratedAt: now.Add(-time.Minute)})
	os.WriteFile(path, data, 0600)
	if state := readStatus(path, now).State; state != "stale" {
		t.Fatalf("stale data shown as %q", state)
	}
	data, _ = json.Marshal(fanout.Status{Version: 2, GeneratedAt: now, Groups: []fanout.GroupStatus{{Origin: "origin.example", LocalClients: 1}}})
	os.WriteFile(path, data, 0600)
	if state := readStatus(path, now).State; state != "degraded" {
		t.Fatalf("unacknowledged group shown as %q", state)
	}
}

func TestTrafficOnlyUsesFixedFields(t *testing.T) {
	file := filepath.Join(t.TempDir(), "traffic.log")
	now := time.Now().UTC().Truncate(time.Second)
	for _, stamp := range []time.Time{now, now.Add(-2 * time.Minute)} {
		input := fmt.Sprintf("{\"time\":%q,\"route\":\"templates\",\"status\":200,\"bytes\":400,\"cache\":\"HIT\",\"upstream\":\"-\"}\n"+
			"{\"time\":%q,\"route\":\"render\",\"status\":502,\"bytes\":21,\"cache\":\"-\",\"upstream\":\"502, 502\"}\n",
			stamp.Format(time.RFC3339), stamp.Format(time.RFC3339))
		os.WriteFile(file, []byte(input), 0600)
		result := ReadTraffic(file, now)
		expect := "healthy"
		if !stamp.Equal(now) {
			expect = "stale"
		}
		if result.State != expect || result.Counts["HIT"] != 1 || result.Errors != 1 ||
			result.ResponseBytes != 421 || result.HitResponseBytes != 400 || result.UpstreamRequests != 2 {
			t.Fatalf("unexpected metrics: %+v", result)
		}
	}
}

func TestBrowserFixture(t *testing.T) {
	address := os.Getenv("OVERSEAS_BROWSER_LISTEN")
	if address == "" {
		t.Skip("browser runner owns this fixture")
	}
	s, _, root := stubAdmin(t)
	statusFile := filepath.Join(root, "status.json")
	trafficFile := filepath.Join(root, "traffic.log")
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	state := "normal"
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/__fixture/state" && r.Method == "POST":
			next := r.URL.Query().Get("mode")
			mu.Lock()
			state = next
			mu.Unlock()
			w.WriteHeader(204)
			return
		case (r.URL.Path == "/api/status" || r.URL.Path == "/api/v2/status" || r.URL.Path == "/api/v2/traffic") && s.auth.valid(token(r)):
			mu.Lock()
			defer mu.Unlock()
			if state == "error" {
				http.Error(w, "synthetic unavailable", 503)
				return
			}
			if state == "loading" && r.URL.Path != "/api/v2/traffic" {
				time.Sleep(2 * time.Second)
			}
			data := fanout.Status{Version: 2, GeneratedAt: time.Now(), LocalClients: 29, AcknowledgedClients: 28, ActiveHubs: 3, UpstreamConnections: 3, SavedUpstreamConnections: 25,
				Reconnects: 2, Goroutines: 74, UptimeSeconds: 10905, Groups: []fanout.GroupStatus{
					{Origin: "origin.example", Channel: "arena-stage", Mode: "onair", LocalClients: 18, AcknowledgedClients: 18, UpstreamReady: true, UpstreamConnected: true, Generation: 1, ConnectedSeconds: 10905},
					{Origin: "origin.example", Channel: "observer-feed", Mode: "preview", LocalClients: 7, AcknowledgedClients: 7, UpstreamReady: true, UpstreamConnected: true, Generation: 2, ConnectedSeconds: 2750, Reconnects: 1},
					{Origin: "backup.example", Channel: "arena-stage", Mode: "onair", LocalClients: 4, AcknowledgedClients: 3, UpstreamReady: true, UpstreamConnected: true, Generation: 2, ConnectedSeconds: 1248, Reconnects: 1},
				}}
			switch state {
			case "empty":
				data = fanout.Status{Version: 2, GeneratedAt: time.Now(), Groups: []fanout.GroupStatus{}}
			case "stale":
				data.GeneratedAt = time.Now().Add(-time.Minute)
			}
			payload, _ := json.Marshal(data)
			os.WriteFile(statusFile, payload, 0600)
			stamp := time.Now().UTC().Format(time.RFC3339)
			dataLog := fmt.Sprintf("{\"time\":%q,\"route\":\"templates\",\"status\":200,\"bytes\":4856921,\"cache\":\"HIT\",\"upstream\":\"-\"}\n", stamp)
			dataLog += fmt.Sprintf("{\"time\":%q,\"route\":\"templates\",\"status\":200,\"bytes\":125601,\"cache\":\"MISS\",\"upstream\":\"200\"}\n", stamp)
			if state == "empty" {
				dataLog = ""
			}
			os.WriteFile(trafficFile, []byte(dataLog), 0600)
		}
		s.ServeHTTP(w, r)
	})
	fmt.Println("BROWSER_READY", listener.Addr())
	if err := http.Serve(listener, h); err != nil {
		t.Fatal(err)
	}
}

var mu sync.Mutex
