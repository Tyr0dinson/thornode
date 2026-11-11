package divergence

import (
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gorilla/mux"
)

const (
	CommitInfoRoute = "/thorchain/debug/commit_info/{height}"
	WriteSetRoute   = "/thorchain/debug/writeset/{height}"
)

// RegisterRoutes registers the node-local debug routes on r. They are plain
// JSON (no proto/openapi) and read only on-disk data: commit info from the
// root store's metadata, write sets from the recorder's ring. d may be nil
// (diagnostics disabled): commit_info still works, writeset returns 404.
func RegisterRoutes(r *mux.Router, commits CommitInfoSource, d *Diagnostics) {
	r.HandleFunc(CommitInfoRoute, commitInfoHandler(commits)).Methods(http.MethodGet)
	r.HandleFunc(WriteSetRoute, writeSetHandler(d)).Methods(http.MethodGet)
}

func writeHTTPError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func commitInfoHandler(commits CommitInfoSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h, err := parseHeight(mux.Vars(r)["height"])
		if err != nil {
			writeHTTPError(w, http.StatusBadRequest, err.Error())
			return
		}
		if commits == nil {
			writeHTTPError(w, http.StatusServiceUnavailable, "commit info unavailable")
			return
		}
		ci, err := commits.GetCommitInfo(h)
		if err != nil {
			writeHTTPError(w, http.StatusNotFound, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CommitInfoResponse{
			Height:  h,
			AppHash: strings.ToUpper(hex.EncodeToString(ci.Hash())),
			Stores:  commitInfoHashes(ci),
		})
	}
}

func writeSetHandler(d *Diagnostics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h, err := parseHeight(mux.Vars(r)["height"])
		if err != nil {
			writeHTTPError(w, http.StatusBadRequest, err.Error())
			return
		}
		rec := d.Recorder()
		if rec == nil {
			writeHTTPError(w, http.StatusNotFound, "divergence diagnostics disabled")
			return
		}
		f, err := os.Open(filepath.Join(rec.WriteSetDir(), writeSetFileName(h)))
		if errors.Is(err, os.ErrNotExist) {
			writeHTTPError(w, http.StatusNotFound, "write set not retained for this height")
			return
		}
		if err != nil {
			writeHTTPError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer f.Close()

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = io.Copy(w, f)
			return
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			writeHTTPError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer zr.Close()
		_, _ = io.CopyN(w, zr, maxResponseBytes)
	}
}
