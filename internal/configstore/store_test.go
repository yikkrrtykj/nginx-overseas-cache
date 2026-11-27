package configstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	s := &Store{UpstreamPath: filepath.Join(root, "proxy-cache.conf"), SettingsPath: filepath.Join(root, "settings.conf"), BackupRoot: root, LockPath: filepath.Join(root, "lock"),
		Validate: func(context.Context) error { return nil }, Reload: func(context.Context) error { return nil }}
	config, err := os.ReadFile("../../nginx/conf.d/proxy-cache.conf")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(s.UpstreamPath, config, 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(s.SettingsPath, RenderSettings("before", "account", "fixture-token-old"), 0600); err != nil {
		t.Fatal(err)
	}
	return s
}
func draft() Changes {
	return Changes{OriginHost: "origin.example", CDNHost: "cdn.example", ChannelID: "after", AccountID: "account"}
}
func TestApplyRetainsTokenAndExplicitRollback(t *testing.T) {
	s := testStore(t)
	before, err := s.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Apply(t.Context(), draft())
	if err != nil || !after.Changed || !after.Config.TokenPresent {
		t.Fatalf("apply: %+v %v", after, err)
	}
	data, _ := os.ReadFile(s.SettingsPath)
	if !strings.Contains(string(data), "fixture-token-old") {
		t.Fatal("blank token was not preserved")
	}
	actual, err := s.Get(t.Context())
	if err != nil || actual.Config != after.Config {
		t.Fatalf("config mismatch: %+v %v", actual, err)
	}
	if _, err := s.Rollback(t.Context(), "../../escape"); err == nil {
		t.Fatal("path escape accepted")
	}
	if result, err := s.Rollback(t.Context(), after.Backup); err != nil || result.Config != before.Config {
		t.Fatalf("rollback %+v %v", result, err)
	}
}
func TestReloadFailureAndCrashRecovery(t *testing.T) {
	s := testStore(t)
	before, err := s.getState()
	if err != nil {
		t.Fatal(err)
	}
	failures := 0
	s.Reload = func(context.Context) error {
		failures++
		if failures <= 2 {
			return errors.New("injected reload failure")
		}
		return nil
	}
	result, err := s.Apply(t.Context(), draft())
	if !errors.Is(err, ErrRecoveryFailed) || result.Backup == "" {
		t.Fatalf("missing recovery journal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.BackupRoot, "pending")); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.BackupRoot, "pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pending marker remains")
	}
	after, err := s.getState()
	if err != nil || string(before.upstream) != string(after.upstream) || string(before.settings) != string(after.settings) {
		t.Fatal("old files were not restored")
	}
	s.Validate = func(context.Context) error {
		failures++
		if failures == 4 {
			return errors.New("injected nginx -t failure")
		}
		return nil
	}
	if _, err := s.Apply(t.Context(), draft()); !errors.Is(err, ErrRecovered) {
		t.Fatalf("expected verified recovery, got %v", err)
	}
}
