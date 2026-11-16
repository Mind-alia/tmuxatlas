package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/files"
	"github.com/LosFurina/tmuxatlas/pkg/identity"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

func TestFilesAPIFollowsRolesAndCeilings(t *testing.T) {
	home := t.TempDir()
	for name, content := range map[string]string{"notes.txt": "bonjour", ".ssh/id_ed25519": "SECRET", "bin.dat": "\xff\xfe"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(home, name)), 0o700)
		if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service, err := files.NewService(files.Config{Mode: files.ModeReadWrite, Root: home, Deny: files.DefaultDeny})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	store := accounts.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	hash, _ := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	admin, _ := store.Create("admin", string(hash), accounts.RoleAdmin)
	guest, _ := store.Create("alice", string(hash), accounts.RoleGuest)
	sessions := auth.NewSessionManager(time.Hour)
	a, _ := auth.NewAccountsAuth(store, sessions)
	node, _ := identity.Generate("hub")
	manager := peer.NewManager(node, nil, nil)
	manager.RegisterPeer("remote", "inv-bob@srv", "", nil)
	opts := &Options{AccountsAuth: a, PeerMgr: manager, Files: service}
	policy := newAccountPolicy(opts)
	api := newFilesAPI(opts, policy)
	router := chi.NewRouter()
	router.Use(a.Middleware, policy.Middleware)
	router.Get("/api/files/hosts", api.hosts)
	router.Get("/api/files/list", api.list)
	router.Get("/api/files/content", api.content)
	router.Get("/api/files/raw", api.raw)
	router.Put("/api/files/content", api.save)
	router.Post("/api/files/upload", api.upload)
	router.Post("/api/files/delete", api.change("delete"))
	adminToken, _ := sessions.CreateForAccount(admin.ID)
	guestToken, _ := sessions.CreateForAccount(guest.ID)
	local := manager.LocalID()
	do := func(token, method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: token})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}

	if got := do(adminToken, http.MethodGet, "/api/files/content?host_id="+local+"&path=/notes.txt", ""); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "bonjour") {
		t.Fatalf("admin read: %d %s", got.Code, got.Body)
	}
	if got := do(adminToken, http.MethodGet, "/api/files/content?host_id="+local+"&path=/.ssh/id_ed25519", ""); got.Code != http.StatusForbidden {
		t.Fatalf("secret read: %d", got.Code)
	}
	if got := do(adminToken, http.MethodGet, "/api/files/content?host_id="+local+"&path=/bin.dat", ""); got.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("binary as text: %d", got.Code)
	}
	raw := do(adminToken, http.MethodGet, "/api/files/raw?host_id="+local+"&path=/notes.txt", "")
	if raw.Code != http.StatusOK || raw.Body.String() != "bonjour" || !strings.HasPrefix(raw.Header().Get("Content-Disposition"), "attachment") ||
		raw.Header().Get("Content-Type") != "application/octet-stream" || raw.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %d %v", raw.Code, raw.Header())
	}
	// The remote host's Agent is offline: no fallback to another account.
	if got := do(adminToken, http.MethodGet, "/api/files/list?host_id=remote&path=/", ""); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("offline host: %d %s", got.Code, got.Body)
	}

	// A guest has no host: everything is refused, and the host list is empty.
	if got := do(guestToken, http.MethodGet, "/api/files/content?host_id="+local+"&path=/notes.txt", ""); got.Code != http.StatusForbidden {
		t.Fatalf("guest without grant: %d", got.Code)
	}
	if got := do(guestToken, http.MethodGet, "/api/files/hosts", ""); !strings.Contains(got.Body.String(), `"hosts":[]`) {
		t.Fatalf("guest hosts: %s", got.Body)
	}
	if _, err := store.SetAccess(guest.ID, []string{local}, "", nil); err != nil {
		t.Fatal(err)
	}
	// Granted, but the hub's own host stays out of reach of guests.
	if got := do(guestToken, http.MethodGet, "/api/files/list?host_id="+local+"&path=/", ""); got.Code != http.StatusForbidden {
		t.Fatalf("guest on hub host: %d", got.Code)
	}
	opts.GuestLocalHost = true
	listing := do(guestToken, http.MethodGet, "/api/files/list?host_id="+local+"&path=/", "")
	if listing.Code != http.StatusOK || strings.Contains(listing.Body.String(), ".ssh") || !strings.Contains(listing.Body.String(), "notes.txt") {
		t.Fatalf("guest listing: %d %s", listing.Code, listing.Body)
	}
	if got := do(guestToken, http.MethodGet, "/api/files/list?host_id=remote&path=/", ""); got.Code != http.StatusForbidden {
		t.Fatalf("guest on another host: %d", got.Code)
	}

	// Save with conflict detection, upload, delete.
	var opened struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(do(adminToken, http.MethodGet, "/api/files/content?host_id="+local+"&path=/notes.txt", "").Body.Bytes(), &opened)
	if got := do(adminToken, http.MethodPut, "/api/files/content?host_id="+local+"&path=/notes.txt&version=0-0", "x"); got.Code != http.StatusConflict {
		t.Fatalf("stale save: %d", got.Code)
	}
	if got := do(adminToken, http.MethodPut, "/api/files/content?host_id="+local+"&path=/notes.txt&version="+opened.Version, "salut"); got.Code != http.StatusOK {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	big := strings.Repeat("a", 3*files.ChunkSize+5)
	if got := do(guestToken, http.MethodPost, "/api/files/upload?host_id="+local+"&path=/big.txt", big); got.Code != http.StatusOK {
		t.Fatalf("chunked upload: %d %s", got.Code, got.Body)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "big.txt")); string(data) != big {
		t.Fatalf("uploaded %d bytes", len(data))
	}
	if got := do(guestToken, http.MethodPost, "/api/files/upload?host_id="+local+"&path=/big.txt", "x"); got.Code != http.StatusConflict {
		t.Fatalf("upload over a file without overwrite: %d", got.Code)
	}
	if got := do(guestToken, http.MethodPost, "/api/files/delete?host_id="+local, `{"path":"/big.txt"}`); got.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", got.Code, got.Body)
	}
}
