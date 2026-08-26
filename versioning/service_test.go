package versioning

import (
	"bytes"
	"errors"
	"sync"
	"testing"
)

func newTestService(t *testing.T, cfg Config) *Service {
	t.Helper()
	objects, err := NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewObjectStore: %v", err)
	}
	svc := NewService(newMemBackend(), objects, cfg)
	t.Cleanup(svc.Close)
	return svc
}

func indexTestFile(t *testing.T, svc *Service, path, content string) *LogicalFile {
	t.Helper()
	key, size, sha, err := svc.Objects.Put(bytes.NewReader([]byte(content)))
	if err != nil {
		t.Fatalf("Objects.Put: %v", err)
	}
	if err := svc.IndexPath(DefaultSourceID, path, size, sha, key); err != nil {
		t.Fatalf("IndexPath: %v", err)
	}
	lf, err := svc.GetManaged(DefaultSourceID, path)
	if err != nil {
		t.Fatalf("GetManaged: %v", err)
	}
	return lf
}

// TestCheckoutConcurrencyExactlyOneOwner is the mandatory concurrency test
// from spec section 22.3 #1: 20 distinct users simultaneously check out the
// same unlocked file; exactly one must succeed and the other 19 must receive
// ErrFileLocked (423 Locked at the HTTP layer). Run with -race.
func TestCheckoutConcurrencyExactlyOneOwner(t *testing.T) {
	svc := newTestService(t, Config{VersioningEnabled: true, LockingEnabled: true})
	lf := indexTestFile(t, svc, "/controller-backup.zip", "v1 bytes")

	const users = 20
	var wg sync.WaitGroup
	results := make([]error, users)
	start := make(chan struct{})

	for i := 0; i < users; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, err := svc.Checkout(DefaultSourceID, lf.CanonicalPath, uint(i+1), "")
			results[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrFileLocked):
			// expected for everyone who lost the race
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("got %d successful checkouts, want exactly 1", succeeded)
	}

	lock, err := svc.storage.back.GetLock(lf.FileID)
	if err != nil {
		t.Fatalf("expected a lock to exist: %v", err)
	}
	if lock.OwnerUserID == 0 {
		t.Fatalf("lock has no owner recorded")
	}
}

// TestOwnerCanRepeatDownloadWhileLocked covers spec AC-04: the owner may
// re-download without releasing the lock, while another user is rejected.
func TestOwnerCanRepeatDownloadWhileLocked(t *testing.T) {
	svc := newTestService(t, Config{VersioningEnabled: true, LockingEnabled: true})
	lf := indexTestFile(t, svc, "/a.zip", "content")

	if _, _, err := svc.Checkout(DefaultSourceID, lf.CanonicalPath, 1, ""); err != nil {
		t.Fatalf("initial checkout: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, _, err := svc.AuthorizeDownload(DefaultSourceID, lf.CanonicalPath, 1, 0); err != nil {
			t.Fatalf("owner repeat download %d: %v", i, err)
		}
	}

	if _, _, err := svc.AuthorizeDownload(DefaultSourceID, lf.CanonicalPath, 2, 0); !errors.Is(err, ErrFileLocked) {
		t.Fatalf("other user download: got %v, want ErrFileLocked", err)
	}
}

// TestAuthorizeDownloadRequiresCheckoutFirst ensures a plain, never-checked-out
// download of an unlocked managed file is rejected rather than silently
// succeeding — the whole point of routing the first download through the
// dedicated checkout endpoint (spec section 10).
func TestAuthorizeDownloadRequiresCheckoutFirst(t *testing.T) {
	svc := newTestService(t, Config{VersioningEnabled: true, LockingEnabled: true})
	lf := indexTestFile(t, svc, "/a.zip", "content")

	if _, _, err := svc.AuthorizeDownload(DefaultSourceID, lf.CanonicalPath, 1, 0); !errors.Is(err, ErrLockRequired) {
		t.Fatalf("got %v, want ErrLockRequired", err)
	}
}

// TestCheckinCreatesVersionAndReleasesLock covers spec AC-05.
func TestCheckinCreatesVersionAndReleasesLock(t *testing.T) {
	svc := newTestService(t, Config{VersioningEnabled: true, LockingEnabled: true})
	lf := indexTestFile(t, svc, "/a.zip", "v1")

	if _, _, err := svc.Checkout(DefaultSourceID, lf.CanonicalPath, 1, ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}

	key, size, sha, err := svc.Objects.Put(bytes.NewReader([]byte("v2 bytes")))
	if err != nil {
		t.Fatalf("Objects.Put: %v", err)
	}

	replaced := false
	newVersion, err := svc.Checkin(lf.FileID, 1, lf.CurrentVersion, key, size, sha, "updated", "local.zip", func() error {
		replaced = true
		return nil
	})
	if err != nil {
		t.Fatalf("Checkin: %v", err)
	}
	if newVersion != 2 {
		t.Fatalf("newVersion = %d, want 2", newVersion)
	}
	if !replaced {
		t.Fatalf("replaceVisible callback was not invoked")
	}

	if _, err := svc.storage.back.GetLock(lf.FileID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected lock to be released, got err=%v", err)
	}

	updated, err := svc.GetManaged(DefaultSourceID, lf.CanonicalPath)
	if err != nil {
		t.Fatalf("GetManaged: %v", err)
	}
	if updated.CurrentVersion != 2 {
		t.Fatalf("CurrentVersion = %d, want 2", updated.CurrentVersion)
	}

	// Non-destructive history (spec AC-06): version 1 must still be readable.
	if _, err := svc.Version(lf.FileID, 1); err != nil {
		t.Fatalf("version 1 should still exist: %v", err)
	}
}

// TestCheckinFailureRetainsLockAndDoesNotCreateVersion covers spec AC-15:
// a failed check-in (here, the version changed underneath the caller) must
// not release the lock or create a version.
func TestCheckinFailureRetainsLockAndDoesNotCreateVersion(t *testing.T) {
	svc := newTestService(t, Config{VersioningEnabled: true, LockingEnabled: true})
	lf := indexTestFile(t, svc, "/a.zip", "v1")

	if _, _, err := svc.Checkout(DefaultSourceID, lf.CanonicalPath, 1, ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}

	key, size, sha, err := svc.Objects.Put(bytes.NewReader([]byte("v2 bytes")))
	if err != nil {
		t.Fatalf("Objects.Put: %v", err)
	}

	wrongExpected := lf.CurrentVersion + 1
	replaceCalled := false
	_, err = svc.Checkin(lf.FileID, 1, wrongExpected, key, size, sha, "", "", func() error {
		replaceCalled = true
		return nil
	})
	if !errors.Is(err, ErrVersionChanged) {
		t.Fatalf("got %v, want ErrVersionChanged", err)
	}
	if replaceCalled {
		t.Fatalf("replaceVisible must not run when the version check fails")
	}

	if _, err := svc.storage.back.GetLock(lf.FileID); err != nil {
		t.Fatalf("lock must be retained after a failed check-in, got err=%v", err)
	}
	if _, err := svc.Version(lf.FileID, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no version 2 should have been created")
	}
}

// TestCheckinRequiresOwnership ensures a non-owner cannot check in.
func TestCheckinRequiresOwnership(t *testing.T) {
	svc := newTestService(t, Config{VersioningEnabled: true, LockingEnabled: true})
	lf := indexTestFile(t, svc, "/a.zip", "v1")

	if _, _, err := svc.Checkout(DefaultSourceID, lf.CanonicalPath, 1, ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}

	_, err := svc.Checkin(lf.FileID, 2 /* not the owner */, lf.CurrentVersion, "k", 1, "sha", "", "", func() error {
		t.Fatalf("replaceVisible must not run for a non-owner")
		return nil
	})
	if !errors.Is(err, ErrLockNotOwned) {
		t.Fatalf("got %v, want ErrLockNotOwned", err)
	}
}

// TestForceUnlockReleasesWithoutCreatingVersion covers spec AC-08.
func TestForceUnlockReleasesWithoutCreatingVersion(t *testing.T) {
	svc := newTestService(t, Config{VersioningEnabled: true, LockingEnabled: true})
	lf := indexTestFile(t, svc, "/a.zip", "v1")

	if _, _, err := svc.Checkout(DefaultSourceID, lf.CanonicalPath, 1, ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}

	if err := svc.ForceUnlock(lf.FileID, 99, "policy violation"); err != nil {
		t.Fatalf("ForceUnlock: %v", err)
	}

	if _, err := svc.storage.back.GetLock(lf.FileID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected lock to be gone after force-unlock")
	}
	versions, err := svc.storage.back.ListVersions(lf.FileID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("force-unlock must not create a version, got %d versions", len(versions))
	}

	// Now available for anyone to check out again.
	if _, _, err := svc.Checkout(DefaultSourceID, lf.CanonicalPath, 2, ""); err != nil {
		t.Fatalf("checkout after force-unlock: %v", err)
	}
}

// TestCancelCheckoutRespectsConfigAndOwnership covers spec section 6.6.
func TestCancelCheckoutRespectsConfigAndOwnership(t *testing.T) {
	svc := newTestService(t, Config{VersioningEnabled: true, LockingEnabled: true, AllowOwnerCancelCheckout: false})
	lf := indexTestFile(t, svc, "/a.zip", "v1")
	if _, _, err := svc.Checkout(DefaultSourceID, lf.CanonicalPath, 1, ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if err := svc.CancelCheckout(lf.FileID, 1, "changed my mind"); !errors.Is(err, ErrOwnerCancelDisabled) {
		t.Fatalf("got %v, want ErrOwnerCancelDisabled when disabled by config", err)
	}

	svc2 := newTestService(t, Config{VersioningEnabled: true, LockingEnabled: true, AllowOwnerCancelCheckout: true})
	lf2 := indexTestFile(t, svc2, "/b.zip", "v1")
	if _, _, err := svc2.Checkout(DefaultSourceID, lf2.CanonicalPath, 1, ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if err := svc2.CancelCheckout(lf2.FileID, 2 /* not owner */, "x"); !errors.Is(err, ErrLockNotOwned) {
		t.Fatalf("got %v, want ErrLockNotOwned for a non-owner cancel", err)
	}
	if err := svc2.CancelCheckout(lf2.FileID, 1, "changed my mind"); err != nil {
		t.Fatalf("owner cancel: %v", err)
	}
	if _, err := svc2.storage.back.GetLock(lf2.FileID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected lock released after cancel")
	}
}

// TestIndexPathIsIdempotent covers spec section 7.3 (resumable/idempotent
// indexing): indexing the same path twice must not create a second logical
// file or a second version 1.
func TestIndexPathIsIdempotent(t *testing.T) {
	svc := newTestService(t, Config{VersioningEnabled: true})
	lf := indexTestFile(t, svc, "/a.zip", "v1")

	if err := svc.IndexPath(DefaultSourceID, lf.CanonicalPath, 999, "deadbeef", "somekey"); err != nil {
		t.Fatalf("second IndexPath call: %v", err)
	}

	again, err := svc.GetManaged(DefaultSourceID, lf.CanonicalPath)
	if err != nil {
		t.Fatalf("GetManaged: %v", err)
	}
	if again.FileID != lf.FileID {
		t.Fatalf("indexing twice created a different FileID")
	}
	if again.CurrentSize == 999 {
		t.Fatalf("second index call must not have overwritten the existing logical file")
	}
}

// TestAuthorizeMutationAdminBypassesLockButDeleteNever covers spec 6.7 vs 6.8:
// rename/move allow an administrator to bypass another user's lock; delete
// never does, for anyone.
func TestAuthorizeMutationAdminBypassesLockButDeleteNever(t *testing.T) {
	svc := newTestService(t, Config{VersioningEnabled: true, LockingEnabled: true})
	lf := indexTestFile(t, svc, "/a.zip", "v1")
	if _, _, err := svc.Checkout(DefaultSourceID, lf.CanonicalPath, 1, ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}

	if _, err := svc.AuthorizeMutation(DefaultSourceID, lf.CanonicalPath, 2, false); !errors.Is(err, ErrFileLocked) {
		t.Fatalf("non-owner, non-admin rename: got %v, want ErrFileLocked", err)
	}
	if _, err := svc.AuthorizeMutation(DefaultSourceID, lf.CanonicalPath, 2, true); err != nil {
		t.Fatalf("admin rename should bypass the lock: %v", err)
	}
	if _, err := svc.AuthorizeDelete(DefaultSourceID, lf.CanonicalPath); !errors.Is(err, ErrFileLocked) {
		t.Fatalf("delete must never bypass the lock, even for callers who would pass isAdmin elsewhere: got %v", err)
	}
}
