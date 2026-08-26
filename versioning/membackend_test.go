package versioning

import (
	"sync"
)

// memBackend is a minimal in-memory Backend implementation used to unit-test
// Service's business logic and concurrency behavior without depending on a
// concrete database. It is intentionally simple (no secondary index
// structures): correctness under concurrent access relies on Service always
// serializing access to a given fileID/path through Storage.WithFileLock /
// WithPathLock, exactly as the real BoltDB-backed implementation does (see
// storage/bolt/versioning.go's doc comment).
type memBackend struct {
	mu       sync.Mutex
	files    map[string]*LogicalFile // by FileID
	byPath   map[string]string       // PathKey -> FileID
	versions map[string]*FileVersion // by FileVersionKey
	locks    map[string]*FileLock    // by FileID
	audit    []*AuditEvent
}

func newMemBackend() *memBackend {
	return &memBackend{
		files:    map[string]*LogicalFile{},
		byPath:   map[string]string{},
		versions: map[string]*FileVersion{},
		locks:    map[string]*FileLock{},
	}
}

func (b *memBackend) CreateLogicalFile(lf *LogicalFile) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.byPath[lf.PathKey]; ok {
		return ErrAlreadyExists
	}
	cp := *lf
	b.files[lf.FileID] = &cp
	b.byPath[lf.PathKey] = lf.FileID
	return nil
}

func (b *memBackend) GetLogicalFileByID(fileID string) (*LogicalFile, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	lf, ok := b.files[fileID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *lf
	return &cp, nil
}

func (b *memBackend) GetLogicalFileByPath(sourceID, canonicalPath string) (*LogicalFile, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id, ok := b.byPath[pathKey(sourceID, canonicalPath)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *b.files[id]
	return &cp, nil
}

func (b *memBackend) UpdateLogicalFile(lf *LogicalFile) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := *lf
	b.files[lf.FileID] = &cp
	b.byPath[lf.PathKey] = lf.FileID
	return nil
}

func (b *memBackend) DeleteLogicalFile(fileID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if lf, ok := b.files[fileID]; ok {
		delete(b.byPath, lf.PathKey)
		delete(b.files, fileID)
	}
	return nil
}

func (b *memBackend) CreateVersion(v *FileVersion) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.versions[v.FileVersionKey]; ok {
		return ErrAlreadyExists
	}
	cp := *v
	b.versions[v.FileVersionKey] = &cp
	return nil
}

func (b *memBackend) GetVersion(fileID string, versionNumber int) (*FileVersion, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.versions[versionKey(fileID, versionNumber)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (b *memBackend) ListVersions(fileID string) ([]*FileVersion, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*FileVersion
	for _, v := range b.versions {
		if v.FileID == fileID {
			cp := *v
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (b *memBackend) DeleteVersionsByFileID(fileID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, v := range b.versions {
		if v.FileID == fileID {
			delete(b.versions, k)
		}
	}
	return nil
}

func (b *memBackend) CreateLock(l *FileLock) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := *l
	b.locks[l.FileID] = &cp
	return nil
}

func (b *memBackend) GetLock(fileID string) (*FileLock, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.locks[fileID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *l
	return &cp, nil
}

func (b *memBackend) UpdateLock(l *FileLock) error {
	return b.CreateLock(l)
}

func (b *memBackend) DeleteLock(fileID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.locks, fileID)
	return nil
}

func (b *memBackend) AppendAudit(e *AuditEvent) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.audit = append(b.audit, e)
	return nil
}

func (b *memBackend) ListAuditByFileID(fileID string) ([]*AuditEvent, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*AuditEvent
	for _, e := range b.audit {
		if e.FileID == fileID {
			out = append(out, e)
		}
	}
	return out, nil
}

var _ Backend = (*memBackend)(nil)
