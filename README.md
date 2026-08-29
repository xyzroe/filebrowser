> [!WARNING]
> 
> **Upstream File Browser is archived on 2026-09-01** and receives no further releases or fixes. **This fork ([xyzroe/filebrowser](https://github.com/xyzroe/filebrowser)) continues development past that date** with additional features and a fix for one of upstream's two unaddressed security issues (see [Security](#security) below).

<p align="center">
  <img src="./branding/banner.png" width="550"/>
</p>

File Browser provides a file managing interface within a specified directory and it can be used to upload, delete, preview and edit your files. It is a **create-your-own-cloud**-kind of software where you can just install it on your server, direct it to a path and access your files through a nice web interface.

**Background:** [Goodbye File Browser, for Real This Time](https://hacdias.com/2026/07/28/filebrowser/), July 2026.

## What's different in this fork

- **File checkout/check-in locking and version history.** Files can be locked ("taken for work") before download, preventing concurrent edits, with a full version history and a UI to browse/download old versions. Off by default; see [`docs/access-restrictions.md`](docs/access-restrictions.md) for every `locking.*`/`versioning.*` flag.
- **Admin-configurable restrictions**, applied to non-admin users only (admins always retain full access): disable copying, whole-directory/archive downloads, multi-selection, creating new files from the UI, the built-in text editor, and the Help button. See [`docs/access-restrictions.md`](docs/access-restrictions.md).
- **JWT/session security fix.** Logout, password changes, and permission changes now immediately invalidate previously issued tokens, and the default session lifetime is 30 minutes instead of 2 hours (see [Security](#security)).
- **Simplified Profile Settings**: only the language selector is user-configurable; hide-dotfiles and single-click-to-open are always on for every user.
- **Docker images published to `ghcr.io/xyzroe/filebrowser`** (GitHub Container Registry) instead of the upstream Docker Hub org, built automatically on every tagged release via GitHub Actions.

## Security

Published advisories are listed under [security advisories](https://github.com/filebrowser/filebrowser/security/advisories),
and reporting instructions are in [SECURITY.md](SECURITY.md). One of the two known issue
classes below has been fixed in this fork; the other remains unaddressed:

- **Command execution, runner, and hooks.** This feature is plagued with vulnerabilities across many published advisories, and would need a full rewrite to be made safe. It is disabled by default; if you re-enable it with `--disable-exec=false`, treat the ability to run commands as equivalent to shell access on the host. Background: [#5199](https://github.com/filebrowser/filebrowser/issues/5199).
- ~~**Session and JWT handling.**~~ **Fixed in this fork.** Logout, password changes, and permission changes now revoke every previously issued token immediately (see `withUser`/`Storage.InvalidateTokens` in `http/auth.go` and `users/storage.go`), and the default session lifetime was shortened from 2h to 30m. Background: [#5216](https://github.com/filebrowser/filebrowser/issues/5216).

If you keep running File Browser, treat it as unmaintained software:

- **Do not expose it directly to the internet.** Put it behind a reverse proxy that terminates TLS and performs its own authentication.
- **Keep the command runner disabled.** It is off by default, so leave it off. See [#5199](https://github.com/filebrowser/filebrowser/issues/5199) and [`docs/command-execution.md`](docs/command-execution.md).
- **Run it unprivileged, inside a container**, with only the directory you intend to serve mounted into it.

## Documentation

Documentation on how to install, configure, and build this project lives in [`docs`](docs) in this repository, including this fork's additions: [`docs/access-restrictions.md`](docs/access-restrictions.md) (locking, versioning, and restriction flags).

[CONTRIBUTING.md](CONTRIBUTING.md) documents how to build and develop the project, which remains useful to anyone forking it.

## Docker

Images for this fork are published to [`ghcr.io/xyzroe/filebrowser`](https://github.com/xyzroe/filebrowser/pkgs/container/filebrowser) on every tagged release:

```sh
docker pull ghcr.io/xyzroe/filebrowser:latest
```

See [`docs/installation.md`](docs/installation.md) for volume layout and the S6-overlay image, and [`docs/access-restrictions.md`](docs/access-restrictions.md) for this fork's additional configuration flags.

## License

[Apache License 2.0](LICENSE) © File Browser Contributors
