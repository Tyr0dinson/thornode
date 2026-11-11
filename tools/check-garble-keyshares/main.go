// check-garble-keyshares loads the go-tss keysign fixtures the way bifrost
// loads a keyshare (storage.FileStateMgr, then the keysign package's
// LoadLocalData) and fails if any of them does not decode to the key it
// belongs to.
//
// The fixtures were written by an ungarbled keygen, as every keyshare on a
// node today was, and LocalPartySaveData has no json tags. A garbled bifrost
// reads them only if garble restores the original field names, which it does
// only for types its reflection pass records; the keysign packages mark both
// LocalPartySaveData types for that. A type it misses decodes to an empty
// struct, so keysign fails on every upgraded node, while a garbled simulation
// still passes because it reads back what garbled keygen wrote. Run this built
// with garble: `make check-garble-keyshares GARBLE=1`.
//
// This tool must not decode LocalPartySaveData itself: its own json calls
// would record the types and hide a missing marker.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign/ecdsa"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign/eddsa"
)

const fixtureDir = "bifrost/tss/go-tss/test_data/keysign_data"

func main() {
	conversion.SetupBech32Prefix()
	fixtures := []struct {
		dir    string
		pubKey func([]byte) (string, error)
	}{
		{"ecdsa", ecdsaPubKey},
		{"ecdsa_legacy", ecdsaPubKey},
		{"eddsa", eddsaPubKey},
	}
	failed := false
	for _, f := range fixtures {
		paths, err := filepath.Glob(filepath.Join(fixtureDir, f.dir, "*.json"))
		if err == nil && len(paths) == 0 {
			err = errors.New("no keyshares found (run from the repository root)")
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s\n", f.dir, err)
			failed = true
			continue
		}
		for _, path := range paths {
			if err := check(path, f.pubKey); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s\n", path, err)
				failed = true
				continue
			}
			fmt.Printf("%s: ok\n", path)
		}
	}
	if failed {
		os.Exit(1)
	}
}

// check copies the keyshare to localstate-<pubkey>.json, the name bifrost
// stores it under, loads it with FileStateMgr and compares the key that its
// local data holds with the pub_key it is stored under.
func check(path string, pubKey func([]byte) (string, error)) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// KeygenLocalState has json tags, so this decode does not rely on garble.
	var state storage.KeygenLocalState
	if err = json.Unmarshal(raw, &state); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "check-garble-keyshares")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err = os.WriteFile(filepath.Join(dir, "localstate-"+state.PubKey+".json"), raw, 0o600); err != nil {
		return err
	}
	mgr, err := storage.NewFileStateMgr(dir)
	if err != nil {
		return err
	}
	if state, err = mgr.GetLocalState(state.PubKey); err != nil {
		return err
	}
	got, err := pubKey(state.LocalData)
	if err != nil {
		return err
	}
	if got != state.PubKey {
		return fmt.Errorf("local data holds key %s, want %s", got, state.PubKey)
	}
	return nil
}

func ecdsaPubKey(localData []byte) (string, error) {
	data, err := ecdsa.LoadLocalData(localData)
	if err != nil {
		return "", err
	}
	pubKey, _, err := conversion.GetTssPubKeyECDSA(data.ECDSAPub)
	return pubKey, err
}

func eddsaPubKey(localData []byte) (string, error) {
	data, err := eddsa.LoadLocalData(localData)
	if err != nil {
		return "", err
	}
	// EdDSA keysign has no ValidateForKeysign, so check the share as well.
	if data.Xi == nil || data.ShareID == nil || len(data.Ks) == 0 || len(data.BigXj) != len(data.Ks) {
		return "", errors.New("local data is missing its key share")
	}
	pubKey, _, err := conversion.GetTssPubKeyEDDSA(data.EDDSAPub)
	return pubKey, err
}
