package configstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

type request struct {
	Changes Changes `json:"changes"`
	Backup  string  `json:"backup"`
}

type response struct {
	Result Result `json:"result"`
	Error  string `json:"error,omitempty"`
}

// RPC is exposed on the systemd-owned Unix socket. The calling account has no
// permission to write Nginx files or issue arbitrary commands.
func RPC(manager Manager) http.Handler {
	permits := make(chan struct{}, 4)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case permits <- struct{}{}:
			defer func() { <-permits }()
		default:
			http.Error(w, "configuration service busy", http.StatusServiceUnavailable)
			return
		}
		var result Result
		var err error
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/config":
			result, err = manager.Get(r.Context())
		case r.Method == http.MethodPost && (r.URL.Path == "/config" || r.URL.Path == "/rollback"):
			var input request
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF {
				http.Error(w, "invalid configuration request", http.StatusBadRequest)
				return
			}
			if r.URL.Path == "/rollback" {
				result, err = manager.Rollback(r.Context(), input.Backup)
			} else {
				result, err = manager.Apply(r.Context(), input.Changes)
			}
		default:
			http.NotFound(w, r)
			return
		}
		out := response{Result: result}
		if err != nil {
			out.Error = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
}

type Client struct{ http *http.Client }

func NewClient(socket string) *Client {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	return &Client{http: &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socket)
			},
			MaxResponseHeaderBytes: 8 << 10,
		},
		Timeout: 77 * time.Second,
	}}
}

func (c *Client) do(ctx context.Context, method, path string, input request) (Result, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://config-helper"+path, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return Result{}, errors.New("配置服务不可用")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return Result{}, errors.New("配置服务不可用")
	}
	var out response
	if err := json.NewDecoder(io.LimitReader(res.Body, 32<<10)).Decode(&out); err != nil {
		return Result{}, errors.New("配置服务响应无效")
	}
	if out.Error != "" {
		return out.Result, errors.New(out.Error)
	}
	return out.Result, nil
}

func (c *Client) Get(ctx context.Context) (Result, error) {
	return c.do(ctx, http.MethodGet, "/config", request{})
}
func (c *Client) Apply(ctx context.Context, changes Changes) (Result, error) {
	return c.do(ctx, http.MethodPost, "/config", request{Changes: changes})
}
func (c *Client) Rollback(ctx context.Context, backup string) (Result, error) {
	return c.do(ctx, http.MethodPost, "/rollback", request{Backup: backup})
}
