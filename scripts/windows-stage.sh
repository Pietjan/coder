#!/bin/bash
# From WSL: cross-compiles the Windows shell and stages it on the Windows
# side, for a native run against the host's Vulkan driver and NVMe. The
# model stays where it is on the Windows side; the shell's config names
# it. Adapted from colibri-go/scripts/windows-stage.sh.
#
# Usage: scripts/windows-stage.sh; CODER_WIN_DEST overrides the directory.
set -euo pipefail
cd "$(dirname "$0")/.."
# cmd.exe is not always on PATH in a non-interactive shell; the Windows
# user is whoever owns it, asked from a directory cmd.exe can chdir to.
cmd=$(command -v cmd.exe || echo /mnt/c/Windows/System32/cmd.exe)
user=$(cd /mnt/c && "$cmd" /c 'echo %USERNAME%' 2>/dev/null | tr -d '\r')
[ -n "$user" ] || { echo "cannot find the Windows user; set CODER_WIN_DEST" >&2; exit 1; }
dest=${CODER_WIN_DEST:-/mnt/c/Users/$user/AppData/Local/Temp/coder}
mkdir -p "$dest"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '-s -w -H=windowsgui' -o "$dest/coder-shell.exe" ./cmd/coder-shell
ls -la "$dest"
echo "run: $(wslpath -w "$dest")\\coder-shell.exe"
echo "config: %APPDATA%\\coder\\config.json (written with defaults on the first run); log: %LOCALAPPDATA%\\coder\\shell.log"
