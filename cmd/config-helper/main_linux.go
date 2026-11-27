package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yikkrrtykj/nginx-overseas-cache/internal/configstore"
	"github.com/yikkrrtykj/nginx-overseas-cache/internal/netguard"
)

var version = "dev"

func command(ctx context.Context, path string, args ...string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin"}
	return cmd.Run()
}

func check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if err := command(ctx, "/usr/sbin/nginx", "-t", "-q"); err != nil {
		log.Println("nginx configuration test failed")
		return errors.New("Nginx check failed")
	}
	return nil
}

func children() (string, map[string]bool) {
	pidData, _ := os.ReadFile("/run/nginx.pid")
	pid := strings.TrimSpace(string(pidData))
	if _, err := strconv.Atoi(pid); err != nil {
		return "", nil
	}
	kids, _ := os.ReadFile("/proc/" + pid + "/task/" + pid + "/children")
	processes := map[string]bool{}
	for _, child := range strings.Fields(string(kids)) {
		processes[child] = true
	}
	return pid, processes
}

func reload(ctx context.Context) error {
	pid, before := children()
	if pid == "" || command(ctx, "/usr/bin/systemctl", "reload", "nginx.service") != nil {
		log.Println("nginx reload command failed")
		return errors.New("Nginx reload failed")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		current, after := children()
		if current != pid {
			return errors.New("Nginx master changed during reload")
		}
		for id := range after {
			if !before[id] && command(waitCtx, "/usr/bin/systemctl", "is-active", "--quiet", "nginx.service") == nil {
				return nil
			}
		}
		select {
		case <-waitCtx.Done():
			log.Println("nginx did not report a fresh worker")
			return errors.New("Nginx reload could not be confirmed")
		case <-tick.C:
		}
	}
}

func hostname(ctx context.Context, host string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 || len(addresses) > 32 {
		return errors.New("unable to resolve upstream")
	}
	for _, addr := range addresses {
		if !netguard.Public(addr) {
			return errors.New("non-public address")
		}
	}
	return nil
}

func main() {
	printVersion := flag.Bool("version", false, "print build version")
	recoverOnly := flag.Bool("recover", false, "recover any pending transaction and exit")
	origin := flag.String("set-origin", "", "update only the upstream hostnames")
	cdn := flag.String("set-cdn", "", "CDN hostname for --set-origin")
	flag.Parse()
	if *printVersion {
		println(version)
		return
	}
	if os.Geteuid() != 0 {
		log.Fatal("configuration helper requires root")
	}

	s := &configstore.Store{
		UpstreamPath: "/etc/nginx/conf.d/proxy-cache.conf",
		SettingsPath: "/etc/nginx/overseas-cache/settings.conf",
		BackupRoot:   "/etc/nginx/overseas-cache/backups",
		LockPath:     "/etc/nginx/overseas-cache/.config.lock",
		Validate:     check, Reload: reload, ValidateHost: hostname,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	recoverCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	err := s.Recover(recoverCtx)
	cancel()
	if err != nil {
		log.Fatal(err)
	}
	if *recoverOnly {
		return
	}
	if *origin != "" {
		result, err := s.ChangeHosts(ctx, *origin, *cdn)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("upstream host update completed changed=%t backup=%s", result.Changed, result.Backup)
		return
	}
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) || os.Getenv("LISTEN_FDS") != "1" {
		log.Fatal("configuration helper requires one systemd-activated socket")
	}
	file := os.NewFile(3, "systemd configuration socket")
	listener, err := net.FileListener(file)
	file.Close()
	if err != nil || listener.Addr().Network() != "unix" {
		log.Fatal("invalid configuration socket")
	}

	server := &http.Server{Handler: configstore.RPC(s), MaxHeaderBytes: 8 << 10, ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout: 5 * time.Second, WriteTimeout: 78 * time.Second, IdleTimeout: 5 * time.Second}
	shutdown := make(chan error, 1)
	go func() {
		<-ctx.Done()
		closeCtx, cancel := context.WithTimeout(context.Background(), 76*time.Second)
		defer cancel()
		shutdown <- server.Shutdown(closeCtx)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	// Shutdown waits for active handlers. Startup recovers any transaction
	// left on disk. Taking the file lock here would deadlock an installer
	// that already holds the lock and is stopping the helper.
	if ctx.Err() != nil {
		if err := <-shutdown; err != nil {
			log.Printf("configuration helper shutdown: %v", err)
		}
	}
}
