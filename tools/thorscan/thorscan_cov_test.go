package thorscan

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func withServerCov(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	ts := httptest.NewServer(h)
	oldEndpoint := APIEndpoint
	oldClient := httpClient
	APIEndpoint = ts.URL
	httpClient = ts.Client()
	t.Cleanup(func() {
		APIEndpoint = oldEndpoint
		httpClient = oldClient
		ts.Close()
	})
}

func TestGetBlockLatestNoHeightParamCov(t *testing.T) {
	var gotQuery string
	withServerCov(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"header":{"height":7,"time":"2024-01-01T00:00:00Z"}}`))
	})
	b, err := getBlock(-1)
	require.NoError(t, err)
	require.Equal(t, int64(7), b.Header.Height)
	require.Equal(t, "", gotQuery)
}

func TestGetBlockBadGzipCov(t *testing.T) {
	withServerCov(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write([]byte("definitely not gzip"))
	})
	_, err := getBlock(1)
	require.Error(t, err)
}

func TestGetBlockOtherStatusCov(t *testing.T) {
	withServerCov(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	_, err := getBlock(1)
	require.EqualError(t, err, "bad status code: 418")
}

func TestGetBlockBadJSONStillReturnsCov(t *testing.T) {
	withServerCov(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{bad json"))
	})
	// decode errors are logged and an empty block is returned
	b, err := getBlock(1)
	require.NoError(t, err)
	require.NotNil(t, b)
	require.Equal(t, int64(0), b.Header.Height)
}

func TestGetBlockBadURLCov(t *testing.T) {
	old := APIEndpoint
	defer func() { APIEndpoint = old }()
	APIEndpoint = "http://bad host\x7f"
	_, err := getBlock(1)
	require.Error(t, err)
}

func TestGetBlockTransportErrorCov(t *testing.T) {
	old := APIEndpoint
	defer func() { APIEndpoint = old }()
	APIEndpoint = "bogus://nowhere"
	_, err := getBlock(1)
	require.Error(t, err)
}

func TestBlockTxUnmarshalJSONWithTxCov(t *testing.T) {
	builder := encodingConfig.TxConfig.NewTxBuilder()
	builder.SetMemo("hello-cov")
	builder.SetGasLimit(123)
	raw, err := encodingConfig.TxConfig.TxJSONEncoder()(builder.GetTx())
	require.NoError(t, err)

	data := fmt.Sprintf(`{"hash":"ABC","tx":%s}`, raw)
	var bt BlockTx
	require.NoError(t, bt.UnmarshalJSON([]byte(data)))
	require.Equal(t, "ABC", bt.Hash)
	require.NotNil(t, bt.Tx)

	// invalid tx payload is rejected
	var bad BlockTx
	require.Error(t, bad.UnmarshalJSON([]byte(`{"hash":"X","tx":{"body":123}}`)))
}

// blockServerCov serves blocks up to tip; the block at tip has a recent timestamp.
func blockServerCov(t *testing.T, tip int64) *sync.Map {
	seen := &sync.Map{}
	withServerCov(t, func(w http.ResponseWriter, r *http.Request) {
		hs := r.URL.Query().Get("height")
		h := tip
		if hs != "" {
			var err error
			h, err = strconv.ParseInt(hs, 10, 64)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		seen.Store(h, true)
		ts := "2020-01-01T00:00:00Z"
		if h >= tip-1 {
			ts = time.Now().UTC().Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(w, `{"header":{"height":%d,"time":%q}}`, h, ts)
	})
	return seen
}

func collectCov(t *testing.T, ch <-chan *BlockResponse) []int64 {
	t.Helper()
	var heights []int64
	timeout := time.After(10 * time.Second)
	for {
		select {
		case b, ok := <-ch:
			if !ok {
				return heights
			}
			heights = append(heights, b.Header.Height)
		case <-timeout:
			t.Fatal("scan timed out")
			return nil
		}
	}
}

func TestScanExplicitRangeCov(t *testing.T) {
	blockServerCov(t, 100)
	heights := collectCov(t, Scan(5, 12))
	require.Equal(t, []int64{5, 6, 7, 8, 9, 10, 11, 12}, heights)
}

func TestScanRelativeToTipCov(t *testing.T) {
	blockServerCov(t, 100)
	// start/stop relative to the current tip
	heights := collectCov(t, Scan(-3, -1))
	require.Equal(t, []int64{97, 98, 99}, heights)
}

func TestScanNearTipShutsDownExtraWorkersCov(t *testing.T) {
	blockServerCov(t, 20)
	// blocks 19 and 20 are recent, so extra workers exit, but output is still complete
	heights := collectCov(t, Scan(15, 20))
	require.Equal(t, []int64{15, 16, 17, 18, 19, 20}, heights)
}
