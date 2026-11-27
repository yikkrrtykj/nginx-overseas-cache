package discovery

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRedirectAndCDNRanking(t *testing.T) {
	s := New(transport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Referer") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("unexpected credential forwarding")
		}
		h := make(http.Header)
		h.Set("Content-Type", "text/html")
		status, body := 200, ""
		switch r.URL.Host + r.URL.Path {
		case "origin.example/old":
			status = 302
			h.Set("Location", "/render?channelId=new&accountId=renderer")
		case "origin.example/render":
			body = "<link href=\"https://cdn.origin.example/templates/main.woff2\">"
		case "cdn.origin.example/templates/main.woff2":
			h.Set("Content-Type", "font/woff2")
			body = "synthetic-font"
		default:
			t.Fatalf("unexpected URL: %s", r.URL.Host+r.URL.Path)
		}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}))
	result, err := s.Scan(t.Context(), "https://origin.example/old?channelId=old&amp;token=synthetic-token", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != "" || result.Channel != "new" || result.Account != "renderer" ||
		result.Recommended != "cdn.origin.example" || result.Inspected != 3 {
		t.Fatalf("unexpected discovery: %+v", result)
	}
}
func TestRedirectCannotSwitchToPrivateSchemeOrIP(t *testing.T) {
	for _, target := range []string{"http://example.com", "https://127.0.0.1/"} {
		calls := 0
		s := New(transport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {target}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}))
		if _, err := s.Scan(t.Context(), "https://example.com/old", ""); err == nil || calls != 1 {
			t.Fatal("unsafe redirect reached transport")
		}
	}
}
