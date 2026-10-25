#!/bin/sh
set -eu
umask 077
server=
update=0
next_server=0
for arg in "$@"; do
  if [ "$next_server" = 1 ]; then server=$arg; next_server=0; continue; fi
  case "$arg" in --server) next_server=1 ;; --update) update=1 ;; esac
done
if [ -z "$server" ]; then echo "Missing Astral Server URL." >&2; exit 2; fi
case "$server" in http://*|https://*) ;; *) echo "Astral Server URL must use HTTP or HTTPS." >&2; exit 2 ;; esac
case "$server" in *'@'*|*'?'*|*'#'*) echo "Astral Server URL must not contain credentials, a query or a fragment." >&2; exit 2 ;; esac
server=${server%/}
# Fail before installing files or building an agent environment if this machine
# cannot reach the exact address selected in Astral.
if ! curl --fail --silent --show-error --connect-timeout 10 --max-time 20 --proto '=http,https' "$server/health" -o /dev/null; then
  echo "Cannot reach Astral at $server. Check the Server address, firewall, and Tailscale/LAN connection, then copy the command again." >&2
  exit 1
fi
case "$(uname -s)" in Darwin) target_os=darwin ;; Linux) target_os=linux ;; *) echo "Use install.ps1 on Windows." >&2; exit 2 ;; esac
case "$(uname -m)" in arm64|aarch64) target_arch=arm64 ;; x86_64|amd64) target_arch=amd64 ;; *) echo "Unsupported CPU architecture." >&2; exit 2 ;; esac
install_base="${XDG_DATA_HOME:-$HOME/.local/share}/astral/bin"
mkdir -p "$install_base"
chmod 700 "$install_base"
temporary="$(mktemp -d "$install_base/install.XXXXXX")"
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
binary_name="astral-$target_os-$target_arch"
curl --fail --silent --show-error "$server/downloads/$binary_name" -o "$temporary/astral"
curl --fail --silent --show-error "$server/downloads/checksums.txt" -o "$temporary/checksums.txt"
expected="$(awk -v file="$binary_name" '$2 == file { print $1 }' "$temporary/checksums.txt")"
if command -v sha256sum >/dev/null 2>&1; then actual="$(sha256sum "$temporary/astral" | awk '{print $1}')"; else actual="$(shasum -a 256 "$temporary/astral" | awk '{print $1}')"; fi
if [ "${#expected}" != 64 ] || [ "$actual" != "$expected" ]; then echo "Astral binary checksum did not match; the installed version is unchanged." >&2; exit 1; fi
release="$install_base/$actual"
mkdir -p "$release"
chmod 700 "$temporary/astral"
# Versioned executables let existing services keep running until the new binary
# and service definition are ready. Agent homes never contain program files.
if [ ! -f "$release/astral" ]; then mv "$temporary/astral" "$release/astral"; fi
if command -v sha256sum >/dev/null 2>&1; then installed="$(sha256sum "$release/astral" | awk '{print $1}')"; else installed="$(shasum -a 256 "$release/astral" | awk '{print $1}')"; fi
if [ "$installed" != "$actual" ]; then echo "An installed release is damaged. Remove that version directory and rerun this command." >&2; exit 1; fi
if [ "$update" = 1 ]; then
  "$release/astral" computer update "$@"
else
  "$release/astral" computer connect "$@"
fi
