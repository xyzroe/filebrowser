package versioning

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Backend is the interface a concrete database implementation (e.g. BoltDB)
// must satisfy. It performs no policy checks; that is the job of Service.
type Backend interface {
	CreateLogicalFile(lf *LogicalFile) error
	GetLogicalFileByID(fileID string) (*LogicalFile, error)
	GetLogicalFileByPath(sourceID, canonicalPath string) (*LogicalFile, error)
	UpdateLogicalFile(lf *LogicalFile) error

	CreateVersion(v *FileVersion) error
	GetVersion(fileID string, versionNumber int) (*FileVersion, error)
	ListVersions(fileID string) ([]*FileVersion, error)
	DeleteVersionsByFileID(fileID string) error

	// CreateLock must fail with ErrAlreadyExists if a lock already exists for
	// FileID, since FileID is the lock's primary key and there can be no more
	// than one active lock per logical file.
	CreateLock(l *FileLock) error
	GetLock(fileID string) (*FileLock, error)
	UpdateLock(l *FileLock) error
	DeleteLock(fileID string) error

	AppendAudit(e *AuditEvent) error
	ListAuditByFileID(fileID string) ([]*AuditEvent, error)

	// DeleteLogicalFile removes the logical file, its versions and its lock.
	// MVP note: this fork does not yet implement the spec's soft-delete /
	// retention window (section 6.8, 7.4); deletion is immediate.
	DeleteLogicalFile(fileID string) error
}

// Storage wraps a Backend with the in-process per-file_id operation guard
// required by spec section 9.3. The database remains the source of truth;
// this mutex only reduces contention/interleaving within a single process.
type Storage struct {
	back Backend

	guardMu sync.Mutex
	guards  map[string]*sync.Mutex
}

func NewStorage(back Backend) *Storage {
	return &Storage{back: back, guards: map[string]*sync.Mutex{}}
}

// WithFileLock runs fn while holding the in-process guard for fileID. It must
// be used around every operation that can change a logical file: checkout,
// check-in, rename, move, delete, copy-from-source, force-unlock.
func (s *Storage) WithFileLock(fileID string, fn func() error) error {
	s.guardMu.Lock()
	m, ok := s.guards[fileID]
	if !ok {
		m = &sync.Mutex{}
		s.guards[fileID] = m
	}
	s.guardMu.Unlock()

	m.Lock()
	defer m.Unlock()
	return fn()
}

// WithPathLock runs fn while holding an in-process guard keyed by path. It is
// used before a logical file exists yet (e.g. concurrent uploads/indexing of
// the same new path), when there is no file_id to key on.
func (s *Storage) WithPathLock(sourceID, canonicalPath string, fn func() error) error {
	return s.WithFileLock("path:"+sourceID+"\x00"+canonicalPath, fn)
}

// NewID generates an immutable, sortable, unique identifier suitable for a
// FileID, VersionID, or EventID.
func NewID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%013d%s", time.Now().UTC().UnixMilli(), hex.EncodeToString(b[:]))
}
