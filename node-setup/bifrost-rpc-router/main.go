package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	"runtime/debug"
	"strconv"
	"syscall"
	"time"
)

func listenAdmin(filename string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, errors.New("cannot provision private admin directory")
	}
	info, err := os.Stat(filepath.Dir(filename))
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("admin directory must exist and be private (0700)")
	}
	if st, err := os.Lstat(filename); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("admin path is not a socket")
		}
		if err := os.Remove(filename); err != nil {
			return nil, errors.New("cannot remove old socket")
		}
	} else if !os.IsNotExist(err) {
		return nil, errors.New("cannot inspect admin socket")
	}
	l, err := net.Listen("unix", filename)
	if err != nil {
		return nil, errors.New("cannot listen on admin socket")
	}
	if err := os.Chmod(filename, 0600); err != nil {
		l.Close()
		return nil, errors.New("cannot secure admin socket")
	}
	return l, nil
}

func serve(ctx context.Context, r *router) error {
	c := r.current.Load().catalog.config
	admin, err := listenAdmin(c.AdminSocket)
	if err != nil {
		return err
	}
	defer admin.Close()
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return errors.New("cannot listen on data port")
	}
	defer listener.Close()
	maxTimeout := 0
	for _, ch := range c.Chains {
		if ch.TimeoutSeconds > maxTimeout {
			maxTimeout = ch.TimeoutSeconds
		}
	}
	dataServer := &http.Server{Handler: r, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: time.Duration(maxTimeout+5) * time.Second, WriteTimeout: time.Duration(maxTimeout+5) * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	adminServer := &http.Server{Handler: http.HandlerFunc(r.admin), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan error, 2)
	go func() { done <- dataServer.Serve(listener) }()
	go func() { done <- adminServer.Serve(admin) }()
	log.Print("event=router_started")
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-done:
	}
	// Stop configuration mutations first, then drain existing data requests.
	shutdown, cancel := context.WithTimeout(context.Background(), time.Duration(maxTimeout+10)*time.Second)
	defer cancel()
	if err := adminServer.Shutdown(shutdown); err != nil {
		adminServer.Close()
	}
	if err := dataServer.Shutdown(shutdown); err != nil {
		dataServer.Close()
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return errors.New("router listener failed")
	}
	return nil
}

func adminCommand(socket, command, chain, backend string, revision uint64) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	method, endpoint := http.MethodGet, "/routes"
	var body []byte
	switch command {
	case "status":
	case "switch":
		if revision == 0 || !identifier.MatchString(chain) || !identifier.MatchString(backend) {
			return errors.New("switch needs -chain, -backend and current -revision")
		}
		method, endpoint = http.MethodPost, "/switch"
		body, _ = json.Marshal(map[string]string{"chain": chain, "backend": backend})
	case "reload":
		if revision == 0 {
			return errors.New("reload needs current -revision")
		}
		method, endpoint = http.MethodPost, "/reload"
		body = []byte("{}")
	default:
		return errors.New("unknown command")
	}
	req, _ := http.NewRequest(method, "http://admin"+endpoint, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", strconv.Quote(strconv.FormatUint(revision, 10)))
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("admin request failed; reread status before retrying")
	}
	defer resp.Body.Close()
	_, err = io.Copy(os.Stdout, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != 200 {
		return fmt.Errorf("admin rejected command: HTTP %d", resp.StatusCode)
	}
	return err
}

func main() {
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(256 << 20)
	}
	filename := flag.String("config", "/etc/rpc-router/config.json", "configuration file")
	command := flag.String("command", "serve", "serve, check, init, status, switch, reload")
	socket := flag.String("socket", "", "admin socket for status/switch/reload")
	chain := flag.String("chain", "", "chain name")
	backend := flag.String("backend", "", "backend name")
	revision := flag.Uint64("revision", 0, "expected current routing revision")
	flag.Parse()
	run := func() error {
		switch *command {
		case "check":
			_, err := loadCatalog(*filename)
			if err == nil {
				fmt.Println("configuration valid (no provider requests made)")
			}
			return err
		case "serve", "init":
			r, err := newRouter(*filename, *command == "init")
			if err != nil {
				return err
			}
			defer r.close()
			if *command == "init" {
				fmt.Println("routing state initialized/verified")
				return nil
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return serve(ctx, r)
		default:
			if !filepath.IsAbs(*socket) {
				return errors.New("provide absolute -socket for admin commands")
			}
			return adminCommand(*socket, *command, *chain, *backend, *revision)
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
