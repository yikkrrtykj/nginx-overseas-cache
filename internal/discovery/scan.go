package discovery

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/url"
	pathpkg "path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yikkrrtykj/nginx-overseas-cache/internal/configstore"
)

type Candidate struct {
	Host        string   `json:"host"`
	Score       int      `json:"score"`
	Types       []string `json:"types"`
	Samples     []string `json:"samples"`
	Compatible  bool     `json:"compatible"`
	Verified    bool     `json:"verified"`
	Valid       bool     `json:"valid"`
	Status      int      `json:"status"`
	ContentType string   `json:"contentType"`
	first       *url.URL
}

type Result struct {
	SourceHost  string      `json:"sourceHost"`
	SourcePath  string      `json:"sourcePath"`
	Channel     string      `json:"channelId"`
	Account     string      `json:"accountId"`
	Token       string      `json:"token"`
	Recommended string      `json:"recommendedHost"`
	Candidates  []Candidate `json:"candidates"`
	Inspected   int         `json:"inspectedCount"`
	Warnings    []string    `json:"warnings"`
}

type Scanner struct {
	client *http.Client
	work   chan struct{}
}

func New(transport http.RoundTripper) *Scanner {
	if transport == nil {
		transport = NewTransport()
	}
	return &Scanner{work: make(chan struct{}, 2), client: &http.Client{
		Transport: transport, Timeout: 7 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type category struct {
	label  string
	weight int
	scan   bool
}

var categories = map[string]category{
	".html": {"页面", 1, true}, ".htm": {"页面", 1, true}, ".css": {"样式", 7, true},
	".js": {"脚本", 3, true}, ".json": {"数据", 2, true},
	".png": {"图片", 5, false}, ".jpeg": {"图片", 5, false}, ".jpg": {"图片", 5, false},
	".gif": {"图片", 5, false}, ".webp": {"图片", 5, false}, ".svg": {"图片", 5, false}, ".ico": {"图片", 5, false},
	".otf": {"字体", 12, false}, ".ttf": {"字体", 12, false}, ".woff": {"字体", 12, false}, ".woff2": {"字体", 12, false},
	".mp4": {"媒体", 5, false}, ".webm": {"媒体", 5, false}, ".m3u8": {"媒体", 5, false},
}

func classify(u *url.URL, mime string) category {
	ext := categories[strings.ToLower(pathpkg.Ext(u.Path))]
	if ext.label != "" {
		return ext
	}
	switch {
	case strings.Contains(mime, "font"):
		return categories[".woff2"]
	case strings.HasPrefix(mime, "image/"):
		return categories[".png"]
	case strings.HasPrefix(mime, "video/"), strings.HasPrefix(mime, "audio/"):
		return categories[".mp4"]
	case strings.Contains(mime, "javascript"):
		return categories[".js"]
	case strings.Contains(mime, "json"):
		return categories[".json"]
	case mime == "text/css":
		return categories[".css"]
	case mime == "text/html":
		return categories[".html"]
	case strings.HasPrefix(u.Path, "/templates/"):
		return category{"模板资源", 10, false}
	default:
		return category{}
	}
}

var (
	resourceAttributes = regexp.MustCompile(`(?is)\b(?:src|href|srcset|data-src|poster|data)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	baseElement        = regexp.MustCompile(`(?is)<base\s+[^>]*>`)
	resourceURL        = regexp.MustCompile(`(?i)(?:https:)?//[^\s"'<>\\)]+|["'(]((?:/|\./|\.\./)[^\s"'()<>]+\.(?:html?|css|js|json|otf|ttf|woff2?|png|jpe?g|gif|webp|svg|ico|mp4|webm|m3u8)(?:\?[^\s"'()<>]*)?)`)
	authSegment        = regexp.MustCompile(`(?i)^(/templates-auth/[^/]+/)[^/]+`)
)

func attribute(m []string) string {
	for _, v := range m[1:] {
		if v != "" {
			return v
		}
	}
	return ""
}

func resources(doc document) []*url.URL {
	base := doc.url
	input := strings.NewReplacer("\\/", "/", "\\u002F", "/", "\\u002f", "/").Replace(string(doc.body))
	if match := resourceAttributes.FindStringSubmatch(baseElement.FindString(input)); len(match) > 0 {
		if u, err := base.Parse(html.UnescapeString(attribute(match))); err == nil && u.Host == base.Host {
			base = u
		}
	}
	seen := map[string]bool{}
	var urls []*url.URL
	collect := func(raw string) {
		if len(raw) == 0 || len(raw) > 2048 {
			return
		}
		u, err := base.Parse(html.UnescapeString(raw))
		if err != nil {
			return
		}
		u, err = strictURL(u.String())
		if err != nil || seen[u.String()] || classify(u, "").weight == 0 {
			return
		}
		seen[u.String()] = true
		urls = append(urls, u)
	}
	for _, m := range resourceAttributes.FindAllStringSubmatch(input, 256) {
		for _, variant := range strings.Split(attribute(m), ",") {
			if fields := strings.Fields(variant); len(fields) > 0 {
				collect(fields[0])
			}
		}
	}
	for _, m := range resourceURL.FindAllStringSubmatch(input, 256) {
		if m[1] != "" {
			collect(m[1])
		} else {
			collect(m[0])
		}
	}
	return urls
}

func parameters(u *url.URL) map[string]string {
	output := map[string]string{}
	for key, values := range u.Query() {
		if len(values) != 1 {
			continue
		}
		v := values[0]
		key = strings.ToLower(key)
		if ((key == "channelid" || key == "accountid") && configstore.ValidID(v)) ||
			(key == "token" && configstore.ValidToken(v)) {
			output[key] = v
		}
	}
	return output
}

func safePath(u *url.URL, secret string) string {
	value := authSegment.ReplaceAllString(u.Path, "$\u007b1}<token>")
	if secret != "" {
		value = strings.ReplaceAll(value, secret, "<token>")
	}
	runes := []rune(value)
	if len(runes) > 160 {
		return string(runes[:157]) + "..."
	}
	return value
}

func related(a, b string) bool {
	if a == b {
		return true
	}
	shared := func(s string) string {
		parts := strings.Split(s, ".")
		n := len(parts)
		minimum := 2
		if n >= 3 && len(parts[n-1]) == 2 && len(parts[n-2]) <= 3 {
			minimum = 3
		}
		if n < minimum {
			return s
		}
		return strings.Join(parts[n-minimum:], ".")
	}
	return shared(a) == shared(b)
}

func (s *Scanner) Scan(ctx context.Context, source, evidence string) (Result, error) {
	from, err := normalize(source)
	if err != nil {
		return Result{}, err
	}
	var resource *url.URL
	if strings.TrimSpace(evidence) != "" {
		resource, err = normalize(evidence)
		if err != nil {
			return Result{}, err
		}
	}
	select {
	case s.work <- struct{}{}:
		defer func() { <-s.work }()
	default:
		return Result{}, errors.New("识别任务繁忙，请稍后重试")
	}
	ctx, cancel := context.WithTimeout(ctx, 22*time.Second)
	defer cancel()
	f := fetcher{client: s.client, remaining: maxDownload}
	doc, err := f.fetch(ctx, from, maxDocument)
	if err != nil {
		return Result{}, err
	}
	if doc.status < 200 || doc.status >= 300 {
		return Result{}, errors.New("原始网页未返回成功状态")
	}

	old, newValues := parameters(from), parameters(doc.url)
	for _, key := range []string{"channelid", "accountid"} {
		if old[key] != "" && newValues[key] != "" && newValues[key] != old[key] {
			clear(old)
			break
		}
	}
	for k, v := range newValues {
		old[k] = v
	}
	out := Result{SourceHost: doc.url.Host, SourcePath: safePath(doc.url, old["token"]), Token: old["token"],
		Channel: old["channelid"], Account: old["accountid"], Candidates: []Candidate{}, Warnings: []string{}}

	candidates := map[string]*Candidate{}
	record := func(u *url.URL, mime string, status int) {
		category := classify(u, mime)
		if category.weight == 0 {
			return
		}
		item := candidates[u.Host]
		if item == nil {
			if len(candidates) >= 128 {
				return
			}
			item = &Candidate{Host: u.Host, first: u}
			candidates[u.Host] = item
		}
		item.Score += category.weight
		item.Compatible = item.Compatible || strings.HasPrefix(u.Path, "/templates/")
		if !slices.Contains(item.Types, category.label) {
			item.Types = append(item.Types, category.label)
		}
		if sample := safePath(u, out.Token); len(item.Samples) < 2 && !slices.Contains(item.Samples, sample) {
			item.Samples = append(item.Samples, sample)
		}
		if status != 0 {
			item.Verified, item.ContentType, item.Status = true, mime, status
			valid := status >= 200 && status < 300 && mime != "text/html"
			item.Valid = item.Valid || valid
			if valid {
				item.Score += 15
			} else {
				item.Score -= 30
			}
		}
	}
	seen := map[string]bool{doc.url.String(): true}
	var queue []*url.URL
	scan := func(d document) {
		for _, u := range resources(d) {
			record(u, "", 0)
			if len(queue) < 24 && !seen[u.String()] && related(out.SourceHost, u.Host) && classify(u, "").scan {
				seen[u.String()] = true
				queue = append(queue, u)
			}
		}
	}

	// Reserve an explicit F12 request before walking automatically discovered scripts.
	if resource != nil {
		d, err := f.fetch(ctx, resource, 128<<10)
		if err != nil {
			return Result{}, err
		}
		record(d.url, d.mime, d.status)
		if item := candidates[d.url.Host]; item != nil && item.Valid {
			item.Score += 30
		}
		if d.status < 200 || d.status >= 300 || d.mime == "text/html" {
			out.Warnings = append(out.Warnings, "F12 地址未返回有效静态资源")
		}
	}
	scan(doc)
	for count := 0; count < 8 && len(queue) > 0 && f.requests < maxRequests && ctx.Err() == nil; count++ {
		u := queue[0]
		queue = queue[1:]
		d, err := f.fetch(ctx, u, maxDocument)
		if err != nil {
			continue
		}
		record(d.url, d.mime, d.status)
		if d.status >= 200 && d.status < 300 && (classify(d.url, d.mime).scan || strings.HasPrefix(d.mime, "text/")) {
			scan(d)
		}
	}

	ranked := make([]*Candidate, 0, len(candidates))
	for host, item := range candidates {
		if host == out.SourceHost {
			item.Score -= 4
		}
		ranked = append(ranked, item)
	}
	slices.SortFunc(ranked, func(a, b *Candidate) int {
		if a.Score != b.Score {
			return b.Score - a.Score
		}
		return strings.Compare(a.Host, b.Host)
	})
	verified := 0
	for _, item := range ranked {
		if item.Verified || item.Host == out.SourceHost || !related(out.SourceHost, item.Host) {
			continue
		}
		if verified == 3 || ctx.Err() != nil || f.requests >= maxRequests {
			break
		}
		verified++
		d, err := f.fetch(ctx, item.first, 128<<10)
		if err == nil {
			record(d.url, d.mime, d.status)
		}
	}
	// Verification can redirect to a previously unseen host.
	ranked = ranked[:0]
	for _, item := range candidates {
		if item.Score > 0 {
			ranked = append(ranked, item)
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.Compatible != b.Compatible {
			return a.Compatible
		}
		if a.Valid != b.Valid {
			return a.Valid
		}
		if a.Verified != b.Verified {
			return a.Verified
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.Host < b.Host
	})
	for _, item := range ranked {
		if out.Recommended == "" && item.Host != out.SourceHost && item.Compatible && (!item.Verified || item.Valid) {
			out.Recommended = item.Host
		}
		if len(out.Candidates) < 8 {
			sort.Strings(item.Types)
			out.Candidates = append(out.Candidates, *item)
		}
	}
	out.Inspected = f.requests
	if out.Channel == "" || out.Account == "" || out.Token == "" {
		out.Warnings = append(out.Warnings, "频道、账户或 Token 未识别完整。保存前请补齐新网页参数。")
	}
	if out.Recommended == "" {
		out.Warnings = append(out.Warnings, "未确认兼容 /templates/ 的独立 CDN，可以补充 F12 资源地址。")
	}
	if ctx.Err() != nil {
		out.Warnings = append(out.Warnings, "扫描时间达到上限，当前结果来自已完成的请求。")
	}
	return out, nil
}
