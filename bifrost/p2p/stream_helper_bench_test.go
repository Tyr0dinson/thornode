package p2p

import (
	"bytes"
	"testing"
)

// BenchmarkStreamRoundTrip writes and reads one framed message through the
// stream helpers, as each attestation batch does per peer.
func BenchmarkStreamRoundTrip(b *testing.B) {
	payload := bytes.Repeat([]byte{0xAB}, 1500)
	stream := NewMockNetworkStream()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := WriteStreamWithBuffer(payload, stream); err != nil {
			b.Fatal(err)
		}
		got, err := ReadStreamWithBuffer(stream)
		if err != nil || len(got) != len(payload) {
			b.Fatalf("read %d bytes, err %v", len(got), err)
		}
	}
}
