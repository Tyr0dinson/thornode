package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const mimirIDsCovSource = `package mimir

type Id int32

type Other int32

const (
	IdAlpha Id = iota
	IdBeta
	IdGamma, IdDelta
)

const (
	NotAnId Other = 1
	AlsoNotAnId
)

var single Id = 7

var untyped = 3

const (
	x, y = 1, 2
)

func f() {}
`

func mimirIDsCovCapture(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = old }()
	fn()
	require.NoError(t, w.Close())
	return <-done
}

func TestMimirIDsMainCov(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "mimir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mimir", "id.go"), []byte(mimirIDsCovSource), 0o600))
	t.Chdir(dir)

	out := mimirIDsCovCapture(t, main)

	// Only names declared with type Id (explicitly or by implicit continuation
	// within the same declaration group) are printed, in source order.
	require.Equal(t, "IdAlpha\nIdBeta\nIdGamma\nIdDelta\nsingle\n", out)
}
