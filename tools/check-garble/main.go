// check-garble fails if a binary still exposes the original package paths or
// source file names of thornode or tss-lib-private in .gopclntab, or carries
// module build info.
//
// .gopclntab survives `strip` and -ldflags="-s -w" because the Go runtime needs
// it, so without obfuscation it lists every function and file name, and diffing
// two release images reveals which functions a security fix added. garble
// renames them and scrambles the table's magic number; this tool restores the
// magic, as an attacker would, before reading the table. garble keeps exported
// method names, which interfaces may need, so those still appear under the
// obfuscated package and type names.
package main

import (
	"debug/buildinfo"
	"debug/elf"
	"debug/gosym"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	// pclntabMagic is the .gopclntab header magic since Go 1.20.
	pclntabMagic = 0xfffffff1

	// privatePrefix covers the thornode and tss-lib-private modules (GARBLE_PKGS).
	privatePrefix = "gitlab.com/thorchain/"

	// maxListed caps how many leaked names are printed per binary.
	maxListed = 20
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: check-garble BINARY...")
		os.Exit(2)
	}
	failed := false
	for _, path := range os.Args[1:] {
		if err := check(path); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s\n", path, err)
			failed = true
			continue
		}
		fmt.Printf("%s: no %s names in .gopclntab and no build info\n", path, privatePrefix)
	}
	if failed {
		os.Exit(1)
	}
}

func check(path string) error {
	// The build info lists every dependency version, which dates the release.
	// garble leaves only an empty record.
	if info, err := buildinfo.ReadFile(path); err == nil && (info.Main.Path != "" || len(info.Deps) > 0) {
		return fmt.Errorf("module build info is present (built without GARBLE=1?)")
	}

	table, err := readTable(path)
	if err != nil {
		return err
	}

	// An empty or unparsed table would pass vacuously, so require a function
	// garble never renames.
	hasRuntimeMain := false
	var leaked []string
	for _, fn := range table.Funcs {
		if fn.Name == "runtime.main" {
			hasRuntimeMain = true
		}
		// -trimpath file names start with the module path.
		file, _, _ := table.PCToLine(fn.Entry)
		if strings.HasPrefix(fn.Name, privatePrefix) || strings.Contains(file, privatePrefix) {
			leaked = append(leaked, fn.Name+" ("+file+")")
		}
	}
	if !hasRuntimeMain {
		return fmt.Errorf("could not read function names from .gopclntab (%d found, no runtime.main)", len(table.Funcs))
	}
	if len(leaked) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d functions under %s are not obfuscated (built without GARBLE=1?):", len(leaked), privatePrefix)
	for i, name := range leaked {
		if i == maxListed {
			fmt.Fprintf(&b, "\n  ... and %d more", len(leaked)-maxListed)
			break
		}
		fmt.Fprintf(&b, "\n  %s", name)
	}
	return errors.New(b.String())
}

func readTable(path string) (*gosym.Table, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	pclntab := f.Section(".gopclntab")
	text := f.Section(".text")
	if pclntab == nil || text == nil {
		return nil, fmt.Errorf("missing .gopclntab or .text section")
	}
	data, err := pclntab.Data()
	if err != nil {
		return nil, fmt.Errorf("read .gopclntab: %w", err)
	}
	if len(data) < 4 {
		return nil, fmt.Errorf(".gopclntab is truncated")
	}
	binary.LittleEndian.PutUint32(data, pclntabMagic)

	table, err := gosym.NewTable(nil, gosym.NewLineTable(data, text.Addr))
	if err != nil {
		return nil, fmt.Errorf("parse .gopclntab: %w", err)
	}
	return table, nil
}
