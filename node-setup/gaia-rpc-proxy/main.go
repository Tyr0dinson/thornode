package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	Listen         string `json:"listen"`
	PrimaryRPCFile string `json:"primary_rpc_file"`
	ArchiveRPC     string `json:"archive_rpc"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func endpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid upstream URL")
	}
	return u, nil
}

func readConfig(filename string) (Config, error) {
	c := Config{Listen: "127.0.0.1:8080", TimeoutSeconds: 10}
	b, err := os.ReadFile(filename)
	if err != nil || uniqueJSON(b) != nil {
		return c, errors.New("cannot read configuration")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || c.Listen == "" || strings.TrimSpace(c.PrimaryRPCFile) == "" || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 30 {
		return c, errors.New("invalid configuration")
	}
	if _, err := endpoint(c.ArchiveRPC); err != nil {
		return c, err
	}
	return c, nil
}

func handler(g *GaiaProxy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.RawQuery == "" {
			switch r.URL.Path {
			case "/healthz":
				reply(w, []byte(`{"alive":true}`))
				return
			case "/readyz":
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
				defer cancel()
				_, e, err := g.call(ctx, g.primary, gaiaReadRequest("status", 0))
				if err != nil || e.Error != nil || *e.Result.SyncInfo.CatchingUp || e.Result.SyncInfo.Time.After(g.now().Add(gaiaFutureSkew)) || !g.now().Before(e.Result.SyncInfo.Time.Add(gaiaMaxHeadAge)) {
					failure(w, 503, "primary not ready")
					return
				}
				reply(w, []byte(`{"ready":true}`))
				return
			case "/metrics":
				w.Header().Set("Content-Type", "text/plain; version=0.0.4")
				g.Metrics(w)
				return
			}
		}
		g.ServeHTTP(w, r)
	})
}

func run(filename string) error {
	c, err := readConfig(filename)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(c.PrimaryRPCFile)
	if err != nil {
		return errors.New("primary credential file unavailable")
	}
	primary, err := endpoint(string(raw))
	if err != nil {
		return err
	}
	archive, err := endpoint(c.ArchiveRPC)
	if err != nil {
		return err
	}
	g := newGaiaProxy(primary, archive, time.Duration(c.TimeoutSeconds)*time.Second)
	defer g.client.CloseIdleConnections()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := &http.Server{Addr: c.Listen, Handler: handler(g), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe() }()
	log.Print("GAIA read-only proxy started")
	select {
	case <-ctx.Done():
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("GAIA listener failed")
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return s.Shutdown(shutdown)
}

func main() {
	filename := flag.String("config", "/etc/gaia-proxy/config.json", "configuration file")
	flag.Parse()
	if err := run(*filename); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
