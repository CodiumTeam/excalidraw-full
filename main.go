package main

import (
	"embed"
	_ "embed"
	"excalidraw-complete/handlers/api/documents"
	"excalidraw-complete/handlers/api/files"
	"excalidraw-complete/handlers/api/firebase"
	"excalidraw-complete/handlers/api/kv"
	"excalidraw-complete/handlers/api/openai"
	"excalidraw-complete/handlers/auth"
	authMiddleware "excalidraw-complete/middleware"
	"excalidraw-complete/stores"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
	"github.com/zishang520/engine.io/v2/types"
	"github.com/zishang520/engine.io/v2/utils"
	socketio "github.com/zishang520/socket.io/v2/socket"
)

type (
	UserToFollow struct {
		SocketId string `json:"socketId"`
		Username string `json:"username"`
	}

	OnUserFollowedPayload struct {
		UserToFollow UserToFollow `json:"userToFollow"`
		Action       string       `json:"action"` // "FOLLOW" | "UNFOLLOW"
	}
)

//go:embed all:frontend
var assets embed.FS

func handleUI() http.HandlerFunc {
	sub, err := fs.Sub(assets, "frontend")
	if err != nil {
		panic(err)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// If the path is empty, it means it's the root, so serve index.html
		if path == "/" || path == "" {
			path = "/index.html"
		}

		// Check if the file exists in the embedded filesystem.
		f, err := sub.Open(strings.TrimPrefix(path, "/"))
		if err != nil {
			// If the file does not exist, and it's not a request for a static asset (like .js, .css),
			// then it's likely a client-side route. In that case, we should serve the index.html
			// and let the client-side router handle it.
			if os.IsNotExist(err) && !strings.Contains(path, ".") {
				path = "/index.html"
				f, err = sub.Open("index.html")
			} else {
				// It's a genuine 404 for a missing asset.
				http.NotFound(w, r)
				return
			}
		}

		if err != nil {
			// If we still have an error, something is wrong.
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		defer f.Close()

		fileContent, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "Error reading file", http.StatusInternalServerError)
			return
		}

		// 替换为请求的url对应的domain，使其在反向代理或不同域名下也能正常工作。
		backendHost := os.Getenv("EXCALIDRAW_BACKEND_HOST")
		if backendHost == "" {
			backendHost = r.Host
		}
		modifiedContent := strings.ReplaceAll(string(fileContent), "firestore.googleapis.com", backendHost)
		modifiedContent = strings.ReplaceAll(modifiedContent, "ssl=!0", "ssl=0")
		modifiedContent = strings.ReplaceAll(modifiedContent, "ssl:!0", "ssl:0")

		// Set the correct Content-Type based on the file extension
		contentType := http.DetectContentType([]byte(modifiedContent))
		switch {
		case strings.HasSuffix(path, ".js"):
			contentType = "application/javascript"
		case strings.HasSuffix(path, ".html"):
			contentType = "text/html"
		case strings.HasSuffix(path, ".css"):
			contentType = "text/css"
		case strings.HasSuffix(path, ".wasm"):
			contentType = "application/wasm"
		case strings.HasSuffix(path, ".tsx"):
			contentType = "text/typescript"
		case strings.HasSuffix(path, ".png"):
			contentType = "image/png"
		case strings.HasSuffix(path, ".woff2"):
			contentType = "font/woff2"
		}

		// Serve the modified content
		w.Header().Set("Content-Type", contentType)
		_, err = w.Write([]byte(modifiedContent))
		if err != nil {
			http.Error(w, "Error serving file", http.StatusInternalServerError)
			return
		}
	}
}

func setupRouter(store stores.Store) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.Logger)

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*", "http://*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Content-Length", "X-CSRF-Token", "Token", "session", "Origin", "Host", "Connection", "Accept-Encoding", "Accept-Language", "X-Requested-With"},
		AllowCredentials: true,
		MaxAge:           300, // Maximum value not ignored by any of major browsers
	}))

	r.Route("/v1/projects/{project_id}/databases/{database_id}", func(r chi.Router) {
		r.Post("/documents:commit", firebase.HandleBatchCommit())
		r.Post("/documents:batchGet", firebase.HandleBatchGet())
	})

	r.Route("/api/v2", func(r chi.Router) {
		// Route for canvases, protected by JWT auth
		r.Group(func(r chi.Router) {
			r.Use(authMiddleware.AuthJWT)
			r.Route("/kv", func(r chi.Router) {
				r.Get("/", kv.HandleListCanvases(store))
				r.Route("/{key}", func(r chi.Router) {
					r.Get("/", kv.HandleGetCanvas(store))
					r.Put("/", kv.HandleSaveCanvas(store))
					r.Delete("/", kv.HandleDeleteCanvas(store))
				})
			})
			r.Route("/chat", func(r chi.Router) {
				r.Post("/completions", openai.HandleChatCompletion())
			})
		})

		// Room files (images) for shared "#room=" links. Anonymous like the
		// scene endpoints; content is opaque (encrypted client-side).
		r.Put("/files/rooms/{roomId}/{fileId}", files.HandlePut())
		r.Get("/files/rooms/{roomId}/{fileId}", files.HandleGet())

		// Old routes for anonymous document sharing
		r.Post("/post/", documents.HandleCreate(store))
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", documents.HandleGet(store))
		})
	})

	r.Route("/auth", func(r chi.Router) {
		r.Get("/login", auth.HandleLogin)
		r.Get("/callback", auth.HandleCallback)
	})

	return r
}

func setupSocketIO() *socketio.Server {
	opts := socketio.DefaultServerOptions()
	opts.SetMaxHttpBufferSize(5000000)
	opts.SetPath("/socket.io")
	opts.SetAllowEIO3(true)
	opts.SetCors(&types.Cors{
		Origin:      "*",
		Credentials: true,
	})
	ioo := socketio.NewServer(nil, opts)

	ioo.On("connection", func(clients ...any) {
		socket := clients[0].(*socketio.Socket)
		me := socket.Id()
		myRoom := socketio.Room(me)

		// Track liveness: bump a timestamp on every packet (including ping/pong) so
		// the reconciler can detect sockets that went silent even when their
		// transport was never marked closed (e.g. a connection wedged behind a proxy).
		touch := func(...any) { socketActivity.Store(me, time.Now()) }
		touch()
		socket.Conn().On("packet", touch)
		socket.Conn().On("heartbeat", touch)

		ioo.To(myRoom).Emit("init-room")
		utils.Log().Println("init room ", myRoom)
		socket.On("join-room", func(datas ...any) {
			room := socketio.Room(datas[0].(string))
			utils.Log().Printf("Socket %v has joined %v\n", me, room)
			socket.Join(room)

			// Build the member list from sockets whose transport is still open,
			// so ghost connections (transport closed but socket.io's disconnect
			// lifecycle never completed) don't inflate the collaborator count that
			// every client renders from room-user-change. See reconcileRooms.
			roomUsers := liveRoomUsers(ioo, room, "")
			if len(roomUsers) <= 1 {
				ioo.To(myRoom).Emit("first-in-room")
			} else {
				utils.Log().Printf("emit new user %v in room %v\n", me, room)
				socket.Broadcast().To(room).Emit("new-user", me)
			}
			utils.Log().Println(" room ", room, " has users ", roomUsers)
			ioo.In(room).Emit("room-user-change", roomUsers)
		})
		socket.On("server-broadcast", func(datas ...any) {
			roomID := datas[0].(string)
			utils.Log().Printf(" user %v sends update to room %v\n", me, roomID)
			socket.Broadcast().To(socketio.Room(roomID)).Emit("client-broadcast", datas[1], datas[2])
		})
		socket.On("server-volatile-broadcast", func(datas ...any) {
			roomID := datas[0].(string)
			utils.Log().Printf(" user %v sends volatile update to room %v\n", me, roomID)
			socket.Volatile().Broadcast().To(socketio.Room(roomID)).Emit("client-broadcast", datas[1], datas[2])
		})

		socket.On("user-follow", func(datas ...any) {
			// TODO()

		})
		socket.On("disconnecting", func(datas ...any) {
			for _, currentRoom := range socket.Rooms().Keys() {
				if currentRoom == socketio.Room(me) {
					continue // skip the socket's own personal room
				}
				utils.Log().Printf("disconnecting %v from room %v\n", me, currentRoom)
				otherClients := liveRoomUsers(ioo, currentRoom, me)
				if len(otherClients) > 0 {
					utils.Log().Printf("leaving user, room %v has users  %v\n", currentRoom, otherClients)
					ioo.In(currentRoom).Emit("room-user-change", otherClients)
				}
			}
		})
		socket.On("disconnect", func(datas ...any) {
			socketActivity.Delete(me)
			socket.RemoveAllListeners("")
			socket.Disconnect(true)
		})
	})

	// Safety net: prune ghost sockets and re-broadcast room membership on an
	// interval, so the collaborator count self-heals even when a client's
	// disconnect lifecycle never fires (backgrounded tab, dropped Wi-Fi).
	ghostStaleAfter = ghostStaleThreshold()
	go reconcileRoomsLoop(ioo)

	return ioo

}

// lastRoomUsers remembers the last member list broadcast per room (as a sorted
// CSV of socket IDs) so reconcileRooms only re-emits when the set truly changes.
var lastRoomUsers sync.Map

// socketActivity records the last time each socket sent any packet (including
// heartbeats). Sockets silent past ghostStaleAfter are treated as dead even if
// their transport was never marked closed.
var socketActivity sync.Map

// ghostStaleAfter is how long a socket may go without any packet before the
// reconciler treats it as a ghost. Kept above the engine.io ping interval+timeout
// (25s+20s) so healthy idle clients are never dropped. Overridable via
// GHOST_STALE_SECONDS (used by tests to force fast detection).
var ghostStaleAfter = 60 * time.Second

func ghostStaleThreshold() time.Duration {
	if v := os.Getenv("GHOST_STALE_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 60 * time.Second
}

// isSocketLive reports whether a socket should still count as present in a room:
// its transport must be open and it must have shown activity recently. This
// catches both ghost shapes seen in production — transport closed but the
// disconnect lifecycle never fired, and a wedged connection that stopped
// responding but was never marked closed.
func isSocketLive(sock *socketio.Socket, id socketio.SocketId) bool {
	if sock.Conn().ReadyState() != "open" {
		return false
	}
	if v, ok := socketActivity.Load(id); ok {
		if time.Since(v.(time.Time)) > ghostStaleAfter {
			return false
		}
	}
	return true
}

// liveRoomUsers returns the IDs of sockets currently in `room` that are still
// live (see isSocketLive), excluding `except`. Ghost connections are skipped so
// they don't inflate the collaborator count that every client renders from the
// room-user-change event.
func liveRoomUsers(ioo *socketio.Server, room socketio.Room, except socketio.SocketId) []socketio.SocketId {
	nsp := ioo.Sockets()
	live := []socketio.SocketId{}
	members, ok := nsp.Adapter().Rooms().Load(room)
	if !ok {
		return live
	}
	sockets := nsp.Sockets()
	for _, id := range members.Keys() {
		if id == except {
			continue
		}
		if sock, ok := sockets.Load(id); ok && isSocketLive(sock, id) {
			live = append(live, id)
		}
	}
	return live
}

// reconcileRoomsLoop runs reconcileRooms on a fixed interval for the lifetime of
// the server.
func reconcileRoomsLoop(ioo *socketio.Server) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		reconcileRooms(ioo)
	}
}

// reconcileRooms disconnects dead ghost sockets and, for every shared room whose
// live membership changed since the last sweep, re-broadcasts room-user-change.
// This is the safety net for ungraceful disconnects that never fire socket.io's
// disconnect lifecycle, which would otherwise leave stale sockets in the room
// forever and steadily inflate the collaborator count.
func reconcileRooms(ioo *socketio.Server) {
	nsp := ioo.Sockets()
	sockets := nsp.Sockets()
	dead := []*socketio.Socket{}
	nsp.Adapter().Rooms().Range(func(room socketio.Room, members *types.Set[socketio.SocketId]) bool {
		// Skip personal rooms (a room named after a socket's own id).
		if _, isPersonal := sockets.Load(socketio.SocketId(room)); isPersonal {
			return true
		}
		live := []socketio.SocketId{}
		for _, id := range members.Keys() {
			sock, ok := sockets.Load(id)
			if !ok {
				continue
			}
			if isSocketLive(sock, id) {
				live = append(live, id)
			} else {
				dead = append(dead, sock) // disconnected after the range, see below
			}
		}
		if len(live) == 0 {
			lastRoomUsers.Delete(room)
			return true
		}
		key := roomUsersKey(live)
		if prev, ok := lastRoomUsers.Load(room); ok && prev.(string) == key {
			return true
		}
		lastRoomUsers.Store(room, key)
		utils.Log().Println(" reconcile room ", room, " live users ", live)
		ioo.In(room).Emit("room-user-change", live)
		return true
	})
	// Disconnect ghosts after ranging, so their disconnect lifecycle can't mutate
	// the adapter rooms we are still iterating.
	for _, sock := range dead {
		socketActivity.Delete(sock.Id())
		sock.Disconnect(true)
	}
}

// roomUsersKey returns a stable, order-independent key for a set of socket IDs.
func roomUsersKey(ids []socketio.SocketId) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = string(id)
	}
	sort.Strings(s)
	return strings.Join(s, ",")
}

func waitForShutdown(ioo *socketio.Server) {
	exit := make(chan struct{})
	SignalC := make(chan os.Signal)

	signal.Notify(SignalC, os.Interrupt, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		for s := range SignalC {
			switch s {
			case os.Interrupt, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT:
				close(exit)
				return
			}
		}
	}()

	<-exit
	ioo.Close(nil)
	os.Exit(0)
	fmt.Println("Shutting down...")
	// TODO(patwie): Close other resources
	os.Exit(0)
}

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		logrus.Info("No .env file found")
	}

	listenAddress := flag.String("listen", ":3002", "The address to listen on.")
	logLevel := flag.String("loglevel", "info", "The log level (debug, info, warn, error).")
	flag.Parse()

	level, err := logrus.ParseLevel(*logLevel)
	if err != nil {
		logrus.Fatalf("Invalid log level: %v", err)
	}
	logrus.SetLevel(level)
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	auth.InitAuth()
	openai.Init()
	store := stores.GetStore()

	r := setupRouter(store)

	ioo := setupSocketIO()
	r.Mount("/socket.io/", ioo.ServeHandler(nil))
	r.NotFound(handleUI())

	logrus.WithField("addr", *listenAddress).Info("starting server")
	go func() {
		if err := http.ListenAndServe(*listenAddress, r); err != nil {
			logrus.WithField("event", "start server").Fatal(err)
		}
	}()

	logrus.Debug("Server is running in the background")
	waitForShutdown(ioo)
}
