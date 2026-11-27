package admin

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/yikkrrtykj/nginx-overseas-cache/internal/fanout"
)

func readLocal(path string, limit int64) ([]byte, time.Time, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, time.Time{}, errors.New("not a regular file")
	}
	f, err := openReadOnly(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, time.Time{}, errors.New("file changed while opening")
	}
	if !opened.Mode().IsRegular() || opened.Size() > limit {
		return nil, time.Time{}, errors.New("invalid file size")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, time.Time{}, errors.New("invalid file size")
	}
	return data, opened.ModTime(), err
}

type status struct {
	Available bool   `json:"available"`
	State     string `json:"state"`
	Message   string `json:"message"`
	fanout.Status
}

var tokenID = regexp.MustCompile("^[0-9a-f]{12}$")
var statusID = regexp.MustCompile("^[a-zA-Z0-9._~-]{0,128}$")
var statusHost = regexp.MustCompile("^[a-z0-9.:-]{1,253}$")

func readStatus(path string, now time.Time) status {
	blank := status{State: "unavailable", Message: "Fanout 尚未上报状态", Status: fanout.Status{Groups: []fanout.GroupStatus{}}}
	raw, mtime, err := readLocal(path, 256<<10)
	if err != nil {
		return blank
	}
	var data fanout.Status
	if json.Unmarshal(raw, &data) != nil || (data.Version != 1 && data.Version != 2) || data.GeneratedAt.IsZero() || len(data.Groups) > 128 {
		blank.State, blank.Message = "unknown", "Fanout 状态数据无效"
		return blank
	}
	if now.Sub(mtime) > 10*time.Second || now.Sub(data.GeneratedAt) > 10*time.Second || data.GeneratedAt.After(now.Add(2*time.Second)) {
		blank.State, blank.Message, blank.GeneratedAt = "stale", "Fanout 数据已过期", data.GeneratedAt
		return blank
	}
	if data.Groups == nil {
		data.Groups = []fanout.GroupStatus{}
	}
	state, message := "healthy", "实时状态正常"
	for i := range data.Groups {
		group := &data.Groups[i]
		if !tokenID.MatchString(group.TokenID) {
			group.TokenID = ""
		}
		if !statusID.MatchString(group.Channel) {
			group.Channel = ""
		}
		if !statusID.MatchString(group.Mode) {
			group.Mode = ""
		}
		if !statusHost.MatchString(group.Origin) {
			group.Origin = ""
		}
		if data.Version == 1 {
			group.UpstreamReady = group.UpstreamConnected && group.AcknowledgedClients > 0
		}
		if !group.UpstreamReady {
			state, message = "degraded", "部分共享组尚未完成上游握手"
		}
	}
	return status{true, state, message, data}
}

type trafficLine struct {
	Time     time.Time `json:"time"`
	Route    string    `json:"route"`
	Status   int       `json:"status"`
	Bytes    int64     `json:"bytes"`
	Cache    string    `json:"cache"`
	Upstream string    `json:"upstream"`
}

type TrafficPoint struct {
	Time             time.Time `json:"time"`
	UpstreamRequests int       `json:"upstreamRequests"`
	Entries          int       `json:"entries"`
}

type Traffic struct {
	Points           []TrafficPoint `json:"points"`
	NginxState       string         `json:"nginxState"`
	State            string         `json:"state"`
	ObservedAt       time.Time      `json:"observedAt"`
	Entries          int            `json:"entries"`
	Errors           int            `json:"errors"`
	ResponseBytes    int64          `json:"responseBytes"`
	HitResponseBytes int64          `json:"hitResponseBytes"`
	UpstreamRequests int            `json:"upstreamRequests"`
	Counts           map[string]int `json:"counts"`
}

func ReadTraffic(path string, now time.Time) Traffic {
	out := Traffic{State: "unavailable"}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return out
	}
	file, err := openReadOnly(path)
	if err != nil {
		return out
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil || !os.SameFile(info, fi) {
		return out
	}
	const limit = 512 << 10
	start := max(int64(0), fi.Size()-limit)
	lines := bufio.NewScanner(io.NewSectionReader(file, start, fi.Size()-start))
	lines.Buffer(make([]byte, 4096), 16384)
	if start > 0 {
		lines.Scan()
	}
	out.State = "empty"
	out.Counts = map[string]int{}
	const interval = 5 * time.Minute
	startTime := now.Truncate(interval).Add(-5 * interval)
	out.Points = make([]TrafficPoint, 6)
	for i := range out.Points {
		out.Points[i].Time = startTime.Add(time.Duration(i) * interval)
	}
	for lines.Scan() {
		var line trafficLine
		if json.Unmarshal(lines.Bytes(), &line) != nil || line.Time.IsZero() ||
			line.Bytes < 0 || line.Bytes > 1<<40 || line.Status < 100 || line.Status > 599 {
			continue
		}
		out.Entries++
		if line.Status >= 500 {
			out.Errors++
		}
		out.ResponseBytes += line.Bytes
		attempts := 0
		for _, code := range strings.FieldsFunc(line.Upstream, func(r rune) bool { return r == ',' || r == ':' || r == ' ' }) {
			if len(code) == 3 && code[0] >= '1' && code[0] <= '5' {
				attempts++
			}
		}
		out.UpstreamRequests += attempts
		if !line.Time.Before(startTime) && !line.Time.After(now.Add(time.Second)) {
			i := int(line.Time.Sub(startTime) / interval)
			if i >= 0 && i < len(out.Points) {
				out.Points[i].Entries++
				out.Points[i].UpstreamRequests += attempts
			}
		}
		if line.Time.After(out.ObservedAt) {
			out.ObservedAt = line.Time
		}
		switch line.Cache {
		case "HIT", "MISS", "BYPASS", "STALE", "EXPIRED", "UPDATING", "REVALIDATED":
			out.Counts[line.Cache]++
			if line.Cache == "HIT" {
				out.HitResponseBytes += line.Bytes
			}
		}
	}
	if lines.Err() != nil {
		return Traffic{State: "unknown"}
	}
	if out.Entries > 0 {
		out.State = "healthy"
		if now.Sub(out.ObservedAt) > time.Minute {
			out.State = "stale"
		}
	}
	return out
}
