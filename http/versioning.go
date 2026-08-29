package fbhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/filebrowser/filebrowser/v2/files"
	"github.com/filebrowser/filebrowser/v2/versioning"
)

// versioningErrorStatus maps a versioning package sentinel error to the HTTP
// status and machine-readable code from spec section 12.9.
func versioningErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, versioning.ErrFileLocked):
		return http.StatusLocked, "FILE_LOCKED"
	case errors.Is(err, versioning.ErrLockNotOwned):
		return http.StatusConflict, "LOCK_NOT_OWNED"
	case errors.Is(err, versioning.ErrLockRequired):
		return http.StatusLocked, "LOCK_REQUIRED"
	case errors.Is(err, versioning.ErrVersionNotFound):
		return http.StatusNotFound, "VERSION_NOT_FOUND"
	case errors.Is(err, versioning.ErrVersionChanged):
		return http.StatusConflict, "VERSION_CHANGED"
	case errors.Is(err, versioning.ErrFileNotManaged):
		return http.StatusConflict, "FILE_NOT_MANAGED"
	case errors.Is(err, versioning.ErrInvalidToken):
		return http.StatusBadRequest, "INVALID_TOKEN"
	case errors.Is(err, versioning.ErrOwnerCancelDisabled):
		return http.StatusForbidden, "OPERATION_BLOCKED_BY_LOCK"
	default:
		return http.StatusInternalServerError, "INTERNAL"
	}
}

type versioningErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeVersioningError renders a stable {code,message} JSON body and returns
// the matching HTTP status, mirroring how other handlers report failures
// through the (status, err) return contract but with a machine-readable code.
func writeVersioningError(w http.ResponseWriter, err error) (int, error) {
	status, code := versioningErrorStatus(err)
	body, _ := json.Marshal(versioningErrorBody{Code: code, Message: err.Error()})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, werr := w.Write(body)
	return 0, werr
}

// requirePathQuery reads and cleans the "path" query parameter used by every
// locking/versioning endpoint (spec section 12), since these are new
// endpoints that do not fit the fork's existing PathPrefix-based routing.
func requirePathQuery(r *http.Request) (string, error) {
	raw := r.URL.Query().Get("path")
	if raw == "" {
		return "", fmt.Errorf("missing required 'path' query parameter")
	}
	return slashClean(raw), nil
}

func requireVersioningEnabled(d *data) bool {
	return d.versioning != nil && d.versioning.Cfg.VersioningEnabled
}

// attachLockInfo populates fi.Lock with the file's current lock summary, so a
// directory listing (or a single-file response) can show a lock icon/owner
// without a separate request per item. It is a no-op for directories and when
// versioning is disabled, and never fails the request: a lookup error simply
// leaves fi.Lock unset.
func (d *data) attachLockInfo(fi *files.FileInfo) {
	if fi == nil || fi.IsDir || !requireVersioningEnabled(d) {
		return
	}
	canonical, err := d.canonicalPath(fi.Path)
	if err != nil {
		return
	}
	lf, lock, err := d.versioning.LockInfo(versioning.DefaultSourceID, canonical)
	if err != nil {
		if !errors.Is(err, versioning.ErrFileNotManaged) {
			log.Printf("WARNING: versioning: could not fetch lock info for %q: %v", canonical, err)
		}
		return
	}
	_ = lf

	info := &files.FileLockInfo{State: "available"}
	if lock != nil {
		info.State = "locked"
		info.LockedAt = lock.LockedAt.Format(timeFormatRFC3339)
		info.IsCurrentUserOwner = lock.OwnerUserID == d.user.ID

		showOwner := info.IsCurrentUserOwner || d.user.Perm.Admin || d.server.Locking.ShowOwnerToUsers
		if showOwner {
			if u, uerr := d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, lock.OwnerUserID); uerr == nil {
				info.OwnerUsername = u.Username
			}
		}
	}
	fi.Lock = info
}

// isLockedByOther reports whether path names a managed file currently locked
// by someone other than the requesting user. Used by routes that only need
// the weaker "not locked by someone else" check rather than full checkout
// enforcement (archive/batch download in raw.go — see the MVP limitation
// noted there: unlike a single-file download, an archive does not itself
// require the files it contains to have been checked out first).
func (d *data) isLockedByOther(path string) (bool, error) {
	if !requireVersioningEnabled(d) {
		return false, nil
	}
	canonical, err := d.canonicalPath(path)
	if err != nil {
		return false, err
	}
	_, lock, err := d.versioning.LockInfo(versioning.DefaultSourceID, canonical)
	if errors.Is(err, versioning.ErrFileNotManaged) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return lock != nil && lock.OwnerUserID != d.user.ID, nil
}

// authorizeRawDownload is the enforcement chokepoint for the plain single-file
// download route (spec section 10). It requires the file to already be locked
// by the requester (i.e. checked out via POST .../checkout beforehand); an
// unmanaged or currently-unlocked file cannot be streamed this way. Unlike the
// dedicated locking/versioning endpoints, this reuses the plain (status, err)
// contract of raw.go/resource.go rather than writing a JSON error body.
func (d *data) authorizeRawDownload(path string) (int, error) {
	if !requireVersioningEnabled(d) {
		return 0, nil
	}
	canonical, err := d.canonicalPath(path)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if _, _, err := d.versioning.AuthorizeDownload(versioning.DefaultSourceID, canonical, d.user.ID, 0); err != nil {
		status, _ := versioningErrorStatus(err)
		return status, err
	}
	return 0, nil
}

// authorizeDeleteMutation enforces spec 6.8 (delete forbidden for everyone,
// including administrators, while locked) for resourceDeleteHandler. It
// returns the logical file's FileID (empty if the path is unmanaged) so the
// caller can finalize metadata deletion after the filesystem delete succeeds.
func (d *data) authorizeDeleteMutation(path string) (fileID string, status int, err error) {
	if !requireVersioningEnabled(d) {
		return "", 0, nil
	}
	canonical, cerr := d.canonicalPath(path)
	if cerr != nil {
		return "", http.StatusInternalServerError, cerr
	}
	lf, aerr := d.versioning.AuthorizeDelete(versioning.DefaultSourceID, canonical)
	if aerr != nil {
		st, _ := versioningErrorStatus(aerr)
		return "", st, aerr
	}
	if lf == nil {
		return "", 0, nil
	}
	return lf.FileID, 0, nil
}

// authorizeMutation enforces spec 6.7 (rename/move: only the lock owner or an
// administrator may act) for resourcePatchHandler. It returns the logical
// file's FileID (empty if unmanaged) so the caller can relocate it after a
// successful rename/move.
func (d *data) authorizeMutation(path string) (fileID string, status int, err error) {
	if !requireVersioningEnabled(d) {
		return "", 0, nil
	}
	canonical, cerr := d.canonicalPath(path)
	if cerr != nil {
		return "", http.StatusInternalServerError, cerr
	}
	lf, aerr := d.versioning.AuthorizeMutation(versioning.DefaultSourceID, canonical, d.user.ID, d.user.Perm.Admin)
	if aerr != nil {
		st, _ := versioningErrorStatus(aerr)
		return "", st, aerr
	}
	if lf == nil {
		return "", 0, nil
	}
	return lf.FileID, 0, nil
}

// authorizeCopySource enforces spec 6.9 (copying a file locked by someone
// else is forbidden, since it requires reading its content).
func (d *data) authorizeCopySource(path string) (status int, err error) {
	if !requireVersioningEnabled(d) {
		return 0, nil
	}
	canonical, cerr := d.canonicalPath(path)
	if cerr != nil {
		return http.StatusInternalServerError, cerr
	}
	if _, aerr := d.versioning.AuthorizeCopySource(versioning.DefaultSourceID, canonical, d.user.ID); aerr != nil {
		st, _ := versioningErrorStatus(aerr)
		return st, aerr
	}
	return 0, nil
}

// isManaged reports whether path already has a logical file, without
// creating one (used by resourcePostHandler/resourcePutHandler to block a
// generic overwrite of a managed file — spec section 11).
func (d *data) isManaged(path string) (bool, error) {
	if !requireVersioningEnabled(d) {
		return false, nil
	}
	canonical, err := d.canonicalPath(path)
	if err != nil {
		return false, err
	}
	return d.versioning.IsIndexed(versioning.DefaultSourceID, canonical), nil
}

// registerCopyDestination records the destination of a successful copy as a
// brand new logical file (spec 6.9): new FileID, version 1, no inherited lock
// or history. Directory copies are skipped (MVP limitation: only individual
// files get their own logical file registered, not every file within a copied
// directory tree); failures are logged rather than failing the request, for
// the same reason as registerNewFile.
func (d *data) registerCopyDestination(dst string) {
	if !requireVersioningEnabled(d) {
		return
	}
	info, err := d.user.Fs.Stat(dst)
	if err != nil || info.IsDir() {
		return
	}
	canonical, err := d.canonicalPath(dst)
	if err != nil {
		log.Printf("WARNING: versioning: could not resolve canonical path for copy destination %q: %v", dst, err)
		return
	}
	f, err := d.user.Fs.Open(dst)
	if err != nil {
		log.Printf("WARNING: versioning: could not register copy destination %q: %v", canonical, err)
		return
	}
	defer f.Close()
	if err := d.versioning.RegisterCopy(versioning.DefaultSourceID, canonical, d.user.ID, f); err != nil {
		log.Printf("WARNING: versioning: could not register copy destination %q: %v", canonical, err)
	}
}

// streams the already-written file once more through the object store to
// compute its hash; failures are logged rather than failing the request,
// since the visible upload itself already succeeded and returning an error
// here would make a successful upload look like it failed to the client.
func (d *data) registerNewFile(path string) {
	if !requireVersioningEnabled(d) {
		return
	}
	canonical, err := d.canonicalPath(path)
	if err != nil {
		log.Printf("WARNING: versioning: could not resolve canonical path for %q: %v", path, err)
		return
	}
	f, err := d.user.Fs.Open(path)
	if err != nil {
		log.Printf("WARNING: versioning: could not register new file %q: %v", canonical, err)
		return
	}
	defer f.Close()

	key, size, sha, err := d.versioning.Objects.Put(f)
	if err != nil {
		log.Printf("WARNING: versioning: could not store object for new file %q: %v", canonical, err)
		return
	}
	if _, err := d.versioning.RegisterCreated(versioning.DefaultSourceID, canonical, d.user.ID, size, sha, key); err != nil {
		log.Printf("WARNING: versioning: could not register new file %q: %v", canonical, err)
	}
}

// lockOwnerInfo is the client-facing owner summary embedded in lock
// responses. Username is omitted when locking.showOwnerToUsers is disabled
// for a non-owner, non-admin requester (spec 12.1).
type lockOwnerInfo struct {
	ID       uint   `json:"id"`
	Username string `json:"username,omitempty"`
}

type lockResponse struct {
	FileID             string         `json:"fileId"`
	State              string         `json:"state"` // "unmanaged" | "available" | "locked"
	Owner              *lockOwnerInfo `json:"owner,omitempty"`
	LockedAt           string         `json:"lockedAt,omitempty"`
	CheckoutVersion    int            `json:"checkoutVersion,omitempty"`
	Comment            string         `json:"comment,omitempty"`
	IsCurrentUserOwner bool           `json:"isCurrentUserOwner"`
	CurrentVersion     int            `json:"currentVersion,omitempty"`
}

func (d *data) buildLockResponse(lf *versioning.LogicalFile, lock *versioning.FileLock) *lockResponse {
	if lf == nil {
		return &lockResponse{State: "unmanaged"}
	}
	resp := &lockResponse{FileID: lf.FileID, State: "available", CurrentVersion: lf.CurrentVersion}
	if lock == nil {
		return resp
	}
	resp.State = "locked"
	resp.LockedAt = lock.LockedAt.Format(timeFormatRFC3339)
	resp.CheckoutVersion = lock.CheckoutVersion
	resp.IsCurrentUserOwner = lock.OwnerUserID == d.user.ID

	showOwner := resp.IsCurrentUserOwner || d.user.Perm.Admin || d.server.Locking.ShowOwnerToUsers
	if showOwner {
		owner := &lockOwnerInfo{ID: lock.OwnerUserID}
		if u, err := d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, lock.OwnerUserID); err == nil {
			owner.Username = u.Username
		}
		resp.Owner = owner
		resp.Comment = lock.Comment
	}
	return resp
}

const timeFormatRFC3339 = "2006-01-02T15:04:05Z07:00"

var lockInfoHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !requireVersioningEnabled(d) {
		return http.StatusNotFound, nil
	}
	path, err := requirePathQuery(r)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if !d.Check(path) {
		return http.StatusForbidden, nil
	}

	canonical, err := d.canonicalPath(path)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	lf, lock, err := d.versioning.LockInfo(versioning.DefaultSourceID, canonical)
	if errors.Is(err, versioning.ErrFileNotManaged) {
		return renderJSON(w, r, d.buildLockResponse(nil, nil))
	}
	if err != nil {
		return http.StatusInternalServerError, err
	}

	return renderJSON(w, r, d.buildLockResponse(lf, lock))
})

type checkoutRequest struct {
	Comment string `json:"comment"`
}

type checkoutResponse struct {
	Token           string `json:"token"`
	FileID          string `json:"fileId"`
	CheckoutVersion int    `json:"checkoutVersion"`
}

var checkoutHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !requireVersioningEnabled(d) {
		return http.StatusNotFound, nil
	}
	if !d.user.Perm.Download {
		return http.StatusForbidden, nil
	}
	path, err := requirePathQuery(r)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if !d.Check(path) {
		return http.StatusForbidden, nil
	}

	var body checkoutRequest
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	}
	if d.server.Locking.RequireCheckoutComment && body.Comment == "" {
		return http.StatusBadRequest, fmt.Errorf("a checkout comment is required")
	}

	canonical, err := d.canonicalPath(path)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	token, lf, err := d.versioning.Checkout(versioning.DefaultSourceID, canonical, d.user.ID, body.Comment)
	if err != nil {
		return writeVersioningError(w, err)
	}

	return renderJSON(w, r, checkoutResponse{Token: token, FileID: lf.FileID, CheckoutVersion: lf.CurrentVersion})
})

type versionCheckoutResponse struct {
	Token         string `json:"token"`
	FileID        string `json:"fileId"`
	VersionNumber int    `json:"versionNumber"`
}

var versionCheckoutHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !requireVersioningEnabled(d) {
		return http.StatusNotFound, nil
	}
	if !d.user.Perm.Download {
		return http.StatusForbidden, nil
	}
	path, err := requirePathQuery(r)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if !d.Check(path) {
		return http.StatusForbidden, nil
	}

	versionNumber, err := strconv.Atoi(r.URL.Query().Get("version"))
	if err != nil || versionNumber < 1 {
		return http.StatusBadRequest, fmt.Errorf("invalid or missing 'version' query parameter")
	}

	var body checkoutRequest
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	}
	if d.server.Locking.RequireCheckoutComment && body.Comment == "" {
		return http.StatusBadRequest, fmt.Errorf("a checkout comment is required")
	}

	canonical, err := d.canonicalPath(path)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	token, lf, err := d.versioning.CheckoutVersion(versioning.DefaultSourceID, canonical, versionNumber, d.user.ID, body.Comment)
	if err != nil {
		return writeVersioningError(w, err)
	}

	return renderJSON(w, r, versionCheckoutResponse{Token: token, FileID: lf.FileID, VersionNumber: versionNumber})
})

// versionDownloadHandler streams a specific historical version's bytes. It is
// also used for the current version when version=0 or the parameter is
// absent. Authorization is via versioning.Service.AuthorizeDownload, which
// requires the requester to already own the file's lock (spec section 10).
var versionDownloadHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !requireVersioningEnabled(d) {
		return http.StatusNotFound, nil
	}
	if !d.user.Perm.Download {
		return http.StatusAccepted, nil
	}
	path, err := requirePathQuery(r)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if !d.Check(path) {
		return http.StatusForbidden, nil
	}

	requestedVersion := 0
	if q := r.URL.Query().Get("version"); q != "" {
		requestedVersion, err = strconv.Atoi(q)
		if err != nil || requestedVersion < 1 {
			return http.StatusBadRequest, fmt.Errorf("invalid 'version' query parameter")
		}
	}

	canonical, err := d.canonicalPath(path)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	resolvedVersion, lf, err := d.versioning.AuthorizeDownload(versioning.DefaultSourceID, canonical, d.user.ID, requestedVersion)
	if err != nil {
		return writeVersioningError(w, err)
	}

	version, err := d.versioning.Version(lf.FileID, resolvedVersion)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	obj, err := d.versioning.Objects.Open(version.ObjectKey)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	defer obj.Close()

	name := filepath.Base(canonical)
	w.Header().Set("Content-Disposition", `attachment; filename*=utf-8''`+url.PathEscape(name))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(version.Size, 10))
	_, werr := io.Copy(w, obj)
	return 0, werr
})

type versionInfo struct {
	VersionNumber      int    `json:"versionNumber"`
	CreatedAt          string `json:"createdAt"`
	CreatedBy          string `json:"createdBy,omitempty"`
	Size               int64  `json:"size"`
	SHA256             string `json:"sha256"`
	Comment            string `json:"comment,omitempty"`
	OriginalUploadName string `json:"originalUploadName,omitempty"`
	IsCurrent          bool   `json:"isCurrent"`
}

type versionsListResponse struct {
	FileID         string        `json:"fileId"`
	CurrentVersion int           `json:"currentVersion"`
	Lock           *lockResponse `json:"lock,omitempty"`
	Versions       []versionInfo `json:"versions"`
}

var listVersionsHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !requireVersioningEnabled(d) {
		return http.StatusNotFound, nil
	}
	path, err := requirePathQuery(r)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if !d.Check(path) {
		return http.StatusForbidden, nil
	}

	canonical, err := d.canonicalPath(path)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	lf, versions, lock, err := d.versioning.ListVersions(versioning.DefaultSourceID, canonical)
	if errors.Is(err, versioning.ErrFileNotManaged) {
		return renderJSON(w, r, versionsListResponse{Versions: []versionInfo{}})
	}
	if err != nil {
		return http.StatusInternalServerError, err
	}

	lockResp := d.buildLockResponse(lf, lock)

	infos := make([]versionInfo, 0, len(versions))
	for _, v := range versions {
		info := versionInfo{
			VersionNumber:      v.VersionNumber,
			CreatedAt:          v.CreatedAt.Format(timeFormatRFC3339),
			Size:               v.Size,
			SHA256:             v.SHA256,
			Comment:            v.Comment,
			OriginalUploadName: v.OriginalUploadName,
			IsCurrent:          v.VersionNumber == lf.CurrentVersion,
		}
		if u, uerr := d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, v.CreatedByUserID); uerr == nil {
			info.CreatedBy = u.Username
		}
		infos = append(infos, info)
	}

	return renderJSON(w, r, versionsListResponse{
		FileID:         lf.FileID,
		CurrentVersion: lf.CurrentVersion,
		Lock:           lockResp,
		Versions:       infos,
	})
})

type checkinResponse struct {
	VersionNumber int `json:"versionNumber"`
}

// checkinHandler implements "Check in new version" (spec section 6.4, 12.5).
// The upload is streamed to a temporary file on the same filesystem as the
// visible target (for an atomic rename), hashed while received, and only then
// is the metadata transaction (Service.Checkin) attempted.
var checkinHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !requireVersioningEnabled(d) {
		return http.StatusNotFound, nil
	}
	if !d.user.Perm.Modify {
		return http.StatusForbidden, nil
	}
	path, err := requirePathQuery(r)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if !d.Check(path) {
		return http.StatusForbidden, nil
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return http.StatusBadRequest, err
	}
	expectedVersion, err := strconv.Atoi(r.FormValue("expectedCurrentVersion"))
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("invalid or missing 'expectedCurrentVersion' form field")
	}
	comment := r.FormValue("comment")
	if d.server.Versioning.RequireCheckinComment && comment == "" {
		return http.StatusBadRequest, fmt.Errorf("a check-in comment is required")
	}

	uploaded, header, err := r.FormFile("file")
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("missing 'file' form field: %w", err)
	}
	defer uploaded.Close()

	canonical, err := d.canonicalPath(path)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	lf, err := d.versioning.GetManaged(versioning.DefaultSourceID, canonical)
	if err != nil {
		return writeVersioningError(w, err)
	}

	// Stream the upload once to a temp file on the visible filesystem (so the
	// eventual replace is a same-filesystem atomic rename), hashing as we go.
	dir := filepath.Dir(d.user.FullPath(path))
	tmpVisible, err := os.CreateTemp(dir, "checkin-*.tmp")
	if err != nil {
		return http.StatusInternalServerError, err
	}
	tmpPath := tmpVisible.Name()
	defer os.Remove(tmpPath) //nolint:errcheck // no-op once renamed into place

	if _, err := io.Copy(tmpVisible, uploaded); err != nil {
		tmpVisible.Close()
		return http.StatusInternalServerError, err
	}
	if err := tmpVisible.Sync(); err != nil {
		tmpVisible.Close()
		return http.StatusInternalServerError, err
	}
	if _, err := tmpVisible.Seek(0, io.SeekStart); err != nil {
		tmpVisible.Close()
		return http.StatusInternalServerError, err
	}

	objectKey, size, sha256Hex, err := d.versioning.Objects.Put(tmpVisible)
	tmpVisible.Close()
	if err != nil {
		return http.StatusInternalServerError, err
	}

	replaceVisible := func() error {
		if err := os.Chmod(tmpPath, os.FileMode(d.settings.FileMode)); err != nil {
			return err
		}
		return os.Rename(tmpPath, d.user.FullPath(path))
	}

	newVersion, err := d.versioning.Checkin(lf.FileID, d.user.ID, expectedVersion, objectKey, size, sha256Hex, comment, header.Filename, replaceVisible)
	if err != nil {
		return writeVersioningError(w, err)
	}

	return renderJSON(w, r, checkinResponse{VersionNumber: newVersion})
})

type reasonRequest struct {
	Reason string `json:"reason"`
}

var cancelCheckoutHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !requireVersioningEnabled(d) {
		return http.StatusNotFound, nil
	}
	path, err := requirePathQuery(r)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if !d.Check(path) {
		return http.StatusForbidden, nil
	}

	var body reasonRequest
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	}

	canonical, err := d.canonicalPath(path)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	lf, err := d.versioning.GetManaged(versioning.DefaultSourceID, canonical)
	if err != nil {
		return writeVersioningError(w, err)
	}

	if err := d.versioning.CancelCheckout(lf.FileID, d.user.ID, body.Reason); err != nil {
		return writeVersioningError(w, err)
	}
	return http.StatusNoContent, nil
})

var forceUnlockHandler = withAdmin(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !requireVersioningEnabled(d) {
		return http.StatusNotFound, nil
	}
	path, err := requirePathQuery(r)
	if err != nil {
		return http.StatusBadRequest, err
	}

	var body reasonRequest
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	}
	if body.Reason == "" {
		return http.StatusBadRequest, fmt.Errorf("a reason is required to force-unlock a file")
	}

	canonical, err := d.canonicalPath(path)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	lf, err := d.versioning.GetManaged(versioning.DefaultSourceID, canonical)
	if err != nil {
		return writeVersioningError(w, err)
	}

	if err := d.versioning.ForceUnlock(lf.FileID, d.user.ID, body.Reason); err != nil {
		return writeVersioningError(w, err)
	}
	return http.StatusNoContent, nil
})
