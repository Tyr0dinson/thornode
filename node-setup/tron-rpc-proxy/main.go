package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func run(configFile, command string) error {
	c, err := readConfig(configFile)
	if err != nil {
		return err
	}
	if command != "serve" {
		if command != "status" && command != "external" && command != "arm-local" {
			return errors.New("unsupported operator command")
		}
		client := &http.Client{Timeout: 90 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", c.AdminSocket)
		}}}
		method := "POST"
		if command == "status" {
			method = "GET"
		}
		req, _ := http.NewRequest(method, "http://operator/"+command, nil)
		resp, err := client.Do(req)
		if err != nil {
			return errors.New("operator socket unavailable")
		}
		defer resp.Body.Close()
		_, _ = io.Copy(os.Stdout, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode != 200 {
			return errors.New("operator command failed")
		}
		return nil
	}
	localAPI, err := endpoint(c.LocalAPI, false)
	if err != nil {
		return err
	}
	localRPC, err := endpoint(c.LocalRPC, false)
	if err != nil {
		return err
	}
	externalAPI, err := endpointFile(c.ExternalAPIFile)
	if err != nil {
		return err
	}
	externalRPC, err := endpointFile(c.ExternalRPCFile)
	if err != nil {
		return err
	}
	s, err := openStore(c.Database, c.Genesis)
	if err != nil {
		return err
	}
	defer s.db.Close()
	p := newProxy(c, s, newUpstream(localAPI, localRPC, c.timeout()), newUpstream(externalAPI, externalRPC, c.timeout()))
	// Restart always requires a new explicit local admission, never automatic failback.
	if !p.setModeFor("external", "startup", reasonStartup, "operator") {
		return errStore
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err = os.MkdirAll(filepath.Dir(c.AdminSocket), 0700); err != nil {
		return errors.New("operator directory unavailable")
	}
	if info, err := os.Lstat(c.AdminSocket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("operator socket path is not a socket")
		}
		if os.Remove(c.AdminSocket) != nil {
			return errors.New("operator socket cleanup failed")
		}
	}
	adminListener, err := net.Listen("unix", c.AdminSocket)
	if err != nil {
		return errors.New("operator listener unavailable")
	}
	defer adminListener.Close()
	if os.Chmod(c.AdminSocket, 0600) != nil {
		return errors.New("operator socket permissions failed")
	}
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return errors.New("HTTP listener unavailable")
	}
	defer listener.Close()
	server := &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return ctx }}
	admin := &http.Server{Handler: http.HandlerFunc(p.admin), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 90 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	failed := make(chan error, 2)
	go func() { failed <- server.Serve(listener) }()
	go func() { failed <- admin.Serve(adminListener) }()
	go func() { p.probe(ctx); p.monitor(ctx) }()
	log.Printf("proxy started: read_only=%t; routing=external", !c.BroadcastEnabled)
	select {
	case <-ctx.Done():
	case <-failed:
		cancel()
	}
	shutdown, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	_ = server.Shutdown(shutdown)
	_ = admin.Shutdown(shutdown)
	log.Print("proxy stopped")
	return nil
}

func main() {
	configFile := flag.String("config", "/etc/tron-proxy/config.json", "configuration file")
	command := flag.String("command", "serve", "serve, status, external, arm-local")
	flag.Parse()
	if err := run(*configFile, *command); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
