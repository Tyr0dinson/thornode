// Package sonic stands in for github.com/bytedance/sonic in garbled release
// builds (see GARBLE in the Makefile). The real sonic JIT links into runtime
// internals that garble rewrites, so garble refuses to build it. Its only
// importer, cosmossdk.io/log, calls Marshal and Unmarshal for log output, which
// this implements with encoding/json. Like sonic's default config, Marshal does
// not escape HTML characters.
package sonic

import (
	"bytes"
	"encoding/json"
)

func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encode appends a newline that Marshal does not.
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func Unmarshal(buf []byte, v any) error {
	return json.Unmarshal(buf, v)
}
