package admin

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yikkrrtykj/nginx-overseas-cache/internal/atomicfile"
)

const sessionTTL = 8 * time.Hour
const nonceTTL = 10 * time.Minute
const passwordIterations = 600000

var usernamePattern = regexp.MustCompile("^[a-zA-Z0-9._-]{1,64}$")

type passwordRecord struct {
	Version    int    `json:"version"`
	Username   string `json:"username"`
	Algorithm  string `json:"algorithm"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Digest     string `json:"digest"`
}

func passwordAllowed(s string) bool {
	return len(s) <= 1024 && utf8.ValidString(s) && utf8.RuneCountInString(s) >= 12 && utf8.RuneCountInString(s) <= 256
}

func InitPassword(path, username, password string) error {
	if !usernamePattern.MatchString(username) || !passwordAllowed(password) {
		return errors.New("用户名格式不正确或密码长度不足（12–256 个字符）")
	}
	var salt [32]byte
	rand.Read(salt[:])
	digest, err := pbkdf2.Key(sha256.New, password, salt[:], passwordIterations, 32)
	if err != nil {
		return err
	}
	record := passwordRecord{1, username, "pbkdf2-sha256", passwordIterations, hex.EncodeToString(salt[:]), hex.EncodeToString(digest)}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0600)
}

func readPassword(path string) (passwordRecord, error) {
	var record passwordRecord
	data, _, err := readLocal(path, 16<<10)
	if err != nil || json.Unmarshal(data, &record) != nil {
		return record, errors.New("无法读取管理员密码文件")
	}
	salt, saltErr := hex.DecodeString(record.Salt)
	digest, digestErr := hex.DecodeString(record.Digest)
	if record.Version != 1 || record.Algorithm != "pbkdf2-sha256" || !usernamePattern.MatchString(record.Username) ||
		record.Iterations < 200000 || record.Iterations > 2000000 || saltErr != nil || digestErr != nil ||
		len(salt) < 16 || len(salt) > 1024 || len(digest) != 32 {
		return record, errors.New("管理员密码文件格式无效")
	}
	return record, nil
}

func (r passwordRecord) verify(username, password string) bool {
	if len(password) > 1024 || len(username) > 64 {
		return false
	}
	salt, _ := hex.DecodeString(r.Salt)
	expected, _ := hex.DecodeString(r.Digest)
	actual, err := pbkdf2.Key(sha256.New, password, salt, r.Iterations, 32)
	if err != nil {
		return false
	}
	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(r.Username))
	passwordMatch := subtle.ConstantTimeCompare(actual, expected)
	return usernameMatch&passwordMatch == 1
}

func secret() string {
	var data [32]byte
	rand.Read(data[:])
	return base64.RawURLEncoding.EncodeToString(data[:])
}

type credential struct {
	session string
	expires time.Time
}
type attempts struct {
	count   int
	expires time.Time
}

type authentication struct {
	passwordFile string
	mu           sync.Mutex
	passwordMu   sync.RWMutex
	sessions     map[string]time.Time
	nonces       map[string]credential
	failures     map[string]attempts
	workers      chan struct{}
}

func newAuth(path string) (*authentication, error) {
	if _, err := readPassword(path); err != nil {
		return nil, err
	}
	return &authentication{
		passwordFile: path, sessions: make(map[string]time.Time), nonces: make(map[string]credential),
		failures: make(map[string]attempts), workers: make(chan struct{}, 2),
	}, nil
}

func (a *authentication) login(ip, username, password string) (token string, retry int) {
	select {
	case a.workers <- struct{}{}:
		defer func() { <-a.workers }()
	default:
		return "", 1
	}

	a.passwordMu.RLock()
	defer a.passwordMu.RUnlock()
	now := time.Now()
	a.mu.Lock()
	for key, item := range a.failures {
		if now.After(item.expires) {
			delete(a.failures, key)
		}
	}
	item, exists := a.failures[ip]
	if !exists && len(a.failures) >= 4096 {
		a.mu.Unlock()
		return "", 60
	}
	if item.count >= 5 {
		a.mu.Unlock()
		return "", max(1, int(time.Until(item.expires).Seconds()))
	}
	// Reserve the attempt before starting the expensive hash.
	a.failures[ip] = attempts{item.count + 1, now.Add(time.Minute)}
	a.mu.Unlock()
	record, err := readPassword(a.passwordFile)
	if err != nil || !record.verify(username, password) {
		return "", 0
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.failures, ip)
	for token, expiry := range a.sessions {
		if !now.Before(expiry) {
			delete(a.sessions, token)
		}
	}
	if len(a.sessions) >= 1024 {
		return "", 60
	}
	token = secret()
	a.sessions[token] = now.Add(sessionTTL)
	return token, 0
}

func (a *authentication) valid(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Now().Before(a.sessions[token])
}

func (a *authentication) csrf(token string) string {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if !now.Before(a.sessions[token]) {
		return ""
	}
	for key, item := range a.nonces {
		if now.After(item.expires) || !now.Before(a.sessions[item.session]) {
			delete(a.nonces, key)
		}
	}
	if len(a.nonces) >= 4096 {
		for key, item := range a.nonces {
			if item.session == token {
				delete(a.nonces, key)
				break
			}
		}
	}
	if len(a.nonces) >= 4096 {
		return ""
	}
	nonce := secret()
	a.nonces[nonce] = credential{token, now.Add(nonceTTL)}
	return nonce
}

func (a *authentication) consume(token, nonce string) bool {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	item := a.nonces[nonce]
	if item.session != token || !now.Before(item.expires) || !now.Before(a.sessions[token]) {
		return false
	}
	delete(a.nonces, nonce)
	return true
}

func (a *authentication) logout(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, token)
}

func (a *authentication) change(token, current, next string) error {
	if !passwordAllowed(next) || subtle.ConstantTimeCompare([]byte(current), []byte(next)) == 1 {
		return errors.New("新密码需为 12–256 个字符，且与当前密码不同")
	}
	select {
	case a.workers <- struct{}{}:
		defer func() { <-a.workers }()
	default:
		return errors.New("认证繁忙，请稍后重试")
	}
	a.passwordMu.Lock()
	defer a.passwordMu.Unlock()
	if !a.valid(token) {
		return errors.New("会话已失效，请重新登录")
	}
	record, err := readPassword(a.passwordFile)
	if err != nil || !record.verify(record.Username, current) {
		return errors.New("当前密码不正确")
	}
	if err := InitPassword(a.passwordFile, record.Username, next); err != nil {
		return errors.New("无法保存管理员密码")
	}
	a.mu.Lock()
	clear(a.sessions)
	clear(a.nonces)
	a.mu.Unlock()
	return nil
}
