// Package versioning implements logical file locking (checkout/check-in) and
// ordinary file version history, as specified in
// filebrowser_fork_locking_versioning_spec.md. It is independent of the
// concrete storage backend (see storage/bolt/versioning.go for the BoltDB
// implementation) and of the HTTP transport (see http/versioning.go).
package versioning

import (
	"errors"
	"time"
)

// SourceAction records what kind of operation created a version.
type SourceAction string

const (
	SourceActionCreate  SourceAction = "CREATE"
	SourceActionCheckin SourceAction = "CHECKIN"
	SourceActionImport  SourceAction = "IMPORT"
)

// LockStatusValue is informational only; a STALE lock is still fully enforced.
type LockStatusValue string

const (
	LockStatusActive LockStatusValue = "ACTIVE"
	LockStatusStale  LockStatusValue = "STALE"
)

// DefaultSourceID is used as the source/root identifier. This fork only
// supports a single configured root (settings.Server.Root), so every logical
// file uses this constant rather than a real multi-root identifier.
const DefaultSourceID = "default"

// LogicalFile is one user-visible file plus all of its historical versions.
// FileID is immutable and independent of CanonicalPath.
//
// NOTE: none of these fields carry json tags. These structs are persisted
// through storm's JSON-based codec, so a `json:"-"` (or any tag storm's codec
// would also honor) silently drops the field from the stored document even
// though storm's secondary index still finds it — the field then always reads
// back as its zero value. The HTTP layer never marshals these structs
// directly; it always converts to dedicated DTOs (see http/versioning.go), so
// there is no need for json tags here at all.
type LogicalFile struct {
	FileID        string `storm:"id"`
	SourceID      string
	CanonicalPath string
	// PathKey is SourceID+"\x00"+CanonicalPath, kept unique so storm rejects a
	// second active logical file at the same path at the storage layer, in
	// addition to the in-process guard.
	PathKey         string `storm:"unique"`
	CurrentVersion  int
	CurrentSize     int64
	CurrentSHA256   string
	CreatedAt       time.Time
	CreatedByUserID uint
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

// FileVersion is an immutable snapshot of a logical file's content and
// metadata, taken at a successful creation, check-in, or import.
type FileVersion struct {
	VersionID     string `storm:"id"`
	FileID        string `storm:"index"`
	VersionNumber int
	// FileVersionKey is FileID+"\x00"+VersionNumber, unique so version numbers
	// can never collide for the same logical file.
	FileVersionKey     string `storm:"unique"`
	ObjectKey          string
	Size               int64
	SHA256             string
	CreatedAt          time.Time
	CreatedByUserID    uint
	Comment            string
	OriginalUploadName string
	SourceAction       SourceAction
}

// FileLock is the persistent business-state lock covering a whole logical
// file (current version and every historical version).
type FileLock struct {
	FileID              string `storm:"id"`
	OwnerUserID         uint
	LockedAt            time.Time
	CheckoutVersion     int
	Comment             string
	LastOwnerActivityAt time.Time
	Status              LockStatusValue
}

// AuditEvent is an immutable, append-only audit log entry.
type AuditEvent struct {
	EventID       string    `storm:"id"`
	Timestamp     time.Time `storm:"index"`
	ActorUserID   *uint
	Action        string
	FileID        string `storm:"index"`
	SourceID      string
	CanonicalPath string
	VersionNumber *int
	RelatedUserID *uint
	Details       string
}

// Required audit actions (spec section 8.4).
const (
	ActionFileImported       = "FILE_IMPORTED"
	ActionFileCreated        = "FILE_CREATED"
	ActionCheckoutAcquired   = "CHECKOUT_ACQUIRED"
	ActionCurrentDownload    = "CURRENT_DOWNLOAD"
	ActionHistoricalDownload = "HISTORICAL_DOWNLOAD"
	ActionDownloadRejected   = "DOWNLOAD_REJECTED_LOCKED"
	ActionCheckinStarted     = "CHECKIN_STARTED"
	ActionCheckinCompleted   = "CHECKIN_COMPLETED"
	ActionCheckinFailed      = "CHECKIN_FAILED"
	ActionOwnerCancelled     = "OWNER_CHECKOUT_CANCELLED"
	ActionAdminForceUnlock   = "ADMIN_FORCE_UNLOCK"
	ActionVersionCreated     = "VERSION_CREATED"
	ActionFileRenamed        = "FILE_RENAMED"
	ActionFileMoved          = "FILE_MOVED"
	ActionFileCopied         = "FILE_COPIED"
	ActionFileDeleted        = "FILE_DELETED"
	ActionIntegrityError     = "INTEGRITY_ERROR"
)

// Sentinel errors, mapped to API error codes / HTTP status in http/versioning.go.
var (
	ErrFileLocked          = errors.New("file is locked by another user")
	ErrLockNotOwned        = errors.New("file is not locked by the requesting user")
	ErrLockRequired        = errors.New("a checkout is required before downloading this file")
	ErrVersionNotFound     = errors.New("version not found")
	ErrVersionChanged      = errors.New("current version changed since the request was prepared")
	ErrFileNotManaged      = errors.New("file is not yet managed by the versioning system")
	ErrInvalidToken        = errors.New("checkout token is invalid, expired, or already used")
	ErrOwnerCancelDisabled = errors.New("owner cancel checkout is disabled by configuration")
	ErrNotFound            = errors.New("not found")
	ErrAlreadyExists       = errors.New("already exists")
)
