package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/yikkrrtykj/nginx-overseas-cache/internal/fanout"
)

var version = "dev"

func env(key, defaultValue string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return defaultValue
}

func intEnv(key string, fallback int) int {
	value, err := strconv.Atoi(env(key, strconv.Itoa(fallback)))
	if err != nil {
		log.Fatal("invalid ", key)
	}
	return value
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(env(key, fallback.String()))
	if err != nil {
		log.Fatal("invalid ", key)
	}
	return value
}

func main() {
	printVersion := flag.Bool("version", false, "print build version")
	flag.Parse()
	if *printVersion {
		println(version)
		return
	}

	config := fanout.DefaultConfig()
	config.MaxClients = intEnv("MAX_CLIENTS", config.MaxClients)
	config.MaxGroups = intEnv("MAX_GROUPS", config.MaxGroups)
	config.MaxMessages = intEnv("MAX_QUEUE_MESSAGES", config.MaxMessages)
	config.MaxMessageBytes = intEnv("MAX_MESSAGE_BYTES", config.MaxMessageBytes)
	config.QueueBytes = intEnv("MAX_QUEUE_BYTES", config.QueueBytes)
	config.TotalQueueBytes = intEnv("MAX_TOTAL_QUEUE_BYTES", config.TotalQueueBytes)
	config.JoinTimeout = durationEnv("JOIN_TIMEOUT", config.JoinTimeout)
	config.WriteTimeout = durationEnv("WRITE_TIMEOUT", config.WriteTimeout)
	config.PingInterval = durationEnv("PING_INTERVAL", config.PingInterval)
	config.IdleTimeout = durationEnv("IDLE_TIMEOUT", config.IdleTimeout)
	config.ReconnectMin = durationEnv("RECONNECT_MIN", config.ReconnectMin)
	config.ReconnectMax = durationEnv("RECONNECT_MAX", config.ReconnectMax)
	config.UpstreamBase = env("UPSTREAM_BASE", "")
	config.UpstreamOrigin = env("UPSTREAM_ORIGIN", "")
	s, err := fanout.New(config, nil)
	if err != nil {
		log.Fatal(err)
	}
	host := env("LISTEN_HOST", "127.0.0.1")
	ip, err := netip.ParseAddr(host)
	port := intEnv("LISTEN_PORT", 3001)
	if err != nil || !ip.IsLoopback() || port < 1 || port > 65535 {
		log.Fatal("fanout requires a loopback listen address")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	statusPath := env("STATUS_FILE", "/run/ws-fanout/status.json")
	seconds, err := strconv.ParseFloat(env("STATUS_INTERVAL", "2"), 64)
	if err != nil || !(seconds >= 1 && seconds <= 5) {
		log.Fatal("STATUS_INTERVAL must be between 1 and 5 seconds")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8 << 10, IdleTimeout: 15 * time.Second}
	exited := make(chan error, 1)
	go func() { exited <- server.Serve(listener) }()

	publish := func() {
		if err := s.Publish(statusPath); err != nil {
			log.Println("fanout status publish failed")
		}
	}
	publish()
	ticker := time.NewTicker(time.Duration(seconds * float64(time.Second)))
	defer ticker.Stop()
	log.Printf("ws-fanout %s listening on %s", version, listener.Addr())
wait:
	for {
		select {
		case <-ticker.C:
			publish()
		case <-ctx.Done():
			break wait
		case err := <-exited:
			if !errors.Is(err, http.ErrServerClosed) {
				log.Fatal(err)
			}
			break wait
		}
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := s.Close(closeCtx); err != nil {
		log.Printf("fanout shutdown: %v", err)
	}
	if err := server.Shutdown(closeCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	os.Remove(statusPath)
}
