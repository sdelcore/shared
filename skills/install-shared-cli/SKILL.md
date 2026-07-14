---
name: install-shared-cli
description: Install or update the `shared` CLI — the deploy tool for the self-hosted shared static-site platform (github.com/sdelcore/shared) — picking the right method for the current OS. Use when the user asks to install, update, or set up the shared CLI, or when a `shared` command fails because the binary is missing.
---

# Install the shared CLI

Installs `shared`, the CLI that talks to a shared server over HTTP
(`shared deploy`, `list`, `open`, `rm`, ...). The server (`sharedd`) runs
elsewhere on the network and is Linux-only — this skill never installs or
starts the server. Installing the CLI requires no privileges beyond writing
to a user-writable `PATH` directory.

## Steps

1. **Check if already installed**: if `shared --help` succeeds, skip to
   step 3 (or reinstall with the same method if the user asked for an
   update).

2. **Install — first matching method wins**:

   - **Nix** (`nix --version` works):

     ```sh
     nix profile install github:sdelcore/shared
     ```

     On NixOS with a flake-managed system config, offer to add it to the
     system flake instead if the user prefers declarative installs.

   - **Go 1.24+** (`go version` shows go1.24 or newer):

     ```sh
     CGO_ENABLED=0 go install github.com/sdelcore/shared/cmd/shared@latest
     ```

     Verify `$(go env GOPATH)/bin` is on `PATH`; add it to the shell profile
     if not.

   - **Release binary** (Linux and Windows only; macOS must use Go or Nix):
     find the latest version tag:

     ```sh
     curl -s https://api.github.com/repos/sdelcore/shared/releases/latest
     ```

     Download the asset for the OS/arch (note: no `v` prefix in filenames;
     `VERSION` is the tag minus `v`):

     | OS      | Asset                                  | Contains           |
     |---------|----------------------------------------|--------------------|
     | Linux   | `shared_VERSION_linux_{amd64,arm64}.tar.gz` | `sharedd` + `shared` (install only `shared`) |
     | Windows | `shared_VERSION_windows_{amd64,arm64}.zip`  | `shared.exe`       |

     Linux: extract and `install -m 0755 shared ~/.local/bin/` (or
     `/usr/local/bin` if the user prefers and has sudo). Windows: unzip
     `shared.exe` into a directory on `PATH`, e.g.
     `%LOCALAPPDATA%\Programs\shared\`, creating it and appending it to the
     user `PATH` if needed.

3. **Point it at the server**: `shared` defaults to
   `http://localhost:8787`, which is almost never right for a CLI-only
   machine. Ask the user for their server URL (e.g. `http://shared.tap` or
   `http://<host>:8787`) if it isn't already known, then persist it:

   - Unix shells: `export SHARED_SERVER=<url>` in the shell profile
   - Windows: `setx SHARED_SERVER <url>` (takes effect in new terminals)

4. **Verify end to end**: run `shared list` (in a fresh environment where
   `SHARED_SERVER` is set). It should print the deployed sites or an empty
   list; a connection error means the URL is wrong or the server is
   unreachable — troubleshoot with the user rather than reinstalling.
