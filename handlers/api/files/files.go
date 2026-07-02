package files

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/go-chi/chi/v5"
)

// Room files (images) are persisted on the local filesystem so that shared
// "#room=" links keep their images across restarts. The client already
// encrypts+compresses each file before upload (see saveFilesToFirebase in the
// frontend), so the server only ever stores/serves an opaque blob. This mirrors
// the scene persistence in handlers/api/firebase.

// maxFileBytes caps a single uploaded file. Matches the frontend
// FILE_UPLOAD_MAX_BYTES ceiling with some slack for the encryption envelope.
const maxFileBytes = 25 << 20 // 25 MiB

// idPattern validates roomId/fileId path segments to prevent path traversal.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// baseDir is where room files are written. Defaults to /data/files (the mounted
// volume); override with FILE_PERSISTENCE_PATH.
func baseDir() string {
	if dir := os.Getenv("FILE_PERSISTENCE_PATH"); dir != "" {
		return dir
	}
	return "/data/files"
}

func filePath(roomID, fileID string) string {
	return filepath.Join(baseDir(), "rooms", roomID, fileID)
}

// HandlePut stores a room file: PUT /api/v2/files/rooms/{roomId}/{fileId}
func HandlePut() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := chi.URLParam(r, "roomId")
		fileID := chi.URLParam(r, "fileId")
		if !idPattern.MatchString(roomID) || !idPattern.MatchString(fileID) {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxFileBytes+1))
		if err != nil {
			http.Error(w, "failed to read body", http.StatusInternalServerError)
			return
		}
		if len(body) > maxFileBytes {
			http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
			return
		}

		dir := filepath.Join(baseDir(), "rooms", roomID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Printf("room files: mkdir %q: %v\n", dir, err)
			http.Error(w, "failed to store", http.StatusInternalServerError)
			return
		}

		// Write atomically (temp file + rename) so a concurrent read never sees
		// a half-written file.
		path := filePath(roomID, fileID)
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, body, 0o644); err != nil {
			fmt.Printf("room files: write %q: %v\n", tmp, err)
			http.Error(w, "failed to store", http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			fmt.Printf("room files: rename %q: %v\n", path, err)
			http.Error(w, "failed to store", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}

// HandleGet serves a room file: GET /api/v2/files/rooms/{roomId}/{fileId}
func HandleGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := chi.URLParam(r, "roomId")
		fileID := chi.URLParam(r, "fileId")
		if !idPattern.MatchString(roomID) || !idPattern.MatchString(fileID) {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		body, err := os.ReadFile(filePath(roomID, fileID))
		if err != nil {
			http.NotFound(w, r)
			return
		}

		// Content is opaque, immutable and content-addressed by fileId, so it
		// is safe to cache aggressively.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(body)
	}
}
