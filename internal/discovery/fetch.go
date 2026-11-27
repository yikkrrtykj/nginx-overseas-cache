// Package discovery inspects public HTTPS renderer and resource URLs.
package discovery

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/yikkrrtykj/nginx-overseas-cache/internal/netguard"
)

const (
	maxDocument  = 2 << 20
	maxDownload  = 6 << 20
	maxRequests  = 20
	maxRedirects = 4
)

var urlInText = regexp.MustCompile(`(?i)https://[^\s"'<>]+`)

func normalize(value string) (*url.URL, error) {
	if len(value) > 4096 {
		return nil, errors.New("地址长度超限")
	}
	match := urlInText.FindString(html.UnescapeString(strings.TrimSpace(value)))
	if match == "" {
		return nil, errors.New("请输入完整的 HTTPS 公网地址")
	}
	if len(match) >= 8 {
		match = "https://" + match[8:]
	}
	return strictURL(strings.TrimRight(match, ".,;)]}"))
}

func strictURL(value string) (*url.URL, error) {
	if len(value) > 4096 {
		return nil, errors.New("地址长度超限")
	}
	u, err := netguard.URL(value, "https")
	if err != nil {
		return nil, errors.New("只允许标准 HTTPS 公网域名")
	}
	u.Fragment, u.RawFragment = "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

type document struct {
	url    *url.URL
	status int
	mime   string
	body   []byte
}

type fetcher struct {
	client              *http.Client
	remaining, requests int
}

func NewTransport() *http.Transport {
	return &http.Transport{
		DialContext: (netguard.Dialer{}).DialContext, DisableKeepAlives: true,
		TLSHandshakeTimeout: 4 * time.Second, ResponseHeaderTimeout: 4 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
	}
}

func (f *fetcher) fetch(ctx context.Context, target *url.URL, limit int) (document, error) {
	for hop := 0; hop <= maxRedirects; hop++ {
		if f.requests >= maxRequests || f.remaining <= 0 {
			return document{}, errors.New("扫描请求或下载预算已用尽")
		}
		f.requests++
		limit = min(limit, f.remaining)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return document{}, errors.New("扫描地址无效")
		}
		request.Header.Set("User-Agent", "OverseasCacheDiscovery/2")
		request.Header.Set("Range", fmt.Sprintf("bytes=0-%d", limit-1))
		// Disabling automatic decompression lets us bound both representations.
		request.Header.Set("Accept-Encoding", "identity")
		res, err := f.client.Do(request)
		if err != nil {
			return document{}, errors.New("无法连接或验证 HTTPS 上游")
		}
		switch res.StatusCode {
		case 301, 302, 303, 307, 308:
			next, err := res.Location()
			res.Body.Close()
			if err != nil {
				return document{}, errors.New("上游返回无效跳转")
			}
			target, err = strictURL(next.String())
			if err != nil {
				return document{}, err
			}
			continue
		}
		contentType, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
		raw, err := io.ReadAll(io.LimitReader(res.Body, int64(limit)))
		res.Body.Close()
		f.remaining -= len(raw)
		if err != nil {
			return document{}, errors.New("读取 HTTPS 响应失败")
		}
		body := raw
		encoding := strings.ToLower(strings.TrimSpace(res.Header.Get("Content-Encoding")))
		if encoding != "" && encoding != "identity" {
			reader, err := decode(raw, encoding)
			if err != nil {
				return document{}, errors.New("上游响应内容编码无效")
			}
			body, err = io.ReadAll(io.LimitReader(reader, int64(min(limit, f.remaining))))
			reader.Close()
			f.remaining -= len(body)
			if err != nil {
				return document{}, errors.New("读取 HTTPS 压缩响应失败")
			}
		}
		return document{target, res.StatusCode, contentType, body}, nil
	}
	return document{}, errors.New("网页跳转次数过多")
}

func decode(raw []byte, encoding string) (io.ReadCloser, error) {
	switch encoding {
	case "gzip":
		return gzip.NewReader(bytes.NewReader(raw))
	case "deflate":
		return zlib.NewReader(bytes.NewReader(raw))
	default:
		return nil, errors.New("unsupported content encoding")
	}
}
