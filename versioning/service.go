package versioning

import (
	"errors"
	"fmt"
	"io"
	"time"
)

// Config holds the runtime policy knobs from settings.Server (spec section 15).
type Config struct {
	LockingEnabled           bool
	VersioningEnabled        bool
	AllowOwnerCancelCheckout bool
	RequireCheckoutComment   bool
	RequireCheckinComment    bool
}

// Service implements the checkout/check-in/versioning business logic. It is
// transport- and filesystem-agnostic: callers (the HTTP handlers) are
// responsible for the actual visible-file I/O and hand the already-computed
// object key/size/hash to Service, plus a callback to perform the atomic
// visible-file replacement at the right point in the transaction.
type Service struct {
	storage *Storage
	Objects *ObjectStore
	Tokens  *TokenStore
	Cfg     Config
}

func NewService(backend Backend, objects *ObjectStore, cfg Config) *Service {
	return &Service{
		storage: NewStorage(backend),
		Objects: objects,
		Tokens:  NewTokenStore(),
		Cfg:     cfg,
	}
}

func (s *Service) Close() {
	s.Tokens.Close()
}

func versionKey(fileID string, versionNumber int) string {
	return fmt.Sprintf("%s\x00%d", fileID, versionNumber)
}

func pathKey(sourceID, canonicalPath string) string {
	return sourceID + "\x00" + canonicalPath
}

// GetManaged looks up the logical file for a path. It returns ErrFileNotManaged
// (not a low-level not-found) when the path has no logical file yet, since
// callers must react to "not yet managed" as a distinct, expected state (spec
// section 21).
func (s *Service) GetManaged(sourceID, canonicalPath string) (*LogicalFile, error) {
	lf, err := s.storage.back.GetLogicalFileByPath(sourceID, canonicalPath)
	if err != nil {
		return nil, ErrFileNotManaged
	}
	return lf, nil
}

func (s *Service) getLockOrNil(fileID string) (*FileLock, error) {
	lock, err := s.storage.back.GetLock(fileID)
	if err != nil {
		return nil, nil //nolint:nilerr // "no lock" is not an error condition for callers
	}
	return lock, nil
}

// LockInfo returns the logical file and its current lock (nil if unlocked)
// for the GET .../lock endpoint. Returns ErrFileNotManaged if the path is not
// yet tracked.
func (s *Service) LockInfo(sourceID, canonicalPath string) (*LogicalFile, *FileLock, error) {
	lf, err := s.GetManaged(sourceID, canonicalPath)
	if err != nil {
		return nil, nil, err
	}
	lock, _ := s.getLockOrNil(lf.FileID)
	return lf, lock, nil
}

// Version returns a single version record of a logical file (used by the HTTP
// layer to resolve the object key after AuthorizeDownload approves a request).
func (s *Service) Version(fileID string, versionNumber int) (*FileVersion, error) {
	return s.storage.back.GetVersion(fileID, versionNumber)
}

// ListVersions returns every version of a logical file, and its current lock.
func (s *Service) ListVersions(sourceID, canonicalPath string) (*LogicalFile, []*FileVersion, *FileLock, error) {
	lf, err := s.GetManaged(sourceID, canonicalPath)
	if err != nil {
		return nil, nil, nil, err
	}
	versions, err := s.storage.back.ListVersions(lf.FileID)
	if err != nil {
		return nil, nil, nil, err
	}
	lock, _ := s.getLockOrNil(lf.FileID)
	return lf, versions, lock, nil
}

// createManaged idempotently creates the logical_files row and version 1 for a
// path that does not have one yet. If the path is already managed, it returns
// the existing logical file without error (used by indexing, which must be
// resumable/idempotent per spec 7.3).
func (s *Service) createManaged(sourceID, canonicalPath string, actorID uint, size int64, sha256Hex, objectKey string, action SourceAction, comment string) (*LogicalFile, error) {
	var lf *LogicalFile
	err := s.storage.WithPathLock(sourceID, canonicalPath, func() error {
		if existing, gerr := s.storage.back.GetLogicalFileByPath(sourceID, canonicalPath); gerr == nil {
			lf = existing
			return nil
		}

		now := time.Now().UTC()
		fileID := NewID()
		lf = &LogicalFile{
			FileID:          fileID,
			SourceID:        sourceID,
			CanonicalPath:   canonicalPath,
			PathKey:         pathKey(sourceID, canonicalPath),
			CurrentVersion:  1,
			CurrentSize:     size,
			CurrentSHA256:   sha256Hex,
			CreatedAt:       now,
			CreatedByUserID: actorID,
			UpdatedAt:       now,
		}
		if err := s.storage.back.CreateLogicalFile(lf); err != nil {
			return err
		}

		v := &FileVersion{
			VersionID:       NewID(),
			FileID:          fileID,
			VersionNumber:   1,
			FileVersionKey:  versionKey(fileID, 1),
			ObjectKey:       objectKey,
			Size:            size,
			SHA256:          sha256Hex,
			CreatedAt:       now,
			CreatedByUserID: actorID,
			Comment:         comment,
			SourceAction:    action,
		}
		if err := s.storage.back.CreateVersion(v); err != nil {
			return err
		}

		action2 := ActionFileCreated
		if action == SourceActionImport {
			action2 = ActionFileImported
		}
		s.auditRaw(lf.FileID, &actorID, action2, nil, "")
		return nil
	})
	return lf, err
}

// RegisterCreated records a brand-new upload as version 1 of a new logical
// file (spec section 6.1). objectKey/size/sha256Hex must already have been
// durably written to Objects by the caller.
func (s *Service) RegisterCreated(sourceID, canonicalPath string, actorID uint, size int64, sha256Hex, objectKey string) (*LogicalFile, error) {
	return s.createManaged(sourceID, canonicalPath, actorID, size, sha256Hex, objectKey, SourceActionCreate, "")
}

// IndexPath registers a pre-existing file discovered by the startup indexer as
// version 1 of a new logical file (spec section 7.3, 21.1). Idempotent.
func (s *Service) IndexPath(sourceID, canonicalPath string, size int64, sha256Hex, objectKey string) error {
	_, err := s.createManaged(sourceID, canonicalPath, 0, size, sha256Hex, objectKey, SourceActionImport, "")
	return err
}

// IsIndexed reports whether a path already has a logical file, without
// creating one.
func (s *Service) IsIndexed(sourceID, canonicalPath string) bool {
	_, err := s.storage.back.GetLogicalFileByPath(sourceID, canonicalPath)
	return err == nil
}

// Checkout atomically acquires the whole-file lock (or verifies the requester
// already owns it) and returns a single-use download token for the current
// version. See spec sections 6.2, 9.2, 12.2.
func (s *Service) Checkout(sourceID, canonicalPath string, userID uint, comment string) (token string, lf *LogicalFile, err error) {
	lf, err = s.GetManaged(sourceID, canonicalPath)
	if err != nil {
		return "", nil, err
	}

	err = s.storage.WithFileLock(lf.FileID, func() error {
		lock, gerr := s.storage.back.GetLock(lf.FileID)
		if gerr == nil {
			if lock.OwnerUserID != userID {
				s.auditRaw(lf.FileID, &userID, ActionDownloadRejected, nil, "")
				return ErrFileLocked
			}
			lock.LastOwnerActivityAt = time.Now().UTC()
			return s.storage.back.UpdateLock(lock)
		}

		newLock := &FileLock{
			FileID:              lf.FileID,
			OwnerUserID:         userID,
			LockedAt:            time.Now().UTC(),
			CheckoutVersion:     lf.CurrentVersion,
			Comment:             comment,
			LastOwnerActivityAt: time.Now().UTC(),
			Status:              LockStatusActive,
		}
		if err := s.storage.back.CreateLock(newLock); err != nil {
			// Lost the race for the lock between the read above and this
			// insert: re-read and treat like the "already owns it" branch.
			if lock2, gerr2 := s.storage.back.GetLock(lf.FileID); gerr2 == nil && lock2.OwnerUserID == userID {
				return nil
			}
			s.auditRaw(lf.FileID, &userID, ActionDownloadRejected, nil, "")
			return ErrFileLocked
		}

		s.auditRaw(lf.FileID, &userID, ActionCheckoutAcquired, &lf.CurrentVersion, comment)
		return nil
	})
	if err != nil {
		return "", nil, err
	}

	token, err = s.Tokens.Issue(CheckoutToken{FileID: lf.FileID, UserID: userID, VersionNumber: 0})
	if err != nil {
		return "", nil, err
	}
	s.auditRaw(lf.FileID, &userID, ActionCurrentDownload, &lf.CurrentVersion, "")
	return token, lf, nil
}

// CheckoutVersion is the same as Checkout, but for a specific historical
// version (spec section 6.3). The lock still covers the whole logical file;
// CheckoutVersion is used only for its audited checkoutVersion metadata.
func (s *Service) CheckoutVersion(sourceID, canonicalPath string, versionNumber int, userID uint, comment string) (token string, lf *LogicalFile, err error) {
	lf, err = s.GetManaged(sourceID, canonicalPath)
	if err != nil {
		return "", nil, err
	}
	if _, verr := s.storage.back.GetVersion(lf.FileID, versionNumber); verr != nil {
		return "", nil, ErrVersionNotFound
	}

	err = s.storage.WithFileLock(lf.FileID, func() error {
		lock, gerr := s.storage.back.GetLock(lf.FileID)
		if gerr == nil {
			if lock.OwnerUserID != userID {
				s.auditRaw(lf.FileID, &userID, ActionDownloadRejected, &versionNumber, "")
				return ErrFileLocked
			}
			lock.LastOwnerActivityAt = time.Now().UTC()
			return s.storage.back.UpdateLock(lock)
		}

		newLock := &FileLock{
			FileID:              lf.FileID,
			OwnerUserID:         userID,
			LockedAt:            time.Now().UTC(),
			CheckoutVersion:     versionNumber,
			Comment:             comment,
			LastOwnerActivityAt: time.Now().UTC(),
			Status:              LockStatusActive,
		}
		if err := s.storage.back.CreateLock(newLock); err != nil {
			if lock2, gerr2 := s.storage.back.GetLock(lf.FileID); gerr2 == nil && lock2.OwnerUserID == userID {
				return nil
			}
			s.auditRaw(lf.FileID, &userID, ActionDownloadRejected, &versionNumber, "")
			return ErrFileLocked
		}
		s.auditRaw(lf.FileID, &userID, ActionCheckoutAcquired, &versionNumber, comment)
		return nil
	})
	if err != nil {
		return "", nil, err
	}

	token, err = s.Tokens.Issue(CheckoutToken{FileID: lf.FileID, UserID: userID, VersionNumber: versionNumber})
	if err != nil {
		return "", nil, err
	}
	s.auditRaw(lf.FileID, &userID, ActionHistoricalDownload, &versionNumber, "")
	return token, lf, nil
}

// AuthorizeDownload decides whether userID may stream bytes for
// requestedVersion (0 = current) of a managed file right now, without
// mutating any state itself. It is the single choke point every byte-serving
// route must call (spec section 10). Authorization is driven entirely by lock
// ownership: the lock must already exist (created by Checkout/CheckoutVersion)
// for anyone but the current owner to be let through, so a request that never
// went through checkout is rejected even though the caller is authenticated.
func (s *Service) AuthorizeDownload(sourceID, canonicalPath string, userID uint, requestedVersion int) (resolvedVersion int, lf *LogicalFile, err error) {
	lf, err = s.GetManaged(sourceID, canonicalPath)
	if err != nil {
		return 0, nil, err
	}

	lock, _ := s.getLockOrNil(lf.FileID)
	resolvedVersion = requestedVersion
	if resolvedVersion == 0 {
		resolvedVersion = lf.CurrentVersion
	}

	switch {
	case lock != nil && lock.OwnerUserID == userID:
		if _, verr := s.storage.back.GetVersion(lf.FileID, resolvedVersion); verr != nil {
			return 0, lf, ErrVersionNotFound
		}
		action := ActionCurrentDownload
		if requestedVersion != 0 {
			action = ActionHistoricalDownload
		}
		s.auditRaw(lf.FileID, &userID, action, &resolvedVersion, "")
		return resolvedVersion, lf, nil
	case lock != nil:
		s.auditRaw(lf.FileID, &userID, ActionDownloadRejected, &resolvedVersion, "")
		return 0, lf, ErrFileLocked
	default:
		return 0, lf, ErrLockRequired
	}
}

// AuthorizeMutation decides whether userID may rename, move, or overwrite
// (via check-in) a managed file. Unlike delete, an administrator may bypass
// the lock for rename/move (spec section 6.7).
func (s *Service) AuthorizeMutation(sourceID, canonicalPath string, userID uint, isAdmin bool) (*LogicalFile, error) {
	lf, err := s.GetManaged(sourceID, canonicalPath)
	if errors.Is(err, ErrFileNotManaged) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lock, _ := s.getLockOrNil(lf.FileID)
	if lock == nil || lock.OwnerUserID == userID || isAdmin {
		return lf, nil
	}
	return lf, ErrFileLocked
}

// AuthorizeDelete decides whether a managed file may be deleted. Deletion is
// forbidden for everyone, including administrators, while locked (spec 6.8).
func (s *Service) AuthorizeDelete(sourceID, canonicalPath string) (*LogicalFile, error) {
	lf, err := s.GetManaged(sourceID, canonicalPath)
	if errors.Is(err, ErrFileNotManaged) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lock, _ := s.getLockOrNil(lf.FileID)
	if lock != nil {
		return lf, ErrFileLocked
	}
	return lf, nil
}

// AuthorizeCopySource decides whether a managed file may be used as the
// source of a copy. Forbidden only when locked by someone else (spec 6.9).
func (s *Service) AuthorizeCopySource(sourceID, canonicalPath string, userID uint) (*LogicalFile, error) {
	lf, err := s.GetManaged(sourceID, canonicalPath)
	if errors.Is(err, ErrFileNotManaged) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lock, _ := s.getLockOrNil(lf.FileID)
	if lock != nil && lock.OwnerUserID != userID {
		return lf, ErrFileLocked
	}
	return lf, nil
}

// Relocate updates a logical file's canonical path after a successful
// rename/move on disk, preserving FileID, lock and version history.
func (s *Service) Relocate(fileID, newCanonicalPath string, actorID uint) error {
	return s.storage.WithFileLock(fileID, func() error {
		lf, err := s.storage.back.GetLogicalFileByID(fileID)
		if err != nil {
			return err
		}
		oldPath := lf.CanonicalPath
		lf.CanonicalPath = newCanonicalPath
		lf.PathKey = pathKey(lf.SourceID, newCanonicalPath)
		lf.UpdatedAt = time.Now().UTC()
		if err := s.storage.back.UpdateLogicalFile(lf); err != nil {
			return err
		}
		s.auditRaw(fileID, &actorID, ActionFileRenamed, nil, fmt.Sprintf("from=%q to=%q", oldPath, newCanonicalPath))
		return nil
	})
}

// Checkin performs the authorized replacement of the current content by the
// lock owner (spec section 6.4). objectKey/size/sha256Hex must already be
// durably written to Objects by the caller before calling Checkin.
// replaceVisible is invoked inside the per-file guard, after re-validating
// ownership and the expected current version, and before any metadata is
// committed; if it fails the lock is retained and no version is created.
func (s *Service) Checkin(fileID string, userID uint, expectedCurrentVersion int, objectKey string, size int64, sha256Hex, comment, originalName string, replaceVisible func() error) (newVersion int, err error) {
	err = s.storage.WithFileLock(fileID, func() error {
		lock, lerr := s.storage.back.GetLock(fileID)
		if lerr != nil || lock.OwnerUserID != userID {
			return ErrLockNotOwned
		}

		lf, lerr := s.storage.back.GetLogicalFileByID(fileID)
		if lerr != nil {
			return lerr
		}
		if lf.CurrentVersion != expectedCurrentVersion {
			return ErrVersionChanged
		}

		s.auditRaw(fileID, &userID, ActionCheckinStarted, &expectedCurrentVersion, "")

		if err := replaceVisible(); err != nil {
			s.auditRaw(fileID, &userID, ActionCheckinFailed, &expectedCurrentVersion, err.Error())
			return err
		}

		newVersion = lf.CurrentVersion + 1
		now := time.Now().UTC()
		v := &FileVersion{
			VersionID:          NewID(),
			FileID:             fileID,
			VersionNumber:      newVersion,
			FileVersionKey:     versionKey(fileID, newVersion),
			ObjectKey:          objectKey,
			Size:               size,
			SHA256:             sha256Hex,
			CreatedAt:          now,
			CreatedByUserID:    userID,
			Comment:            comment,
			OriginalUploadName: originalName,
			SourceAction:       SourceActionCheckin,
		}
		if err := s.storage.back.CreateVersion(v); err != nil {
			s.auditRaw(fileID, &userID, ActionCheckinFailed, &expectedCurrentVersion, err.Error())
			return err
		}

		lf.CurrentVersion = newVersion
		lf.CurrentSize = size
		lf.CurrentSHA256 = sha256Hex
		lf.UpdatedAt = now
		if err := s.storage.back.UpdateLogicalFile(lf); err != nil {
			s.auditRaw(fileID, &userID, ActionCheckinFailed, &expectedCurrentVersion, err.Error())
			return err
		}

		if err := s.storage.back.DeleteLock(fileID); err != nil {
			return err
		}

		s.auditRaw(fileID, &userID, ActionCheckinCompleted, &newVersion, comment)
		s.auditRaw(fileID, &userID, ActionVersionCreated, &newVersion, "")
		return nil
	})
	return newVersion, err
}

// CancelCheckout releases the lock without creating a version (spec 6.6).
func (s *Service) CancelCheckout(fileID string, userID uint, reason string) error {
	if !s.Cfg.AllowOwnerCancelCheckout {
		return ErrOwnerCancelDisabled
	}
	return s.storage.WithFileLock(fileID, func() error {
		lock, err := s.storage.back.GetLock(fileID)
		if err != nil || lock.OwnerUserID != userID {
			return ErrLockNotOwned
		}
		if err := s.storage.back.DeleteLock(fileID); err != nil {
			return err
		}
		s.auditRaw(fileID, &userID, ActionOwnerCancelled, nil, reason)
		return nil
	})
}

// ForceUnlock releases the lock as an administrator action (spec 6.5). No
// version is created and no content is touched.
func (s *Service) ForceUnlock(fileID string, adminUserID uint, reason string) error {
	return s.storage.WithFileLock(fileID, func() error {
		lock, err := s.storage.back.GetLock(fileID)
		if err != nil {
			return ErrLockNotOwned
		}
		if err := s.storage.back.DeleteLock(fileID); err != nil {
			return err
		}
		s.auditRaw(fileID, &adminUserID, ActionAdminForceUnlock, nil,
			fmt.Sprintf("previousOwnerUserId=%d reason=%q", lock.OwnerUserID, reason))
		return nil
	})
}

// HandleDeleted removes a logical file's metadata after its visible content
// has already been deleted (spec 6.8). The MVP does not implement the spec's
// soft-delete/retention window (section 7.4): metadata deletion is immediate
// and unconditional. AuthorizeDelete must be called beforehand to enforce
// that the file was unlocked.
func (s *Service) HandleDeleted(fileID string, actorID uint) error {
	return s.storage.WithFileLock(fileID, func() error {
		if err := s.storage.back.DeleteVersionsByFileID(fileID); err != nil {
			return err
		}
		if err := s.storage.back.DeleteLogicalFile(fileID); err != nil {
			return err
		}
		s.auditRaw(fileID, &actorID, ActionFileDeleted, nil, "")
		return nil
	})
}

// RegisterCopy creates a brand new logical file for the destination of a copy
// (spec 6.9): new FileID, version 1, no inherited lock or history. content is
// read once, streamed into the object store to compute its hash/size.
func (s *Service) RegisterCopy(sourceID, dstCanonicalPath string, actorID uint, content io.Reader) error {
	key, size, sha, err := s.Objects.Put(content)
	if err != nil {
		return err
	}
	_, err = s.createManaged(sourceID, dstCanonicalPath, actorID, size, sha, key, SourceActionCreate, "")
	if err == nil {
		if lf, lerr := s.storage.back.GetLogicalFileByPath(sourceID, dstCanonicalPath); lerr == nil {
			s.auditRaw(lf.FileID, &actorID, ActionFileCopied, nil, "")
		}
	}
	return err
}

func (s *Service) auditRaw(fileID string, actorID *uint, action string, versionNumber *int, details string) {
	_ = s.storage.back.AppendAudit(&AuditEvent{
		EventID:       NewID(),
		Timestamp:     time.Now().UTC(),
		ActorUserID:   actorID,
		Action:        action,
		FileID:        fileID,
		VersionNumber: versionNumber,
		Details:       details,
	})
}
