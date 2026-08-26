package versioning

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
)

// ObjectStore persists immutable version content outside the browsable user
// file tree (spec section 7). Objects are addressed by their SHA-256 hex
// digest and laid out as objects/<ab>/<cd>/<rest-of-hash> to avoid huge flat
// directories.
type ObjectStore struct {
	fs   afero.Fs
	root string
}

// NewObjectStore creates an ObjectStore rooted at root (an absolute path
// outside any browsable scope). It creates the objects/ and tmp/
// subdirectories if they do not already exist.
func NewObjectStore(root string) (*ObjectStore, error) {
	fs := afero.NewOsFs()
	for _, sub := range []string{"objects", "tmp"} {
		if err := fs.MkdirAll(filepath.Join(root, sub), 0o750); err != nil {
			return nil, fmt.Errorf("versioning: cannot create %s: %w", sub, err)
		}
	}
	return &ObjectStore{fs: fs, root: root}, nil
}

func (o *ObjectStore) objectPath(key string) string {
	if len(key) < 4 {
		return filepath.Join(o.root, "objects", key)
	}
	return filepath.Join(o.root, "objects", key[0:2], key[2:4], key)
}

// Put streams r into a randomized temporary file on the same filesystem,
// computes its SHA-256 and size, then atomically renames it into place at its
// content-addressed path. It never buffers the whole content in memory.
// Returns the object key (sha256 hex), size, and sha256 hex (same as key).
func (o *ObjectStore) Put(r io.Reader) (key string, size int64, sha256Hex string, err error) {
	tmp, err := afero.TempFile(o.fs, filepath.Join(o.root, "tmp"), "upload-*")
	if err != nil {
		return "", 0, "", err
	}
	tmpName := tmp.Name()
	// Always cleaned up: on success the file has already been renamed away, so
	// this Remove is a (harmless) no-op; on any error it deletes the partial
	// temp file instead of leaking it.
	defer func() { _ = o.fs.Remove(tmpName) }()

	h := sha256.New()
	n, err := io.Copy(tmp, io.TeeReader(r, h))
	if err != nil {
		tmp.Close()
		return "", 0, "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", 0, "", err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, "", err
	}

	sum := hex.EncodeToString(h.Sum(nil))
	dst := o.objectPath(sum)

	if _, statErr := o.fs.Stat(dst); statErr == nil {
		// Content-addressed object already exists (identical bytes already
		// stored by another version): reuse it instead of erroring out.
		return sum, n, sum, nil
	}

	if err := o.fs.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", 0, "", err
	}
	if err := o.fs.Rename(tmpName, dst); err != nil {
		return "", 0, "", err
	}

	return sum, n, sum, nil
}

// Open opens an immutable object for reading by its key.
func (o *ObjectStore) Open(key string) (afero.File, error) {
	f, err := o.fs.Open(o.objectPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

// Delete removes an object. Because Put can return an existing object key for
// byte-identical content (see above), deleting an object is only safe once its
// reference count is known to be zero. The MVP does not implement reference
// counting or a retention/purge job (deferred, see spec section 7.4), so
// nothing in this package currently calls Delete; it is kept for a future
// admin purge command that will need to compute reference counts first.
func (o *ObjectStore) Delete(key string) error {
	err := o.fs.Remove(o.objectPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
