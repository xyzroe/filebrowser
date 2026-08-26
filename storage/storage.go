package storage

import (
	"github.com/filebrowser/filebrowser/v2/auth"
	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/share"
	"github.com/filebrowser/filebrowser/v2/users"
	"github.com/filebrowser/filebrowser/v2/versioning"
)

// Storage is a storage powered by a Backend which makes the necessary
// verifications when fetching and saving data to ensure consistency.
type Storage struct {
	Users    users.Store
	Share    *share.Storage
	Auth     *auth.Storage
	Settings *settings.Storage

	// Versioning is the raw locking/versioning data-access backend. Unlike the
	// other fields, the higher-level policy service (versioning.Service) that
	// wraps it is constructed at server-start time in cmd/root.go, since it
	// needs deployment configuration (settings.Server) that is not available
	// yet when Storage is built.
	Versioning versioning.Backend
}
