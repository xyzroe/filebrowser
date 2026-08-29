package settings

import (
	"crypto/rand"
	"io/fs"
	"log"
	"strings"
	"time"

	"github.com/filebrowser/filebrowser/v2/rules"
)

const DefaultUsersHomeBasePath = "/users"
const DefaultLogoutPage = "/login"
const DefaultMinimumPasswordLength = 12
const DefaultFileMode = 0640
const DefaultDirMode = 0750

// AuthMethod describes an authentication method.
type AuthMethod string

// Settings contain the main settings of the application.
type Settings struct {
	Key                   []byte              `json:"key"`
	Signup                bool                `json:"signup"`
	HideLoginButton       bool                `json:"hideLoginButton"`
	CreateUserDir         bool                `json:"createUserDir"`
	UserHomeBasePath      string              `json:"userHomeBasePath"`
	Defaults              UserDefaults        `json:"defaults"`
	AuthMethod            AuthMethod          `json:"authMethod"`
	LogoutPage            string              `json:"logoutPage"`
	Branding              Branding            `json:"branding"`
	Tus                   Tus                 `json:"tus"`
	Commands              map[string][]string `json:"commands"`
	Shell                 []string            `json:"shell"`
	Rules                 []rules.Rule        `json:"rules"`
	MinimumPasswordLength uint                `json:"minimumPasswordLength"`
	FileMode              fs.FileMode         `json:"fileMode"`
	DirMode               fs.FileMode         `json:"dirMode"`
	HideDotfiles          bool                `json:"hideDotfiles"`
}

// GetRules implements rules.Provider.
func (s *Settings) GetRules() []rules.Rule {
	return s.Rules
}

// Server specific settings.
type Server struct {
	Root                   string `json:"root"`
	BaseURL                string `json:"baseURL"`
	Socket                 string `json:"socket"`
	TLSKey                 string `json:"tlsKey"`
	TLSCert                string `json:"tlsCert"`
	Port                   string `json:"port"`
	Address                string `json:"address"`
	Log                    string `json:"log"`
	EnableThumbnails       bool   `json:"enableThumbnails"`
	ResizePreview          bool   `json:"resizePreview"`
	EnableExec             bool   `json:"enableExec"`
	TypeDetectionByHeader  bool   `json:"typeDetectionByHeader"`
	ImageResolutionCal     bool   `json:"imageResolutionCalculation"`
	AuthHook               string `json:"authHook"`
	TokenExpirationTime    string `json:"tokenExpirationTime"`
	FollowExternalSymlinks bool   `json:"followExternalSymlinks"`

	// Locking and Versioning implement the checkout/check-in file locking and
	// version history feature (see filebrowser_fork_locking_versioning_spec.md).
	// Sharing controls whether public share links can be created at all; it
	// defaults to disabled in this fork because a publicly shared managed file
	// can never satisfy the checkout policy (spec section 14).
	Locking    Locking    `json:"locking"`
	Versioning Versioning `json:"versioning"`
	Sharing    Sharing    `json:"sharing"`

	// Restrictions lets an administrator globally disable rename, move, copy,
	// and whole-directory/archive download, regardless of per-user permissions.
	Restrictions Restrictions `json:"restrictions"`

	// CaseInsensitiveFs is detected from Root at startup rather than
	// configured, and tells the rule checker to match paths case-insensitively.
	// It is never persisted.
	CaseInsensitiveFs bool `json:"-"`
}

// Locking holds the checkout/check-in lock policy configuration.
type Locking struct {
	Enabled                       bool `json:"enabled"`
	AllowOwnerCancelCheckout      bool `json:"allowOwnerCancelCheckout"`
	StaleAfterDays                int  `json:"staleAfterDays"`
	ShowOwnerToUsers              bool `json:"showOwnerToUsers"`
	RequireCheckoutComment        bool `json:"requireCheckoutComment"`
	BlockAdminDownloadWhileLocked bool `json:"blockAdminDownloadWhileLocked"`
}

// Versioning holds the version-history storage/retention policy configuration.
type Versioning struct {
	Enabled                  bool   `json:"enabled"`
	StoragePath              string `json:"storagePath"`
	MaxVersionsPerFile       int    `json:"maxVersionsPerFile"`
	MaxAgeDays               int    `json:"maxAgeDays"`
	DeletedFileRetentionDays int    `json:"deletedFileRetentionDays"`
	RequireCheckinComment    bool   `json:"requireCheckinComment"`
}

// Sharing controls the public-share-link feature as a whole.
type Sharing struct {
	Enabled bool `json:"enabled"`
}

// Restrictions disables specific operations for every non-admin user,
// regardless of their individual permissions; administrators always bypass
// these restrictions.
type Restrictions struct {
	DisableCopy              bool `json:"disableCopy"`
	DisableDirectoryDownload bool `json:"disableDirectoryDownload"`
	DisableMultipleSelection bool `json:"disableMultipleSelection"`
	DisableNewFile           bool `json:"disableNewFile"`
	DisableEditor            bool `json:"disableEditor"`
}

const (
	DefaultLockingStaleAfterDays              = 30
	DefaultVersioningDeletedFileRetentionDays = 30
)

// Clean cleans any variables that might need cleaning.
func (s *Server) Clean() {
	s.BaseURL = strings.TrimSuffix(s.BaseURL, "/")
}

func (s *Server) GetTokenExpirationTime(fallback time.Duration) time.Duration {
	if s.TokenExpirationTime == "" {
		return fallback
	}

	duration, err := time.ParseDuration(s.TokenExpirationTime)
	if err != nil {
		log.Printf("[WARN] Failed to parse tokenExpirationTime: %v", err)
		return fallback
	}
	return duration
}

// GenerateKey generates a key of 512 bits.
func GenerateKey() ([]byte, error) {
	b := make([]byte, 64)
	_, err := rand.Read(b)
	// Note that err == nil only if we read len(b) bytes.
	if err != nil {
		return nil, err
	}

	return b, nil
}
