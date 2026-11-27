// Package admin serves the LAN management API and console.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yikkrrtykj/nginx-overseas-cache/internal/configstore"
	"github.com/yikkrrtykj/nginx-overseas-cache/internal/discovery"
)

const cookieName = "overseas_admin_session"
const DefaultNetworks = "192.168.0.0/16,10.0.0.0/8,172.16.0.0/12,127.0.0.0/8,::1/128"

type Options struct {
	PasswordFile, StatusFile, TrafficFile string
	AllowedNetworks, AllowedHosts         string
	Assets                                fs.FS
	Config                                configstore.Manager
	Discovery                             *discovery.Scanner
	NginxHealth                           func(context.Context) bool
}

type Server struct {
	options                        Options
	auth                           *authentication
	networks                       []netip.Prefix
	allowedHosts                   map[string]bool
	requests                       chan struct{}
	mu                             sync.Mutex
	apiCount, applyOK, applyFailed uint64
	apiTime                        time.Duration
}

func New(opts Options) (*Server, error) {
	if opts.Config == nil || opts.Assets == nil {
		return nil, errors.New("missing config service or assets")
	}
	auth, err := newAuth(opts.PasswordFile)
	if err != nil {
		return nil, err
	}
	if opts.Discovery == nil {
		opts.Discovery = discovery.New(nil)
	}
	s := &Server{options: opts, auth: auth, allowedHosts: map[string]bool{}, requests: make(chan struct{}, 64)}
	for _, part := range strings.Split(opts.AllowedNetworks, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(part))
		if err != nil {
			return nil, errors.New("无效的管理网段设置")
		}
		s.networks = append(s.networks, prefix.Masked())
	}
	for _, part := range strings.Split(opts.AllowedHosts, ",") {
		if part = strings.ToLower(strings.TrimSpace(part)); part != "" {
			s.allowedHosts[part] = true
		}
	}
	return s, nil
}

func address(value string) netip.Addr {
	ip, _ := netip.ParseAddr(value)
	return ip.WithZone("").Unmap()
}

func (s *Server) allowed(ip netip.Addr) bool {
	for _, prefix := range s.networks {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func authority(value string) (hostname, port string, valid bool) {
	u, err := url.Parse("//" + value)
	if err != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.HasSuffix(u.Host, ":") {
		return "", "", false
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", "", false
		}
	}
	return strings.ToLower(u.Hostname()), u.Port(), u.Hostname() != ""
}

func (s *Server) hostAllowed(value string) bool {
	host, _, ok := authority(value)
	if !ok {
		return false
	}
	if host == "localhost" {
		return s.allowed(netip.MustParseAddr("127.0.0.1"))
	}
	return s.allowedHosts[host] || s.allowed(address(host))
}

func sameOrigin(r *http.Request) bool {
	o, err := url.Parse(r.Header.Get("Origin"))
	scheme, defaultPort := "http", "80"
	if r.TLS != nil {
		scheme, defaultPort = "https", "443"
	}
	if err != nil || o.Scheme != scheme || o.User != nil || (o.Path != "" && o.Path != "/") ||
		o.RawQuery != "" || o.ForceQuery || o.Fragment != "" {
		return false
	}
	wantHost, wantPort, valid := authority(r.Host)
	gotHost, gotPort, ok := authority(o.Host)
	if wantPort == "" {
		wantPort = defaultPort
	}
	if gotPort == "" {
		gotPort = defaultPort
	}
	return valid && ok && wantHost == gotHost && wantPort == gotPort
}

func jsonReply(w http.ResponseWriter, code int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(payload)
}

func failure(w http.ResponseWriter, code int, message string) {
	jsonReply(w, code, map[string]any{"ok": false, "message": message})
}

func jsonBody(w http.ResponseWriter, r *http.Request, to any) error {
	mimeType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mimeType != "application/json" {
		return errors.New("请求格式必须为 JSON")
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if err = d.Decode(to); err != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("请求内容不是有效的 JSON")
	}
	return nil
}

func token(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil || len(c.Value) != 43 {
		return ""
	}
	return c.Value
}

func setCookie(w http.ResponseWriter, r *http.Request, value string) {
	age := int(sessionTTL.Seconds())
	if value == "" {
		age = -1
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil, MaxAge: age})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if r.TLS != nil {
		w.Header().Set("Strict-Transport-Security", "max-age=86400")
	}

	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	peer := address(host)
	if !s.allowed(peer) {
		failure(w, 403, "管理页仅允许配置的管理网段")
		return
	}
	if !s.hostAllowed(r.Host) {
		failure(w, 400, "请使用配置的管理地址")
		return
	}
	select {
	case s.requests <- struct{}{}:
		defer func() { <-s.requests }()
	default:
		failure(w, 503, "管理服务繁忙")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		failure(w, 405, "请求方法不支持")
		return
	}
	if r.URL.Path == "/metrics" && (peer.IsLoopback() || s.auth.valid(token(r))) && r.Method == http.MethodGet {
		s.metrics(w)
		return
	}
	if r.URL.Path == "/healthz" && peer.IsLoopback() && r.Method == http.MethodGet {
		w.WriteHeader(204)
		return
	}
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/v2/") {
		path = "/api/" + strings.TrimPrefix(path, "/api/v2/")
	}
	if strings.HasPrefix(path, "/api/") {
		start := time.Now()
		defer func() { s.mu.Lock(); s.apiCount++; s.apiTime += time.Since(start); s.mu.Unlock() }()
		s.api(w, r, path, peer.String())
		return
	}
	if r.Method == http.MethodPost {
		failure(w, 404, "页面不存在")
		return
	}
	switch path {
	case "/login", "/login.html":
		s.asset(w, r, "login.html")
	case "/", "/index.html":
		if !s.auth.valid(token(r)) {
			http.Redirect(w, r, "/login", 303)
			return
		}
		s.asset(w, r, "index.html")
	default:
		if path == "/app.css" || path == "/login.js" || path == "/app.js" || strings.HasPrefix(path, "/assets/") {
			s.asset(w, r, strings.TrimPrefix(path, "/"))
			return
		}
		failure(w, 404, "页面不存在")
	}
}

func (s *Server) asset(w http.ResponseWriter, r *http.Request, name string) {
	if !fs.ValidPath(name) {
		failure(w, 404, "页面不存在")
		return
	}
	if _, err := fs.Stat(s.options.Assets, name); err != nil {
		failure(w, 404, "页面不存在")
		return
	}
	http.ServeFileFS(w, r, s.options.Assets, name)
}

func (s *Server) api(w http.ResponseWriter, r *http.Request, path, ip string) {
	session := token(r)
	if r.Method == http.MethodPost && !sameOrigin(r) {
		failure(w, 403, "来源检查失败，请重新打开管理页")
		return
	}
	if r.Method == http.MethodPost && path == "/api/login" {
		var input struct{ Username, Password string }
		if err := jsonBody(w, r, &input); err != nil {
			failure(w, 400, err.Error())
			return
		}
		session, retry := s.auth.login(ip, input.Username, input.Password)
		if retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			failure(w, 429, "登录尝试过多，请稍后重试")
			return
		}
		if session == "" {
			failure(w, 401, "用户名或密码不正确")
			return
		}
		s.auth.logout(token(r))
		setCookie(w, r, session)
		jsonReply(w, 200, map[string]any{"ok": true, "message": "登录成功"})
		return
	}
	if r.Method == http.MethodGet && path == "/api/session" {
		jsonReply(w, 200, map[string]any{"ok": true, "authenticated": s.auth.valid(session)})
		return
	}
	if !s.auth.valid(session) {
		failure(w, 401, "登录已过期，请重新登录")
		return
	}
	if r.Method == http.MethodGet {
		s.get(w, r, path, session)
		return
	}
	if r.Method != http.MethodPost {
		failure(w, 405, "请求方法不支持")
		return
	}
	if path == "/api/logout" {
		s.auth.logout(session)
		setCookie(w, r, "")
		jsonReply(w, 200, map[string]any{"ok": true, "message": "已退出登录"})
		return
	}
	if !s.auth.consume(session, r.Header.Get("X-CSRF-Token")) {
		failure(w, 403, "页面已过期，请刷新后重试")
		return
	}
	out, err := s.mutate(w, r, path, session)
	if err != nil {
		jsonReply(w, 400, map[string]any{"ok": false, "message": err.Error(), "csrfToken": s.auth.csrf(session)})
		return
	}
	out["ok"] = true
	if path != "/api/password" {
		out["csrfToken"] = s.auth.csrf(session)
	}
	jsonReply(w, 200, out)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, path, session string) {
	switch path {
	case "/api/config":
		nonce := s.auth.csrf(session)
		result, err := s.options.Config.Get(r.Context())
		if err != nil {
			jsonReply(w, 503, map[string]any{"ok": false, "message": err.Error(), "csrfToken": nonce})
			return
		}
		jsonReply(w, 200, map[string]any{"ok": true, "config": result.Config, "csrfToken": nonce})
	case "/api/status":
		jsonReply(w, 200, map[string]any{"ok": true, "status": readStatus(s.options.StatusFile, time.Now())})
	case "/api/traffic":
		traffic := ReadTraffic(s.options.TrafficFile, time.Now())
		traffic.NginxState = "unknown"
		if s.options.NginxHealth != nil {
			traffic.NginxState = "unavailable"
			if s.options.NginxHealth(r.Context()) {
				traffic.NginxState = "healthy"
			}
		}
		jsonReply(w, 200, map[string]any{"ok": true, "traffic": traffic})
	default:
		failure(w, 404, "接口不存在")
	}
}

func (s *Server) mutate(w http.ResponseWriter, r *http.Request, path, session string) (map[string]any, error) {
	switch path {
	case "/api/password":
		var input struct{ CurrentPassword, NewPassword string }
		if err := jsonBody(w, r, &input); err != nil {
			return nil, err
		}
		if err := s.auth.change(session, input.CurrentPassword, input.NewPassword); err != nil {
			return nil, err
		}
		setCookie(w, r, "")
		log.Print("admin password changed")
		return map[string]any{"message": "密码已修改，请重新登录"}, nil
	case "/api/discover":
		var input struct{ SourceURL, EvidenceURL string }
		if err := jsonBody(w, r, &input); err != nil {
			return nil, err
		}
		result, err := s.options.Discovery.Scan(r.Context(), input.SourceURL, input.EvidenceURL)
		return map[string]any{"message": "识别完成", "result": result}, err
	case "/api/config":
		var input configstore.Changes
		if err := jsonBody(w, r, &input); err != nil {
			return nil, err
		}
		result, err := s.options.Config.Apply(r.Context(), input)
		return s.applyResult("apply", result, err)
	case "/api/rollback":
		var input struct{ Backup string }
		if err := jsonBody(w, r, &input); err != nil {
			return nil, err
		}
		result, err := s.options.Config.Rollback(r.Context(), input.Backup)
		return s.applyResult("rollback", result, err)
	default:
		return nil, errors.New("接口不存在")
	}
}

func (s *Server) applyResult(action string, result configstore.Result, err error) (map[string]any, error) {
	s.mu.Lock()
	if err == nil {
		s.applyOK++
	} else {
		s.applyFailed++
	}
	s.mu.Unlock()
	if err != nil {
		log.Printf("admin configuration %s failed", action)
		return nil, err
	}
	log.Printf("admin configuration %s completed changed=%t", action, result.Changed)
	var backup any
	if result.Backup != "" {
		backup = result.Backup
	}
	message := "配置没有变化"
	if result.Changed {
		message = "配置已应用，Nginx 检查和重载完成"
	}
	return map[string]any{"config": result.Config, "backup": backup, "message": message}, nil
}

func (s *Server) metrics(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.mu.Lock()
	count, total, ok, failed := s.apiCount, s.apiTime.Seconds(), s.applyOK, s.applyFailed
	s.mu.Unlock()
	fmt.Fprintf(w, "# TYPE overseas_admin_http_seconds summary\noverseas_admin_http_seconds_count %d\noverseas_admin_http_seconds_sum %g\n", count, total)
	fmt.Fprintf(w, "# TYPE overseas_admin_config_total counter\noverseas_admin_config_total{result=\"success\"} %d\noverseas_admin_config_total{result=\"failure\"} %d\n", ok, failed)
}
