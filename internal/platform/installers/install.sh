#!/bin/sh
set -eu
umask 077
server=
server_pin=
update=0
github_proxy=${ASTRAL_GITHUB_PROXY:-}
install_base="${XDG_DATA_HOME:-$HOME/.local/share}/astral/bin"
command_path=
command_needs_sudo=0
command_reused=0
command_created=0
previous_current=
current_switched=0
command_committed=0
# BEGIN Computer command entry
computer_install_error() { printf '%s\n' "$*" >&2; exit 1; }
command_directory_writable() { [ -d "$1" ] && [ -w "$1" ] && [ -x "$1" ]; }
authorize_command_registration() {
  if ! sudo -n true 2>/dev/null; then
    if ! { exec 8<>/dev/tty; } 2>/dev/null; then computer_install_error "Registering $command_path requires sudo authentication in a terminal."; fi
    sudo -v <&8 || computer_install_error 'Administrator access for command registration was not granted.'
    exec 8>&-
  fi
}
managed_server_command() {
  target=$(readlink "$1") || return 1
  case "$target" in
    /*/current/bin/astral)
      server_root=${target%/current/bin/astral}
      [ -f "$server_root/installation.json" ] && [ -L "$server_root/current" ] || return 1
      case $(readlink "$server_root/current") in "$server_root"/releases/*) ;; *) return 1 ;; esac
      # Official Server installation already requires Python. Validate its
      # ownership marker before sharing that command with a Computer.
      command -v python3 >/dev/null 2>&1 || return 1
      python3 - "$server_root" <<'PYSERVERCOMMAND' >/dev/null 2>&1
import json,os,sys
root=sys.argv[1]
marker=os.path.join(root,'installation.json')
with open(marker,encoding='utf-8') as f: value=json.load(f)
if value.get('schema')!=1 or value.get('root')!=root or value.get('data_directory')!=root+'/data/server' or value.get('uid')!=os.getuid() or value.get('service')!='astral-server.service' or os.stat(marker).st_uid!=os.getuid() or os.stat(root).st_uid!=os.getuid(): raise SystemExit(1)
PYSERVERCOMMAND
      return $?
      ;;
  esac
  return 1
}
select_command_entry() {
  case "$install_base" in /*) ;; *) computer_install_error 'The Computer binary installation directory must be absolute.' ;; esac
  if [ -e "$install_base/current" ] && [ ! -L "$install_base/current" ]; then computer_install_error 'The Computer current executable must be an installer-managed symlink.'; fi
  existing=$(command -v astral || true)
  if [ -n "$existing" ]; then
    if [ -L "$existing" ] && [ "$(readlink "$existing")" = "$install_base/current/astral" ]; then command_path=$existing; return; fi
    if [ -L "$existing" ] && managed_server_command "$existing"; then command_path=$existing; command_reused=1; return; fi
    computer_install_error "An unrelated astral command already exists: $existing. It was not replaced."
  fi
  remaining_path=${PATH:-}:
  while [ -n "$remaining_path" ]; do
    directory=${remaining_path%%:*}
    remaining_path=${remaining_path#*:}
    case "$directory" in /*) ;; *) continue ;; esac
    if command_directory_writable "$directory"; then command_path=${directory%/}/astral; break; fi
  done
  if [ -z "$command_path" ]; then
    for candidate in /usr/local/bin /usr/bin; do
      remaining_path=${PATH:-}:
      while [ -n "$remaining_path" ]; do
        directory=${remaining_path%%:*}
        remaining_path=${remaining_path#*:}
        if [ "${directory%/}" = "$candidate" ] && [ -d "$candidate" ]; then command_path=$candidate/astral; command_needs_sudo=1; break 2; fi
      done
    done
  fi
  [ -n "$command_path" ] || computer_install_error 'No supported command directory exists on the current PATH; ask the administrator to provide command installation access, then retry.'
  if [ -e "$command_path" ] || [ -L "$command_path" ]; then
    [ -L "$command_path" ] && [ "$(readlink "$command_path")" = "$install_base/current/astral" ] || computer_install_error "Refusing to replace an unrelated command: $command_path"
  fi
  if [ "$command_needs_sudo" = 1 ]; then
    command -v sudo >/dev/null 2>&1 || computer_install_error "Registering $command_path requires administrator access, but sudo is unavailable."
    printf 'Registering astral at %s requires sudo; the Computer stays in the current user.\n' "$command_path"
    authorize_command_registration
  fi
}
install_command_entry() {
  if [ "$command_reused" = 1 ]; then
    astral version >/dev/null || computer_install_error 'The existing managed astral command could not run.'
    return
  fi
  if [ -e "$install_base/current" ] || [ -L "$install_base/current" ]; then
    [ -L "$install_base/current" ] || computer_install_error 'The Computer current executable must be an installer-managed symlink.'
    previous_current=$(readlink "$install_base/current")
  fi
  ln -sfn "$release" "$install_base/current"
  current_switched=1
  if [ ! -e "$command_path" ] && [ ! -L "$command_path" ]; then
    if [ "$command_needs_sudo" = 1 ]; then authorize_command_registration; sudo -n ln -s -- "$install_base/current/astral" "$command_path"
    else ln -s "$install_base/current/astral" "$command_path"; fi
    command_created=1
  fi
  expected_version=$("$release/astral" version) || computer_install_error 'The downloaded astral command could not report its version.'
  actual_version=$(astral version) || computer_install_error 'The installed astral command could not run from the current PATH.'
  if [ "$(command -v astral || true)" != "$command_path" ] || [ -z "$expected_version" ] || [ "$actual_version" != "$expected_version" ]; then
    computer_install_error 'The installed astral command could not run from the current PATH.'
  fi
}
# END Computer command entry
# Consume installer-only options without changing the order or quoting of the
# arguments forwarded to the Computer command.
remaining=$#
while [ "$remaining" -gt 0 ]; do
  argument=$1
  shift
  remaining=$((remaining - 1))
  case "$argument" in
    --github-proxy)
      if [ "$remaining" -eq 0 ]; then echo "--github-proxy requires an HTTPS prefix (or an empty value for direct downloads)." >&2; exit 2; fi
      github_proxy=$1
      shift
      remaining=$((remaining - 1))
      ;;
    --github-proxy=*) github_proxy=${argument#*=} ;;
    *) set -- "$@" "$argument" ;;
  esac
done
if [ -n "$github_proxy" ]; then
  if ! printf '%s\n' "$github_proxy" | LC_ALL=C awk '
    function port_valid(value) {
      sub(/^https:\/\//, "", value)
      sub(/\/.*/, "", value)
      if (value ~ /^\[/) sub(/^\[[^]]+\]/, "", value)
      else sub(/^[^:]+/, "", value)
      if (value == "") return 1
      sub(/^:/, "", value)
      return value + 0 > 0 && value + 0 <= 65535
    }
    NR != 1 { exit 1 }
    /^https:\/\/[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]+)?(\/[A-Za-z0-9._~%\/-]*)?$/ { if (!port_valid($0)) exit 1; valid=1; next }
    /^https:\/\/\[[0-9A-Fa-f:]+\](:[0-9]+)?(\/[A-Za-z0-9._~%\/-]*)?$/ { if (!port_valid($0)) exit 1; valid=1; next }
    { exit 1 }
    END { if (!valid) exit 1 }
  '; then
    echo "GitHub proxy must be an HTTPS prefix without credentials, a query, a fragment or whitespace." >&2
    exit 2
  fi
  while [ "${github_proxy%/}" != "$github_proxy" ]; do github_proxy=${github_proxy%/}; done
  github_proxy=$github_proxy/
  printf 'GitHub downloads use the selected proxy: %s\n' "$github_proxy"
fi
next_server=0
next_pin=0
for arg in "$@"; do
  if [ "$next_server" = 1 ]; then server=$arg; next_server=0; continue; fi
  if [ "$next_pin" = 1 ]; then server_pin=$arg; next_pin=0; continue; fi
  case "$arg" in --server) next_server=1 ;; --server-pin) next_pin=1 ;; --update) update=1 ;; esac
done
select_command_entry
if [ "$update" != 1 ]; then
  if [ -z "$server" ]; then echo "Missing Astral Server URL." >&2; exit 2; fi
  case "$server" in http://*|https://*) ;; *) echo "Astral Server URL must use HTTP or HTTPS." >&2; exit 2 ;; esac
  case "$server" in *'@'*|*'?'*|*'#'*) echo "Astral Server URL must not contain credentials, a query or a fragment." >&2; exit 2 ;; esac
  case "$server_pin" in *[!a-fA-F0-9]*|'') echo "A valid Server public-key pin is required; copy a new pairing command." >&2; exit 2 ;; esac
  if [ "${#server_pin}" != 64 ]; then echo "Invalid Server public-key pin." >&2; exit 2; fi
  server=${server%/}
  # Enrollment checks its selected address before installing files or building
  # an Agent environment. Updates use the existing credentials instead.
  if ! curl --fail --silent --show-error --connect-timeout 10 --max-time 20 --proto '=http,https' "$server/health" -o /dev/null; then
    echo "Cannot reach Astral at $server. Check the Server address, firewall, and Tailscale/LAN connection, then copy the command again." >&2
    exit 1
  fi
elif [ -n "$server" ] || [ -n "$server_pin" ]; then
  echo "Updates use the existing Computer credentials; --server and --server-pin are not accepted." >&2
  exit 2
fi
case "$(uname -s)" in Darwin) target_os=darwin ;; Linux) target_os=linux ;; *) echo "Use install.ps1 on Windows." >&2; exit 2 ;; esac
case "$(uname -m)" in arm64|aarch64) target_arch=arm64 ;; x86_64|amd64) target_arch=amd64 ;; *) echo "Unsupported CPU architecture." >&2; exit 2 ;; esac
mkdir -p "$install_base"
chmod 700 "$install_base"
temporary="$(mktemp -d "$install_base/install.XXXXXX")"
cleanup() {
  status=$?
  trap - EXIT HUP INT TERM
  set +e
  if [ "$status" != 0 ] && [ "$current_switched" = 1 ] && [ "$command_committed" = 0 ]; then
    if [ -n "$previous_current" ]; then ln -sfn "$previous_current" "$install_base/current"; else rm -f "$install_base/current"; fi
    if [ "$command_created" = 1 ] && [ -L "$command_path" ] && [ "$(readlink "$command_path")" = "$install_base/current/astral" ]; then
      if [ "$command_needs_sudo" = 1 ]; then sudo -n rm -- "$command_path"; else rm -- "$command_path"; fi
    fi
  fi
  rm -rf "$temporary"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
binary_name="astral-$target_os-$target_arch"
# The Server may use HTTP, so executable trust must come from the official
# HTTPS release, never from that Server or its unauthenticated checksum file.
release_base=https://github.com/oines/astral/releases/latest/download
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' "$github_proxy$release_base/$binary_name" -o "$temporary/astral"
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' "$github_proxy$release_base/checksums.txt" -o "$temporary/checksums.txt"
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
# The Computer may download another official architecture for a container.
# Pass the exact selection through, including an explicit direct-download choice.
ASTRAL_GITHUB_PROXY=$github_proxy
export ASTRAL_GITHUB_PROXY
install_command_entry
if [ "$update" = 1 ]; then
  "$release/astral" computer update "$@"
else
  "$release/astral" computer connect "$@"
fi
command_committed=1
