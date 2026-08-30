package bolt

import (
	"errors"
	"sort"
	"strconv"

	"github.com/asdine/storm/v3"
	"github.com/asdine/storm/v3/q"

	fberrors "github.com/filebrowser/filebrowser/v2/errors"
	"github.com/filebrowser/filebrowser/v2/versioning"
)

// versioningBackend implements versioning.Backend on top of the same BoltDB
// (via storm) used for the rest of the application's storage.
//
// Concurrency note: unlike users/shares, FileLock rows are legitimately
// updated in place (e.g. touching LastOwnerActivityAt), so CreateLock and
// UpdateLock both use a plain upsert (db.Save). This is safe because every
// mutating call goes through versioning.Storage.WithFileLock, an in-process
// per-file_id mutex that serializes all access to a given lock within this
// single supported instance (see spec section 9.4: multi-instance is out of
// scope). The PathKey/FileVersionKey unique indexes below still give true
// database-level uniqueness for logical files and versions, which are
// genuine inserts rather than read-then-update operations.
type versioningBackend struct {
	db *storm.DB
}

func newVersioningBackend(db *storm.DB) *versioningBackend {
	return &versioningBackend{db: db}
}

func (b *versioningBackend) CreateLogicalFile(lf *versioning.LogicalFile) error {
	err := b.db.Save(lf)
	if errors.Is(err, storm.ErrAlreadyExists) {
		return versioning.ErrAlreadyExists
	}
	return err
}

func (b *versioningBackend) GetLogicalFileByID(fileID string) (*versioning.LogicalFile, error) {
	var lf versioning.LogicalFile
	err := b.db.One("FileID", fileID, &lf)
	if errors.Is(err, storm.ErrNotFound) {
		return nil, fberrors.ErrNotExist
	}
	return &lf, err
}

func (b *versioningBackend) GetLogicalFileByPath(sourceID, canonicalPath string) (*versioning.LogicalFile, error) {
	var lf versioning.LogicalFile
	err := b.db.One("PathKey", sourceID+"\x00"+canonicalPath, &lf)
	if errors.Is(err, storm.ErrNotFound) {
		return nil, fberrors.ErrNotExist
	}
	return &lf, err
}

func (b *versioningBackend) UpdateLogicalFile(lf *versioning.LogicalFile) error {
	return b.db.Save(lf)
}

func (b *versioningBackend) DeleteLogicalFile(fileID string) error {
	err := b.db.DeleteStruct(&versioning.LogicalFile{FileID: fileID})
	if errors.Is(err, storm.ErrNotFound) {
		return nil
	}
	return err
}

func (b *versioningBackend) CreateVersion(v *versioning.FileVersion) error {
	err := b.db.Save(v)
	if errors.Is(err, storm.ErrAlreadyExists) {
		return versioning.ErrAlreadyExists
	}
	return err
}

func (b *versioningBackend) GetVersion(fileID string, versionNumber int) (*versioning.FileVersion, error) {
	var v versioning.FileVersion
	key := versioningKey(fileID, versionNumber)
	err := b.db.One("FileVersionKey", key, &v)
	if errors.Is(err, storm.ErrNotFound) {
		return nil, fberrors.ErrNotExist
	}
	return &v, err
}

func (b *versioningBackend) ListVersions(fileID string) ([]*versioning.FileVersion, error) {
	var versions []*versioning.FileVersion
	err := b.db.Select(q.Eq("FileID", fileID)).Find(&versions)
	if errors.Is(err, storm.ErrNotFound) {
		return []*versioning.FileVersion{}, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].VersionNumber > versions[j].VersionNumber
	})
	return versions, nil
}

func (b *versioningBackend) DeleteVersionsByFileID(fileID string) error {
	var versions []*versioning.FileVersion
	err := b.db.Select(q.Eq("FileID", fileID)).Find(&versions)
	if errors.Is(err, storm.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, v := range versions {
		if err := b.db.DeleteStruct(v); err != nil && !errors.Is(err, storm.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (b *versioningBackend) CreateLock(l *versioning.FileLock) error {
	return b.db.Save(l)
}

func (b *versioningBackend) GetLock(fileID string) (*versioning.FileLock, error) {
	var l versioning.FileLock
	err := b.db.One("FileID", fileID, &l)
	if errors.Is(err, storm.ErrNotFound) {
		return nil, fberrors.ErrNotExist
	}
	return &l, err
}

func (b *versioningBackend) UpdateLock(l *versioning.FileLock) error {
	return b.db.Save(l)
}

func (b *versioningBackend) DeleteLock(fileID string) error {
	err := b.db.DeleteStruct(&versioning.FileLock{FileID: fileID})
	if errors.Is(err, storm.ErrNotFound) {
		return nil
	}
	return err
}

func (b *versioningBackend) ListLocksByOwner(ownerUserID uint) ([]*versioning.FileLock, error) {
	var locks []*versioning.FileLock
	err := b.db.Select(q.Eq("OwnerUserID", ownerUserID)).Find(&locks)
	if errors.Is(err, storm.ErrNotFound) {
		return []*versioning.FileLock{}, nil
	}
	return locks, err
}

func (b *versioningBackend) AppendAudit(e *versioning.AuditEvent) error {
	return b.db.Save(e)
}

func (b *versioningBackend) ListAuditByFileID(fileID string) ([]*versioning.AuditEvent, error) {
	var events []*versioning.AuditEvent
	err := b.db.Select(q.Eq("FileID", fileID)).Find(&events)
	if errors.Is(err, storm.ErrNotFound) {
		return []*versioning.AuditEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.After(events[j].Timestamp)
	})
	return events, nil
}

// versioningKey mirrors the FileVersionKey format built by the versioning
// package (fileID + NUL + version number) so lookups agree with inserts.
func versioningKey(fileID string, versionNumber int) string {
	return fileID + "\x00" + strconv.Itoa(versionNumber)
}
