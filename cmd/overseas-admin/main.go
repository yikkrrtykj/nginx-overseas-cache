package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/yikkrrtykj/nginx-overseas-cache/internal/admin"
	"github.com/yikkrrtykj/nginx-overseas-cache/internal/configstore"
	"github.com/yikkrrtykj/nginx-overseas-cache/web"
)

var version = "dev"

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

var healthClient = &http.Client{
	Timeout:       800 * time.Millisecond,
	Transport:     &http.Transport{MaxResponseHeaderBytes: 2048},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func nginxHealth(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:23334/healthz", nil)
	if err != nil {
		return false
	}
	res, err := healthClient.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == 204
}

func main() {
	host := flag.String("host", env("OVERSEAS_ADMIN_HOST", "127.0.0.1"), "management bind IP")
	port := flag.Int("port", 23000, "management port")
	cert := flag.String("tls-cert", env("OVERSEAS_ADMIN_TLS_CERT", ""), "TLS certificate path")
	key := flag.String("tls-key", env("OVERSEAS_ADMIN_TLS_KEY", ""), "TLS key path")
	printVersion := flag.Bool("version", false, "print build version")
	initPassword := flag.String("init-password-file", "", "read password from stdin")
	username := flag.String("username", "admin", "username for initialization")
	flag.Parse()
	if *printVersion {
		println(version)
		return
	}
	if *initPassword != "" {
		password, err := io.ReadAll(io.LimitReader(os.Stdin, 1025))
		if err != nil {
			log.Fatal("unable to read password")
		}
		if err := admin.InitPassword(*initPassword, *username, string(password)); err != nil {
			log.Fatal(err)
		}
		return
	}
	ip, err := netip.ParseAddr(*host)
	if err != nil || *port < 1 || *port > 65535 {
		log.Fatal("invalid management listen address")
	}
	if (*cert == "") != (*key == "") {
		log.Fatal("configure both TLS certificate and key")
	}
	if *cert == "" && !ip.IsLoopback() && env("OVERSEAS_ADMIN_ALLOW_HTTP", "") != "yes" {
		log.Fatal("non-loopback Admin requires TLS or explicit trusted LAN HTTP mode")
	}

	handler, err := admin.New(admin.Options{
		PasswordFile:    env("OVERSEAS_ADMIN_PASSWORD_FILE", "/var/lib/overseas-cache-admin/admin-password.json"),
		StatusFile:      env("OVERSEAS_FANOUT_STATUS", "/run/ws-fanout/status.json"),
		TrafficFile:     env("OVERSEAS_TRAFFIC_LOG", "/var/log/overseas-cache/traffic.log"),
		AllowedHosts:    env("OVERSEAS_ADMIN_ALLOWED_HOSTS", ""),
		AllowedNetworks: env("OVERSEAS_ADMIN_ALLOWED_NETWORKS", admin.DefaultNetworks),
		Config:          configstore.NewClient(env("OVERSEAS_CONFIG_SOCKET", "/run/overseas-cache-config.sock")),
		Assets:          web.Assets(),
		NginxHealth:     nginxHealth,
	})
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr: net.JoinHostPort(*host, strconv.Itoa(*port)), Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 82 * time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	exited := make(chan error, 1)
	go func() {
		if *cert != "" {
			exited <- server.ListenAndServeTLS(*cert, *key)
		} else {
			exited <- server.ListenAndServe()
		}
	}()
	log.Printf("overseas-admin %s listening on %s", version, server.Addr)
	select {
	case err := <-exited:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case <-ctx.Done():
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(closeCtx); err != nil {
			log.Printf("admin shutdown: %v", err)
		}
	}
}
