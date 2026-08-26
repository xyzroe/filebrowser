package bolt

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/asdine/storm/v3"

	fberrors "github.com/filebrowser/filebrowser/v2/errors"
	"github.com/filebrowser/filebrowser/v2/versioning"
)

func newTestVersioningBackend(t *testing.T) *versioningBackend {
	t.Helper()
	db, err := storm.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("storm.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return newVersioningBackend(db)
}

// TestVersioningBackendPathUniqueness verifies that two logical files cannot
// be created at the same (sourceID, canonicalPath): CreateLogicalFile must
// surface ErrAlreadyExists from the storm-level unique PathKey index.
func TestVersioningBackendPathUniqueness(t *testing.T) {
	b := newTestVersioningBackend(t)

	lf1 := &versioning.LogicalFile{
		FileID: "file-1", SourceID: "default", CanonicalPath: "/a.zip",
		PathKey: "default\x00/a.zip", CurrentVersion: 1,
	}
	if err := b.CreateLogicalFile(lf1); err != nil {
		t.Fatalf("first CreateLogicalFile: %v", err)
	}

	lf2 := &versioning.LogicalFile{
		FileID: "file-2", SourceID: "default", CanonicalPath: "/a.zip",
		PathKey: "default\x00/a.zip", CurrentVersion: 1,
	}
	if err := b.CreateLogicalFile(lf2); !errors.Is(err, versioning.ErrAlreadyExists) {
		t.Fatalf("got %v, want ErrAlreadyExists for a duplicate path", err)
	}

	got, err := b.GetLogicalFileByPath("default", "/a.zip")
	if err != nil {
		t.Fatalf("GetLogicalFileByPath: %v", err)
	}
	if got.FileID != "file-1" {
		t.Fatalf("FileID = %q, want file-1", got.FileID)
	}
}

// TestVersioningBackendVersionUniqueness verifies (FileID, VersionNumber)
// uniqueness at the storage layer.
func TestVersioningBackendVersionUniqueness(t *testing.T) {
	b := newTestVersioningBackend(t)

	v1 := &versioning.FileVersion{
		VersionID: "v1", FileID: "file-1", VersionNumber: 1,
		FileVersionKey: "file-1\x001", CreatedAt: time.Now().UTC(),
	}
	if err := b.CreateVersion(v1); err != nil {
		t.Fatalf("first CreateVersion: %v", err)
	}

	dup := &versioning.FileVersion{
		VersionID: "v2", FileID: "file-1", VersionNumber: 1,
		FileVersionKey: "file-1\x001", CreatedAt: time.Now().UTC(),
	}
	if err := b.CreateVersion(dup); !errors.Is(err, versioning.ErrAlreadyExists) {
		t.Fatalf("got %v, want ErrAlreadyExists for a duplicate version number", err)
	}

	got, err := b.GetVersion("file-1", 1)
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if got.VersionID != "v1" {
		t.Fatalf("VersionID = %q, want v1", got.VersionID)
	}
}

// TestVersioningBackendLockLifecycle exercises create/get/update/delete for a
// lock, including that GetLock/GetLogicalFileByID map "not found" to the
// package's ErrNotExist sentinel (used by the versioning package to detect
// "no lock").
func TestVersioningBackendLockLifecycle(t *testing.T) {
	b := newTestVersioningBackend(t)

	if _, err := b.GetLock("missing"); !errors.Is(err, fberrors.ErrNotExist) {
		t.Fatalf("got %v, want ErrNotExist for a missing lock", err)
	}

	lock := &versioning.FileLock{FileID: "file-1", OwnerUserID: 7, LockedAt: time.Now().UTC()}
	if err := b.CreateLock(lock); err != nil {
		t.Fatalf("CreateLock: %v", err)
	}

	got, err := b.GetLock("file-1")
	if err != nil {
		t.Fatalf("GetLock: %v", err)
	}
	if got.OwnerUserID != 7 {
		t.Fatalf("OwnerUserID = %d, want 7", got.OwnerUserID)
	}

	got.OwnerUserID = 8
	if err := b.UpdateLock(got); err != nil {
		t.Fatalf("UpdateLock: %v", err)
	}
	got2, err := b.GetLock("file-1")
	if err != nil {
		t.Fatalf("GetLock after update: %v", err)
	}
	if got2.OwnerUserID != 8 {
		t.Fatalf("OwnerUserID after update = %d, want 8", got2.OwnerUserID)
	}

	if err := b.DeleteLock("file-1"); err != nil {
		t.Fatalf("DeleteLock: %v", err)
	}
	if _, err := b.GetLock("file-1"); !errors.Is(err, fberrors.ErrNotExist) {
		t.Fatalf("got %v, want ErrNotExist after delete", err)
	}
}
