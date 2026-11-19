package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

const configLimit = 1 << 20

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var routePath = regexp.MustCompile(`^(/[a-zA-Z0-9_-]+)+$`)

type Config struct {
	Listen      string           `json:"listen"`
	AdminSocket string           `json:"admin_socket"`
	StateFile   string           `json:"state_file"`
	Chains      map[string]Chain `json:"chains"`
}

type Chain struct {
	Default         string `json:"default"`
	Active          int    `json:"active"`
	Waiting         int    `json:"waiting"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	QueueSeconds    int    `json:"queue_seconds"`
	MaxRequestBytes int64  `json:"max_request_bytes"`
	// Opt-in: answer chain-head queries from one provider call for this long.
	// See headcache.go. Zero (the default) forwards every request.
	HeadCacheMS int `json:"head_cache_ms,omitempty"`
	// Opt-in, needs head_cache_ms: fetch this many blocks (and their receipts)
	// ahead of the scanner, never past the provider's safe head. See readahead.go.
	ReadAheadBlocks int `json:"read_ahead_blocks,omitempty"`
	// Opt-in, needs read_ahead_blocks: also fetch header-by-height queries ahead
	// of the scanner, again never past the safe head. See readahead.go.
	ReadAheadHeaders bool `json:"read_ahead_headers,omitempty"`
	// Opt-in, for bitcoind-RPC chains, instead of head_cache_ms: keep the tip no
	// older than this and fetch each new block and its previous-transaction
	// lookups before the scanner asks. See utxo.go.
	UTXOAheadMS int `json:"utxo_ahead_ms,omitempty"`
	// Opt-in, needs head_cache_ms: keep the head current without waiting for the
	// scanner and fetch each new head block and its receipts before it asks.
	// See tipahead.go.
	TipAhead bool `json:"tip_ahead,omitempty"`
	// Opt-in, needs head_cache_ms: when a known run of height-pinned state
	// queries starts, make the rest of the run early. See callahead.go.
	CallAhead bool `json:"call_ahead,omitempty"`
	// Opt-in, needs tip_ahead and call_ahead: show the scanner a block that
	// touches a vault only once this many later blocks exist. See holdahead.go.
	HoldBlocks int                          `json:"hold_blocks,omitempty"`
	Endpoints  map[string]Endpoint          `json:"endpoints"`
	Backends   map[string]map[string]string `json:"backends"`
}

type Endpoint struct {
	Path   string `json:"path"`
	Suffix bool   `json:"suffix"`
}

// Targets are restricted files, not command arguments or public configuration.
type Target struct {
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	AllowHTTP bool              `json:"allow_http,omitempty"`
}

type catalog struct {
	config  Config
	targets map[string]map[string]map[string]Target
	digest  string
}

// Reject duplicate keys as well as unknown fields, including in nested maps.
func strictJSON(data []byte, dest any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return errors.New("duplicate JSON key")
					}
					seen[name] = true
					if err := walk(); err != nil {
						return err
					}
				}
			case '[':
				for d.More() {
					if err := walk(); err != nil {
						return err
					}
				}
			default:
				return errors.New("unexpected delimiter")
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(dest)
}

func readLimited(filename string, limit int64) ([]byte, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("file unreadable or too large")
	}
	return b, nil
}

func validateTarget(t Target) error {
	u, err := url.Parse(t.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "https" && !(u.Scheme == "http" && t.AllowHTTP)) {
		return errors.New("invalid target URL (HTTPS required unless explicitly internal HTTP)")
	}
	if u.RawPath != "" || strings.Contains(u.Path, "//") || strings.Contains(u.Path, "/../") || strings.HasSuffix(u.Path, "/..") {
		return errors.New("invalid target path")
	}
	for k, v := range t.Headers {
		if k != "Authorization" && k != "X-Api-Key" && k != "Tron-Pro-Api-Key" {
			return errors.New("unsupported credential header")
		}
		if strings.ContainsAny(v, "\r\n") || len(v) > 8192 || strings.TrimSpace(v) == "" {
			return errors.New("invalid credential header")
		}
	}
	return nil
}

func loadCatalog(filename string) (*catalog, error) {
	b, err := readLimited(filename, configLimit)
	if err != nil {
		return nil, errors.New("cannot read configuration")
	}
	var c Config
	if strictJSON(b, &c) != nil {
		return nil, errors.New("invalid configuration JSON")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil || !filepath.IsAbs(c.AdminSocket) || !filepath.IsAbs(c.StateFile) || len(c.AdminSocket) > 100 || c.AdminSocket == c.StateFile || len(c.Chains) == 0 || len(c.Chains) > 32 {
		return nil, errors.New("invalid listeners/state/chains")
	}
	out := &catalog{config: c, targets: map[string]map[string]map[string]Target{}}
	paths := []string{}
	for name, chain := range c.Chains {
		if !identifier.MatchString(name) || chain.Active < 1 || chain.Active > 128 || chain.Waiting < 0 || chain.Waiting > 128 || chain.TimeoutSeconds < 1 || chain.TimeoutSeconds > 120 || chain.QueueSeconds < 1 || chain.QueueSeconds > 5 || chain.QueueSeconds > chain.TimeoutSeconds || chain.MaxRequestBytes < 1 || chain.MaxRequestBytes > 8<<20 || chain.HeadCacheMS < 0 || chain.HeadCacheMS > 5000 || chain.ReadAheadBlocks < 0 || chain.ReadAheadBlocks > 8 || chain.ReadAheadBlocks > 0 && chain.HeadCacheMS == 0 || chain.ReadAheadHeaders && chain.ReadAheadBlocks == 0 || chain.TipAhead && chain.HeadCacheMS == 0 || chain.CallAhead && chain.HeadCacheMS == 0 || chain.HoldBlocks < 0 || chain.HoldBlocks > holdMaxBlocks || chain.HoldBlocks > 0 && !(chain.TipAhead && chain.CallAhead) || chain.UTXOAheadMS != 0 && (chain.UTXOAheadMS < 200 || chain.UTXOAheadMS > 5000 || chain.HeadCacheMS != 0) || len(chain.Endpoints) == 0 || len(chain.Endpoints) > 4 || len(chain.Backends) == 0 || len(chain.Backends) > 16 || chain.Backends[chain.Default] == nil {
			return nil, errors.New("invalid chain configuration")
		}
		for id, endpoint := range chain.Endpoints {
			if !identifier.MatchString(id) || !routePath.MatchString(endpoint.Path) || endpoint.Path == "/healthz" || endpoint.Path == "/readyz" || endpoint.Path == "/metrics" {
				return nil, errors.New("invalid endpoint path")
			}
			for _, p := range paths {
				if p == endpoint.Path || strings.HasPrefix(p, endpoint.Path+"/") || strings.HasPrefix(endpoint.Path, p+"/") {
					return nil, errors.New("overlapping paths")
				}
			}
			paths = append(paths, endpoint.Path)
		}
		out.targets[name] = map[string]map[string]Target{}
		for backend, files := range chain.Backends {
			if !identifier.MatchString(backend) || len(files) != len(chain.Endpoints) {
				return nil, errors.New("incomplete backend")
			}
			out.targets[name][backend] = map[string]Target{}
			for id := range chain.Endpoints {
				if !filepath.IsAbs(files[id]) {
					return nil, errors.New("target file must be absolute")
				}
				data, err := readLimited(files[id], 32<<10)
				var target Target
				if err != nil || strictJSON(data, &target) != nil {
					return nil, errors.New("cannot load target credential file")
				}
				if err := validateTarget(target); err != nil {
					return nil, err
				}
				out.targets[name][backend][id] = target
			}
		}
	}
	// Bind persistent choices to the precise loaded catalog, including credentials.
	data, _ := json.Marshal(struct {
		Config  Config
		Targets any
	}{c, out.targets})
	hash := sha256.Sum256(data)
	out.digest = hex.EncodeToString(hash[:])
	return out, nil
}

func compatible(a, b Config) bool {
	if a.Listen != b.Listen || a.AdminSocket != b.AdminSocket || a.StateFile != b.StateFile || len(a.Chains) != len(b.Chains) {
		return false
	}
	for name, old := range a.Chains {
		next, ok := b.Chains[name]
		old.Backends, next.Backends = nil, nil
		old.HeadCacheMS, next.HeadCacheMS = 0, 0 // Tunable by reload.
		old.ReadAheadBlocks, next.ReadAheadBlocks = 0, 0
		old.ReadAheadHeaders, next.ReadAheadHeaders = false, false
		old.UTXOAheadMS, next.UTXOAheadMS = 0, 0
		old.TipAhead, next.TipAhead = false, false
		old.CallAhead, next.CallAhead = false, false
		old.HoldBlocks, next.HoldBlocks = 0, 0
		if !ok || !reflect.DeepEqual(old, next) {
			return false
		}
	}
	return true
}

func jsonReply(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
