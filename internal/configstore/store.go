package configstore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yikkrrtykj/nginx-overseas-cache/internal/atomicfile"
)

type Store struct {
	UpstreamPath, SettingsPath, BackupRoot, LockPath string
	InstallationJournalPath                         string
	Validate, Reload                                 func(context.Context) error
	ValidateHost                                     func(context.Context, string) error
	mu                                               sync.Mutex
}

var ErrRecovered = errors.New("应用失败，已恢复原配置")
var ErrRecoveryFailed = errors.New("应用失败且自动恢复未完成，请检查服务日志和备份")
var ErrInstallationPending = errors.New("安装事务待恢复，配置服务暂不可用")
var backupID = regexp.MustCompile("^(admin-|install-)?[0-9]{8}T[0-9]{6}Z([-_.][0-9A-Za-z_-]{1,96})?$")

func readBounded(path string, limit int64) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > limit {
		return nil, errors.New("invalid config file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("cannot read config file")
	}
	return data, nil
}

type state struct{ upstream, settings []byte }

func readState(upstream, settings string) (state, error) {
	u, err := readBounded(upstream, 1<<20)
	if err != nil {
		return state{}, err
	}
	r, err := readBounded(settings, 16<<10)
	return state{u, r}, err
}

func (s *Store) getState() (state, error) { return readState(s.UpstreamPath, s.SettingsPath) }

func (s *Store) Get(ctx context.Context) (Result, error) {
	var result Result
	err := s.lock(ctx, func() error {
		current, err := s.getState()
		if err != nil {
			return errors.New("无法读取当前配置")
		}
		result.Config, _, err = readConfig(current.upstream, current.settings)
		return err
	})
	return result, err
}

func (s *Store) Apply(ctx context.Context, changes Changes) (Result, error) {
	changes, err := changes.Validate()
	if err != nil {
		return Result{}, err
	}
	return s.apply(ctx, func(Config) (Changes, error) { return changes, nil })
}

func (s *Store) ChangeHosts(ctx context.Context, origin, cdn string) (Result, error) {
	return s.apply(ctx, func(previous Config) (Changes, error) {
		if cdn == "" {
			cdn = previous.CDNHost
		}
		return (Changes{OriginHost: origin, CDNHost: cdn, ChannelID: previous.ChannelID, AccountID: previous.AccountID}).Validate()
	})
}

func (s *Store) validateHosts(ctx context.Context, hosts ...string) error {
	if s.ValidateHost == nil {
		return nil
	}
	for _, host := range hosts {
		if err := s.ValidateHost(ctx, host); err != nil {
			return errors.New("源站和 CDN 必须解析到公网地址")
		}
	}
	return nil
}

func (s *Store) apply(ctx context.Context, resolve func(Config) (Changes, error)) (Result, error) {
	var result Result
	err := s.lock(ctx, func() error {
		before, err := s.getState()
		if err != nil {
			return errors.New("无法读取当前配置")
		}
		old, token, err := readConfig(before.upstream, before.settings)
		if err != nil {
			return err
		}
		changes, err := resolve(old)
		if err != nil {
			return err
		}
		if err := s.validateHosts(ctx, changes.OriginHost, changes.CDNHost); err != nil {
			return err
		}
		if changes.Token == "" {
			changes.Token = token
		}
		next := Config{OriginHost: changes.OriginHost, CDNHost: changes.CDNHost, ChannelID: changes.ChannelID, AccountID: changes.AccountID, TokenPresent: changes.Token != "REPLACE_WITH_REAL_TOKEN"}
		result.Config = next
		if old == next && token == changes.Token {
			return nil
		}
		upstream, err := updateHosts(before.upstream, changes.OriginHost, changes.CDNHost)
		if err != nil {
			return err
		}
		after := state{upstream, RenderSettings(changes.ChannelID, changes.AccountID, changes.Token)}
		result.Backup, err = s.transact(before, after)
		result.Changed = err == nil
		return err
	})
	return result, err
}

func (s *Store) Rollback(ctx context.Context, id string) (Result, error) {
	if !backupID.MatchString(id) {
		return Result{}, errors.New("无效的备份标识")
	}
	var result Result
	err := s.lock(ctx, func() error {
		before, err := s.getState()
		if err != nil {
			return errors.New("无法读取当前配置")
		}
		backup, err := s.backup(id)
		if err != nil {
			return errors.New("备份不可用")
		}
		// V1 backups may contain an older Nginx template. Restore only the
		// renderer fields and upstream names into the installed security policy.
		target, token, err := readConfig(backup.upstream, backup.settings)
		if err != nil {
			return errors.New("备份配置不可用")
		}
		result.Config = target
		if s.ValidateHost != nil {
			for _, host := range []string{target.OriginHost, target.CDNHost} {
				if err := s.ValidateHost(ctx, host); err != nil {
					return errors.New("备份中的域名未解析到公网地址")
				}
			}
		}
		upstream, err := updateHosts(before.upstream, target.OriginHost, target.CDNHost)
		if err != nil {
			return err
		}
		result.Backup, err = s.transact(before, state{upstream, RenderSettings(target.ChannelID, target.AccountID, token)})
		result.Changed = err == nil
		return err
	})
	return result, err
}

func (s *Store) lock(ctx context.Context, operation func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := lockFile(ctx, s.LockPath)
	if err != nil {
		return errors.New("配置锁不可用")
	}
	defer unlock()
	// Install and configuration operations share LockPath. Check the journal
	// while holding it, before reading or recovering managed configuration.
	if s.InstallationJournalPath != "" {
		if _, err := os.Lstat(s.InstallationJournalPath); !errors.Is(err, os.ErrNotExist) {
			return ErrInstallationPending
		}
	}
	if err := s.recover(); err != nil {
		return ErrRecoveryFailed
	}
	return operation()
}

func (s *Store) Recover(ctx context.Context) error {
	return s.lock(ctx, func() error { return nil })
}

func (s *Store) backup(id string) (state, error) {
	dir := filepath.Join(s.BackupRoot, id)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return state{}, errors.New("invalid backup directory")
	}
	return readState(filepath.Join(dir, "proxy-cache.conf"), filepath.Join(dir, "settings.conf"))
}

func (s *Store) write(original state) error {
	if err := atomicfile.Write(s.UpstreamPath, original.upstream, 0644); err != nil {
		return err
	}
	return atomicfile.Write(s.SettingsPath, original.settings, 0600)
}

func (s *Store) lifecycle() error {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if err := s.Validate(ctx); err != nil {
		return err
	}
	return s.Reload(ctx)
}

func (s *Store) transact(before, after state) (string, error) {
	dir, err := os.MkdirTemp(s.BackupRoot, time.Now().UTC().Format("admin-20060102T150405Z-"))
	if err != nil {
		return "", errors.New("创建配置备份失败")
	}
	id := filepath.Base(dir)
	if err := atomicfile.Write(filepath.Join(dir, "proxy-cache.conf"), before.upstream, 0600); err != nil {
		return "", errors.New("创建配置备份失败")
	}
	if err := atomicfile.Write(filepath.Join(dir, "settings.conf"), before.settings, 0600); err != nil {
		return "", errors.New("创建配置备份失败")
	}
	marker := filepath.Join(s.BackupRoot, "pending")
	if err := atomicfile.Write(marker, []byte(id), 0600); err != nil {
		return "", errors.New("记录配置事务失败")
	}

	err = s.write(after)
	if err == nil {
		err = s.lifecycle()
	}
	if err == nil {
		err = atomicfile.Remove(marker)
	}
	if err == nil {
		return id, nil
	}

	if err := s.restore(before, marker); err != nil {
		return id, ErrRecoveryFailed
	}
	return id, ErrRecovered
}

func (s *Store) restore(before state, marker string) error {
	if err := s.write(before); err != nil {
		return err
	}
	if err := s.lifecycle(); err != nil {
		return err
	}
	return atomicfile.Remove(marker)
}

func (s *Store) recover() error {
	marker := filepath.Join(s.BackupRoot, "pending")
	raw, err := readBounded(marker, 128)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	id := strings.TrimSpace(string(raw))
	if !backupID.MatchString(id) {
		return errors.New("invalid pending transaction")
	}
	before, err := s.backup(id)
	if err != nil {
		return err
	}
	return s.restore(before, marker)
}
