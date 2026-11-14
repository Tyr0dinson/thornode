package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type State struct {
	Version uint64            `json:"version"`
	Catalog string            `json:"catalog"`
	Routes  map[string]string `json:"routes"`
}

func validState(s State, c *catalog) bool {
	if s.Version == 0 || s.Catalog != c.digest || len(s.Routes) != len(c.config.Chains) {
		return false
	}
	for chain, conf := range c.config.Chains {
		if conf.Backends[s.Routes[chain]] == nil {
			return false
		}
	}
	return true
}

func persist(filename string, s State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(filename), ".routes-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filename); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(filename))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// openState returns the saved routes. fault reports that an adopted state could
// not be written and is held in memory only.
func openState(c *catalog, initialize bool) (s State, lock *os.File, fault bool, err error) {
	filename := c.config.StateFile
	if initialize {
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			return State{}, nil, false, errors.New("cannot provision private state directory")
		}
	}
	lock, err = os.OpenFile(filename+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return State{}, nil, false, errors.New("cannot open state lock")
	}
	fail := func() (State, *os.File, bool, error) {
		lock.Close()
		return State{}, nil, false, errors.New("state unavailable, incompatible, or already in use")
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return fail()
	}
	data, err := readLimited(filename, configLimit)
	if os.IsNotExist(err) && initialize {
		s = State{Version: 1, Catalog: c.digest, Routes: map[string]string{}}
		for name, ch := range c.config.Chains {
			s.Routes[name] = ch.Default
		}
		if persist(filename, s) != nil {
			return fail()
		}
		return s, lock, false, nil
	}
	// Unreadable state holds no route choice to preserve and is still refused.
	if err != nil || strictJSON(data, &s) != nil || s.Version == 0 || s.Routes == nil {
		return fail()
	}
	if validState(s, c) {
		return s, lock, false, nil
	}
	// The catalog changed without a reload (for example an edited target file
	// followed by a restart). Refusing to start would take every chain down, so
	// keep each route that still names a backend and default the rest.
	adopted := State{Version: s.Version + 1, Catalog: c.digest, Routes: map[string]string{}}
	var changed []string
	for name, ch := range c.config.Chains {
		route := s.Routes[name]
		if ch.Backends[route] == nil {
			route = ch.Default
			changed = append(changed, name)
		}
		adopted.Routes[name] = route
	}
	sort.Strings(changed)
	dropped := 0
	for name := range s.Routes {
		if _, ok := c.config.Chains[name]; !ok {
			dropped++
		}
	}
	log.Printf("event=state_catalog_drift old_version=%d new_version=%d changed=%s dropped=%d", s.Version, adopted.Version, strings.Join(changed, ","), dropped)
	if persist(filename, adopted) != nil {
		fault = true
		log.Print("event=routing_state_fault source=startup")
	}
	return adopted, lock, fault, nil
}
