package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/viper"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/versioning"
)

// setupVersioning validates the locking/versioning configuration, and builds
// the versioning.Service used by the HTTP layer. It always returns a non-nil
// Service, even when the feature is fully disabled, so callers never need to
// nil-check it; Service.Cfg simply reflects the disabled state.
func setupVersioning(v *viper.Viper, server *settings.Server, backend versioning.Backend) (*versioning.Service, error) {
	if server.Locking.Enabled && !server.Versioning.Enabled {
		return nil, errors.New("locking.enabled requires versioning.enabled: checkout locks apply to the logical file that versioning creates")
	}

	redisCacheURL := v.GetString("redisCacheUrl")
	if (server.Locking.Enabled || server.Versioning.Enabled) && redisCacheURL != "" {
		return nil, errors.New(
			"locking/versioning only supports a single application instance (see spec section 9.4), " +
				"but --redisCacheUrl configures a Redis-backed upload cache for multi-instance deployments; " +
				"disable locking.enabled/versioning.enabled or stop using --redisCacheUrl")
	}

	var objects *versioning.ObjectStore
	if server.Versioning.Enabled {
		if strings.TrimSpace(server.Versioning.StoragePath) == "" {
			return nil, errors.New("versioning.enabled requires versioning.storagePath to be set")
		}

		storagePath, err := filepath.Abs(server.Versioning.StoragePath)
		if err != nil {
			return nil, fmt.Errorf("versioning.storagePath: %w", err)
		}
		server.Versioning.StoragePath = storagePath

		if err := validateStoragePathOutsideRoot(server.Root, storagePath); err != nil {
			return nil, err
		}

		objects, err = versioning.NewObjectStore(storagePath)
		if err != nil {
			return nil, fmt.Errorf("versioning.storagePath: %w", err)
		}
	}

	cfg := versioning.Config{
		LockingEnabled:           server.Locking.Enabled,
		VersioningEnabled:        server.Versioning.Enabled,
		AllowOwnerCancelCheckout: server.Locking.AllowOwnerCancelCheckout,
		RequireCheckoutComment:   server.Locking.RequireCheckoutComment,
		RequireCheckinComment:    server.Versioning.RequireCheckinComment,
	}

	return versioning.NewService(backend, objects, cfg), nil
}

// validateStoragePathOutsideRoot fails if storagePath is the same as, or
// nested inside, the browsable server root (spec section 15: "storagePath
// must not be inside a browsable user scope").
func validateStoragePathOutsideRoot(root, storagePath string) error {
	rootClean := filepath.Clean(root)
	storageClean := filepath.Clean(storagePath)

	if rootClean == storageClean {
		return errors.New("versioning.storagePath must not be the same directory as the server root")
	}

	rel, err := filepath.Rel(rootClean, storageClean)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("versioning.storagePath must not be inside the server root (it would become browsable)")
	}

	return nil
}

// startBackgroundIndexer walks server.Root in the background, registering
// every pre-existing regular file as version 1 of a new logical file (spec
// sections 7.3, 21.1). It runs asynchronously so it never delays server
// startup; a file remains blocked from managed-download enforcement until its
// own indexing completes. It is safe to run repeatedly (idempotent) and to
// interrupt (resumable), since IndexPath skips paths that are already managed.
func startBackgroundIndexer(server *settings.Server, svc *versioning.Service) {
	go func() {
		rootFs := afero.NewBasePathFs(afero.NewOsFs(), server.Root)
		count := 0
		err := afero.Walk(rootFs, "/", func(walkPath string, info os.FileInfo, err error) error {
			if err != nil {
				log.Printf("WARNING: versioning: indexing: skipping %q: %v", walkPath, err)
				return nil
			}
			if info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil
			}

			canonicalPath := "/" + strings.TrimPrefix(filepath.ToSlash(walkPath), "/")
			if svc.IsIndexed(versioning.DefaultSourceID, canonicalPath) {
				return nil
			}

			if err := indexOneFile(rootFs, walkPath, canonicalPath, svc); err != nil {
				log.Printf("WARNING: versioning: indexing: failed to index %q: %v", canonicalPath, err)
				return nil
			}
			count++
			return nil
		})
		if err != nil {
			log.Printf("WARNING: versioning: startup indexing did not complete: %v", err)
			return
		}
		log.Printf("versioning: startup indexing complete, %d file(s) newly registered", count)
	}()
}

func indexOneFile(rootFs afero.Fs, walkPath, canonicalPath string, svc *versioning.Service) error {
	f, err := rootFs.Open(walkPath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	key, objSize, _, err := svc.Objects.Put(f)
	if err != nil {
		return err
	}
	if objSize != size {
		return fmt.Errorf("size changed while indexing (%d != %d), file was modified concurrently", size, objSize)
	}

	return svc.IndexPath(versioning.DefaultSourceID, canonicalPath, size, hex.EncodeToString(h.Sum(nil)), key)
}
