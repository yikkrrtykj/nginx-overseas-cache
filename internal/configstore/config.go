// Package configstore owns the V1 on-disk format and privileged transactions.
package configstore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/yikkrrtykj/nginx-overseas-cache/internal/netguard"
)

type Config struct {
	OriginHost   string `json:"originHost"`
	CDNHost      string `json:"cdnHost"`
	ChannelID    string `json:"channelId"`
	AccountID    string `json:"accountId"`
	TokenPresent bool   `json:"tokenPresent"`
}

type Changes struct {
	OriginHost string `json:"originHost"`
	CDNHost    string `json:"cdnHost"`
	ChannelID  string `json:"channelId"`
	AccountID  string `json:"accountId"`
	Token      string `json:"token"`
}

var idPattern = regexp.MustCompile("^[a-zA-Z0-9._~-]{1,128}$")
var tokenPattern = regexp.MustCompile("^[a-zA-Z0-9._~=-]{8,512}$")

func ValidID(s string) bool    { return idPattern.MatchString(s) }
func ValidToken(s string) bool { return tokenPattern.MatchString(s) }

var settingsPattern = regexp.MustCompile(`(?m)^[ \t]*set[ \t]+\$(render_channel|render_account|render_token)[ \t]+"([^"\r\n]*)"[ \t]*;[ \t]*\r?$`)

func settings(data []byte) (channel, account, token string, err error) {
	values := map[string]string{}
	for _, match := range settingsPattern.FindAllSubmatch(data, -1) {
		key := string(match[1])
		if _, exists := values[key]; exists {
			return "", "", "", errors.New("renderer settings contain duplicate fields")
		}
		values[key] = string(match[2])
	}
	channel, account, token = values["render_channel"], values["render_account"], values["render_token"]
	if !ValidID(channel) || !ValidID(account) || !ValidToken(token) {
		err = errors.New("renderer settings are missing or invalid")
	}
	return
}

type upstreamField struct {
	re  *regexp.Regexp
	cdn bool
}

var upstreamFields = []upstreamField{
	{regexp.MustCompile(`(?ms)(^[ \t]*upstream\s+webcoder_origin\s*\{[^}]*?\bserver\s+)([^\s:;]+)(:443\s*;)`), false},
	{regexp.MustCompile(`(?ms)(^[ \t]*upstream\s+webcoder_cdn\s*\{[^}]*?\bserver\s+)([^\s:;]+)(:443\s*;)`), true},
	{regexp.MustCompile(`(?ms)(^[ \t]*map\s+\$host\s+\$overseas_origin_host\s*\{\s*default\s+)([^\s;]+)(\s*;)`), false},
	{regexp.MustCompile(`(?ms)(^[ \t]*map\s+\$host\s+\$overseas_cdn_host\s*\{\s*default\s+)([^\s;]+)(\s*;)`), true},
}

func readConfig(upstream, render []byte) (Config, string, error) {
	var config Config
	for _, field := range upstreamFields {
		matches := field.re.FindAllSubmatch(upstream, 2)
		if len(matches) != 1 {
			return Config{}, "", errors.New("unexpected Nginx upstream configuration")
		}
		host, err := netguard.Hostname(string(matches[0][2]))
		if err != nil {
			return Config{}, "", errors.New("invalid Nginx upstream hostname")
		}
		old := &config.OriginHost
		if field.cdn {
			old = &config.CDNHost
		}
		if *old != "" && *old != host {
			return Config{}, "", errors.New("inconsistent Nginx upstream and map")
		}
		*old = host
	}
	channel, account, token, err := settings(render)
	if err != nil {
		return Config{}, "", err
	}
	config.ChannelID, config.AccountID, config.TokenPresent = channel, account, token != "REPLACE_WITH_REAL_TOKEN"
	return config, token, nil
}

func updateHosts(input []byte, origin, cdn string) ([]byte, error) {
	for _, field := range upstreamFields {
		matches := field.re.FindAllSubmatchIndex(input, 2)
		if len(matches) != 1 {
			return nil, errors.New("unexpected Nginx upstream configuration")
		}
		index := matches[0]
		host := origin
		if field.cdn {
			host = cdn
		}
		output := make([]byte, 0, len(input)+len(host))
		output = append(output, input[:index[4]]...)
		output = append(output, host...)
		input = append(output, input[index[5]:]...)
	}
	return input, nil
}

func RenderSettings(channel, account, token string) []byte {
	return fmt.Appendf(nil, "# Managed by Overseas Cache. Keep this file mode 0600.\nset $render_channel \"%s\";\nset $render_account \"%s\";\nset $render_token   \"%s\";\n", channel, account, token)
}

func (c Changes) Validate() (Changes, error) {
	var err error
	c.OriginHost, err = netguard.Hostname(c.OriginHost)
	if err != nil {
		return c, errors.New("主站必须填写有效域名")
	}
	c.CDNHost, err = netguard.Hostname(c.CDNHost)
	if err != nil {
		return c, errors.New("CDN 必须填写有效域名")
	}
	c.ChannelID, c.AccountID, c.Token = strings.TrimSpace(c.ChannelID), strings.TrimSpace(c.AccountID), strings.TrimSpace(c.Token)
	if !ValidID(c.ChannelID) || !ValidID(c.AccountID) {
		return c, errors.New("channelId 和 accountId 格式不正确")
	}
	if c.Token != "" && !ValidToken(c.Token) {
		return c, errors.New("Token 格式不正确，只允许 URL 安全字符")
	}
	return c, nil
}

type Result struct {
	Config  Config `json:"config"`
	Backup  string `json:"backup,omitempty"`
	Changed bool   `json:"changed"`
}

type Manager interface {
	Get(context.Context) (Result, error)
	Apply(context.Context, Changes) (Result, error)
	Rollback(context.Context, string) (Result, error)
}
