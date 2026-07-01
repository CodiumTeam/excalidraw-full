package firebase

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

type (
	BatchGetRequest struct {
		Documents []string `json:"documents"`
	}
	BatchGetEmptyResponse struct {
		Missing  string `json:"missing"`
		ReadTime string `json:"readTime"`
	}

	FoundInfoResponse struct {
		Name       string      `json:"name"`
		Fields     interface{} `json:"fields"`
		CreateTime string      `json:"createTime"`
		UpdateTime string      `json:"updateTime"`
	}
	BatchGetExistsResponse struct {
		Found    FoundInfoResponse `json:"found"`
		ReadTime string            `json:"readTime"`
	}

	UpdateRequest struct {
		Name   string      `json:"name"`
		Fields interface{} `json:"fields"`
	}
	WriteRequest struct {
		Update UpdateRequest `json:"update"`
	}
	BatchCommitRequest struct {
		Writes []WriteRequest `json:"writes"`
	}

	WriteResult struct {
		UpdateTime string `json:"updateTime"`
	}
	BatchCommitResponse struct {
		WriteResults []WriteResult `json:"writeResults"`
		CommitTime   string        `json:"commitTime"`
	}
)

// savedItems keeps the collaboration-room scenes in memory for speed. It is now
// backed by disk (see persistItem/loadItem) so shared "#room=" links survive a
// container restart/redeploy instead of being lost.
var (
	savedItems = make(map[string]interface{})
	itemsMu    sync.RWMutex
)

// roomPersistenceDir is where room scenes are written. Defaults to /data/rooms
// (the mounted volume); override with ROOM_PERSISTENCE_PATH.
func roomPersistenceDir() string {
	if dir := os.Getenv("ROOM_PERSISTENCE_PATH"); dir != "" {
		return dir
	}
	return "/data/rooms"
}

// keyToPath maps an (arbitrary, slash-containing) document key to a safe file
// path by hashing it.
func keyToPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(roomPersistenceDir(), hex.EncodeToString(sum[:])+".json")
}

// persistItem writes a scene to disk atomically (temp file + rename).
func persistItem(key string, fields interface{}) {
	dir := roomPersistenceDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Printf("room persist: mkdir %q: %v\n", dir, err)
		return
	}
	b, err := json.Marshal(fields)
	if err != nil {
		fmt.Printf("room persist: marshal %q: %v\n", key, err)
		return
	}
	path := keyToPath(key)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		fmt.Printf("room persist: write %q: %v\n", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		fmt.Printf("room persist: rename %q: %v\n", path, err)
	}
}

// loadItem reads a scene previously written by persistItem.
func loadItem(key string) (interface{}, bool) {
	b, err := os.ReadFile(keyToPath(key))
	if err != nil {
		return nil, false
	}
	var fields interface{}
	if err := json.Unmarshal(b, &fields); err != nil {
		fmt.Printf("room persist: unmarshal %q: %v\n", key, err)
		return nil, false
	}
	return fields, true
}

func (body *BatchGetRequest) Bind(r *http.Request) (err error) {
	return nil
}
func (body *BatchCommitRequest) Bind(r *http.Request) (err error) {
	return nil
}
func HandleBatchCommit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectId := chi.URLParam(r, "project_id")
		databaseId := chi.URLParam(r, "database_id")
		_ = projectId
		_ = databaseId

		data := &BatchCommitRequest{}
		// Seems like requests is text/plain but content is json ...
		if err := render.DecodeJSON(r.Body, data); err != nil {
			fmt.Println(err)
			render.Status(r, http.StatusBadRequest)
			return
		}

		if len(data.Writes) == 0 {
			render.Status(r, http.StatusBadRequest)
			return
		}

		name := data.Writes[0].Update.Name
		fields := data.Writes[0].Update.Fields

		itemsMu.Lock()
		savedItems[name] = fields
		itemsMu.Unlock()
		persistItem(name, fields)

		render.JSON(w, r, BatchCommitResponse{
			CommitTime: time.Now().Format(time.RFC3339),
			WriteResults: []WriteResult{
				WriteResult{UpdateTime: time.Now().Format(time.RFC3339)},
			},
		})
		render.Status(r, http.StatusOK)
		return

	}
}

func HandleBatchGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		projectId := chi.URLParam(r, "project_id")
		databaseId := chi.URLParam(r, "database_id")
		fmt.Printf("Got %v and %v\n", projectId, databaseId)
		data := &BatchGetRequest{}

		// Seems like requests is text/plain but content is json ...
		if err := render.DecodeJSON(r.Body, data); err != nil {
			fmt.Println(err)
			render.Status(r, http.StatusBadRequest)
			return
		}
		if len(data.Documents) == 0 {
			render.Status(r, http.StatusBadRequest)
			return
		}
		key := data.Documents[0]
		fmt.Printf("Got key %v \n", key)

		itemsMu.RLock()
		fields, ok := savedItems[key]
		itemsMu.RUnlock()

		// Not in memory (e.g. after a restart): try to load it from disk.
		if !ok {
			if loaded, found := loadItem(key); found {
				itemsMu.Lock()
				savedItems[key] = loaded
				itemsMu.Unlock()
				fields, ok = loaded, true
			}
		}

		if !ok {
			fmt.Println("missing key")
			render.JSON(w, r, []BatchGetEmptyResponse{BatchGetEmptyResponse{
				Missing:  key,
				ReadTime: time.Now().Format(time.RFC3339),
			}})
			render.Status(r, http.StatusOK)
			return
		}
		fmt.Println("existing key")
		render.JSON(w, r, []BatchGetExistsResponse{BatchGetExistsResponse{
			Found: FoundInfoResponse{
				Name:       key,
				Fields:     fields,
				CreateTime: time.Now().Format(time.RFC3339),
				UpdateTime: time.Now().Format(time.RFC3339),
			},
			ReadTime: time.Now().Format(time.RFC3339),
		}})
		render.Status(r, http.StatusOK)
		return

	}
}
