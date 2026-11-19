package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Listen              string `json:"listen"`
	AdminSocket         string `json:"admin_socket"`
	Database            string `json:"database"`
	Genesis             string `json:"genesis"`
	LocalAPI            string `json:"local_api"`
	LocalRPC            string `json:"local_rpc"`
	ExternalAPIFile     string `json:"external_api_file"`
	ExternalRPCFile     string `json:"external_rpc_file"`
	BroadcastEnabled    bool   `json:"broadcast_enabled"`
	AutoFailbackEnabled bool   `json:"auto_failback_enabled"`
	BlockFloor          int64  `json:"block_floor"`
	ReceiptFloor        int64  `json:"receipt_floor"`
	TimeoutSeconds      int    `json:"timeout_seconds"`
	PollSeconds         int    `json:"poll_seconds"`
}

func readConfig(filename string) (Config, error) {
	c := Config{Listen: "127.0.0.1:8080", TimeoutSeconds: 4, PollSeconds: 10}
	b, err := os.ReadFile(filename)
	if err != nil || uniqueJSON(b) != nil {
		return c, errors.New("cannot read configuration")
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || !validID(c.Genesis) || c.Database == "" || c.AdminSocket == "" || c.BlockFloor < 0 || c.ReceiptFloor < 0 || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 10 || c.PollSeconds < 5 || c.PollSeconds > 30 {
		return c, errors.New("invalid configuration")
	}
	return c, nil
}

func endpoint(raw string, external bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && (external || u.Scheme != "http")) {
		return nil, errors.New("invalid upstream URL")
	}
	return u, nil
}

func endpointFile(filename string) (*url.URL, error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return nil, errors.New("cannot read upstream credential file")
	}
	return endpoint(string(b), true)
}

func (c Config) timeout() time.Duration { return time.Duration(c.TimeoutSeconds) * time.Second }
