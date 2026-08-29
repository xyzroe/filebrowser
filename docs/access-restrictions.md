# File Locking, Version History, and Access Restrictions

This fork adds three related, independently toggleable features on top of
upstream File Browser: checkout/check-in file locking, ordinary file version
history, and admin-configurable restrictions on specific user actions. All of
them are configured through CLI flags / config file keys (there is no web UI
for these settings), and can also be set via
[`filebrowser config set`](cli/filebrowser-config-set.md).

## Locking and Version History

Disabled by default. Enabling `versioning.enabled` turns every file a user
downloads (or takes for work) into a *managed* file: its full history is kept,
and it is protected by a checkout lock so only one user can hold it for
editing at a time.

```sh
filebrowser config set \
  --locking.enabled \
  --versioning.enabled \
  --versioning.storagePath /var/lib/filebrowser/versions
```

| Flag | Default | Description |
| --- | --- | --- |
| `locking.enabled` | `false` | Enable checkout/check-in file locking. |
| `locking.allowOwnerCancelCheckout` | `true` | Allow the lock owner to cancel a checkout without checking in a new version. |
| `locking.staleAfterDays` | `30` | Days of owner inactivity after which a lock is flagged stale (informational only; never auto-unlocked). |
| `locking.showOwnerToUsers` | `true` | Show the lock owner's username to other users who can browse the file. |
| `locking.requireCheckoutComment` | `false` | Require a comment when checking out a file. |
| `locking.blockAdminDownloadWhileLocked` | `true` | Require an administrator to force-unlock before downloading a locked file. |
| `versioning.enabled` | `false` | Enable ordinary file version history. |
| `versioning.storagePath` | *(none)* | Directory for immutable version objects, outside any browsable scope. Required if `versioning.enabled`. |
| `versioning.maxVersionsPerFile` | `0` (unlimited) | Maximum retained versions per file. |
| `versioning.maxAgeDays` | `0` (unlimited) | Maximum age in days of a retained version. |
| `versioning.deletedFileRetentionDays` | `30` | Days a deleted file's versions are retained before purge. |
| `versioning.requireCheckinComment` | `false` | Require a comment when checking in a new version. |
| `sharing.enabled` | `false` | Allow creating public share links. Disabled by default: a publicly shared managed/versioned file can never satisfy the checkout policy. |

An administrator never bypasses the content lock by downloading; they must
explicitly force-unlock a file first, so the audit trail stays unambiguous.

## Restrictions

Restrictions disable specific actions in the web UI (and the equivalent API
endpoints) for every **non-admin** user; administrators always retain full
access regardless of these settings.

```sh
filebrowser config set \
  --restrictions.disableCopy \
  --restrictions.disableDirectoryDownload \
  --restrictions.disableMultipleSelection \
  --restrictions.disableNewFile \
  --restrictions.disableEditor \
  --restrictions.disableHelp
```

| Flag | Default | Description |
| --- | --- | --- |
| `restrictions.disableCopy` | `false` | Disable copying files and directories. |
| `restrictions.disableDirectoryDownload` | `false` | Disable downloading a whole directory (or multiple selected items) as an archive. |
| `restrictions.disableMultipleSelection` | `false` | Disable selecting multiple files/directories at once (toolbar toggle, ctrl/shift-click, and dragging multiple items). |
| `restrictions.disableNewFile` | `false` | Disable creating new empty files from the browser UI. Uploading files is unaffected. |
| `restrictions.disableEditor` | `false` | Disable editing text files with the built-in editor. Text files fall back to a read-only preview. |
| `restrictions.disableHelp` | `false` | Hide the "Help" button/panel and disable the `F1` shortcut. |
