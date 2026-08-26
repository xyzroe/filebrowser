package fbhttp

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/asdine/storm/v3"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/storage"
	"github.com/filebrowser/filebrowser/v2/storage/bolt"
	"github.com/filebrowser/filebrowser/v2/users"
	"github.com/filebrowser/filebrowser/v2/versioning"
)

// versioningTestEnv wires a full locking/versioning stack (real BoltDB-backed
// storage + a real ObjectStore on disk) for HTTP-level integration tests,
// mirroring how cmd/root.go assembles everything at startup.
type versioningTestEnv struct {
	st             *storage.Storage
	server         *settings.Server
	svc            *versioning.Service
	key            []byte
	aliceID, bobID uint
}

func newVersioningTestEnv(t *testing.T) *versioningTestEnv {
	t.Helper()
	root := t.TempDir()

	db, err := storm.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("storm.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	st, err := bolt.NewStorage(db)
	if err != nil {
		t.Fatalf("bolt.NewStorage: %v", err)
	}

	key := []byte("test-signing-key")
	if err := st.Settings.Save(&settings.Settings{Key: key}); err != nil {
		t.Fatalf("Settings.Save: %v", err)
	}

	perm := users.Permissions{Download: true, Modify: true, Rename: true, Delete: true, Create: true}
	alice := &users.User{Username: "alice", Password: "pw", Scope: "/", Perm: perm}
	if err := st.Users.Save(alice); err != nil {
		t.Fatalf("save alice: %v", err)
	}
	bob := &users.User{Username: "bob", Password: "pw", Scope: "/", Perm: perm}
	if err := st.Users.Save(bob); err != nil {
		t.Fatalf("save bob: %v", err)
	}

	server := &settings.Server{
		Root: root,
		Locking: settings.Locking{
			Enabled:                  true,
			AllowOwnerCancelCheckout: true,
			ShowOwnerToUsers:         true,
		},
		Versioning: settings.Versioning{
			Enabled:     true,
			StoragePath: filepath.Join(t.TempDir(), "version-data"),
		},
	}

	objects, err := versioning.NewObjectStore(server.Versioning.StoragePath)
	if err != nil {
		t.Fatalf("NewObjectStore: %v", err)
	}
	svc := versioning.NewService(st.Versioning, objects, versioning.Config{
		LockingEnabled:           true,
		VersioningEnabled:        true,
		AllowOwnerCancelCheckout: true,
	})
	t.Cleanup(svc.Close)

	return &versioningTestEnv{st: st, server: server, svc: svc, key: key, aliceID: alice.ID, bobID: bob.ID}
}

// indexExistingFile writes content at a root-relative path and registers it
// as version 1, simulating the startup indexer having already run.
func (env *versioningTestEnv) indexExistingFile(t *testing.T, path, content string) {
	t.Helper()
	full := filepath.Join(env.server.Root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(full)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	key, size, sha, err := env.svc.Objects.Put(f)
	if err != nil {
		t.Fatalf("Objects.Put: %v", err)
	}
	if err := env.svc.IndexPath(versioning.DefaultSourceID, path, size, sha, key); err != nil {
		t.Fatalf("IndexPath: %v", err)
	}
}

func (env *versioningTestEnv) tokenFor(t *testing.T, id uint, username string) string {
	t.Helper()
	perm := users.Permissions{Download: true, Modify: true, Rename: true, Delete: true, Create: true}
	return signShareTestToken(t, id, username, perm, env.key)
}

func (env *versioningTestEnv) request(t *testing.T, fn handleFunc, method, path string, userID uint, username string, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("X-Auth", env.tokenFor(t, userID, username))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	handle(fn, "", env.st, env.server, env.svc).ServeHTTP(rec, req)
	return rec
}

// TestLockingVersioningHTTPFlow is an end-to-end integration test covering
// spec acceptance criteria AC-01 through AC-05: the first download locks the
// file, a second user is rejected while it is locked, the owner may
// re-download, and check-in creates a new version and releases the lock.
func TestLockingVersioningHTTPFlow(t *testing.T) {
	env := newVersioningTestEnv(t)
	env.indexExistingFile(t, "/backup.zip", "v1 bytes")

	// Available before anyone checks it out.
	rec := env.request(t, lockInfoHandler, http.MethodGet, "/resources/lock?path=/backup.zip", env.aliceID, "alice", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("lockInfo (available): status=%d body=%s", rec.Code, rec.Body)
	}
	var lockResp lockResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lockResp); err != nil {
		t.Fatalf("decode lock response: %v", err)
	}
	if lockResp.State != "available" {
		t.Fatalf("state = %q, want available", lockResp.State)
	}

	// AC-01: alice's checkout locks the file.
	rec = env.request(t, checkoutHandler, http.MethodPost, "/resources/checkout?path=/backup.zip", env.aliceID, "alice", []byte(`{}`), "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout (alice): status=%d body=%s", rec.Code, rec.Body)
	}

	// AC-02: bob is rejected with 423 while alice holds the lock, for both
	// checkout and the plain raw download route.
	rec = env.request(t, checkoutHandler, http.MethodPost, "/resources/checkout?path=/backup.zip", env.bobID, "bob", []byte(`{}`), "application/json")
	if rec.Code != http.StatusLocked {
		t.Fatalf("checkout (bob): status=%d, want 423 body=%s", rec.Code, rec.Body)
	}
	rec = env.request(t, rawHandler, http.MethodGet, "/backup.zip", env.bobID, "bob", nil, "")
	if rec.Code != http.StatusLocked {
		t.Fatalf("raw download (bob): status=%d, want 423 body=%s", rec.Code, rec.Body)
	}

	// AC-04: alice (the owner) can re-download via the plain raw route
	// without checking out again.
	rec = env.request(t, rawHandler, http.MethodGet, "/backup.zip", env.aliceID, "alice", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("raw download (alice, repeat): status=%d body=%s", rec.Code, rec.Body)
	}
	if rec.Body.String() != "v1 bytes" {
		t.Fatalf("raw download body = %q, want %q", rec.Body.String(), "v1 bytes")
	}

	// AC-05: alice checks in a new version; this both creates version 2 and
	// releases the lock.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("expectedCurrentVersion", "1"); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("comment", "updated backup"); err != nil {
		t.Fatal(err)
	}
	part, err := mw.CreateFormFile("file", "backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("v2 bytes!!")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	rec = env.request(t, checkinHandler, http.MethodPost, "/resources/checkin?path=/backup.zip", env.aliceID, "alice", buf.Bytes(), mw.FormDataContentType())
	if rec.Code != http.StatusOK {
		t.Fatalf("checkin: status=%d body=%s", rec.Code, rec.Body)
	}
	var checkinResp checkinResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &checkinResp); err != nil {
		t.Fatalf("decode checkin response: %v", err)
	}
	if checkinResp.VersionNumber != 2 {
		t.Fatalf("VersionNumber = %d, want 2", checkinResp.VersionNumber)
	}

	// The lock is released: bob can now check out the file himself.
	rec = env.request(t, checkoutHandler, http.MethodPost, "/resources/checkout?path=/backup.zip", env.bobID, "bob", []byte(`{}`), "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout (bob, after checkin): status=%d body=%s", rec.Code, rec.Body)
	}

	// AC-06/16: version history is preserved and browsable; the original
	// version 1 bytes are still downloadable by the (new) owner.
	rec = env.request(t, listVersionsHandler, http.MethodGet, "/resources/versions?path=/backup.zip", env.bobID, "bob", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("listVersions: status=%d body=%s", rec.Code, rec.Body)
	}
	var versionsResp versionsListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &versionsResp); err != nil {
		t.Fatalf("decode versions response: %v", err)
	}
	if len(versionsResp.Versions) != 2 {
		t.Fatalf("got %d versions, want 2", len(versionsResp.Versions))
	}

	rec = env.request(t, versionDownloadHandler, http.MethodGet, "/resources/versions/download?path=/backup.zip&version=1", env.bobID, "bob", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("download version 1 (bob, new owner): status=%d body=%s", rec.Code, rec.Body)
	}
	if rec.Body.String() != "v1 bytes" {
		t.Fatalf("version 1 body = %q, want %q", rec.Body.String(), "v1 bytes")
	}
}

// TestForceUnlockHTTP covers spec AC-08: a non-administrator's force-unlock
// attempt must be rejected regardless of reason.
func TestForceUnlockHTTP(t *testing.T) {
	env := newVersioningTestEnv(t)
	env.indexExistingFile(t, "/backup.zip", "v1 bytes")

	rec := env.request(t, checkoutHandler, http.MethodPost, "/resources/checkout?path=/backup.zip", env.aliceID, "alice", []byte(`{}`), "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout: status=%d body=%s", rec.Code, rec.Body)
	}

	rec = env.request(t, forceUnlockHandler, http.MethodPost, "/resources/unlock?path=/backup.zip", env.bobID, "bob", []byte(`{"reason":"test"}`), "application/json")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin force-unlock: status=%d, want 403 body=%s", rec.Code, rec.Body)
	}
}
