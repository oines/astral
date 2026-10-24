#!/usr/bin/env bash
# Install the Astral Server and embedded Web UI, not a Computer enrollment Daemon.
set -euo pipefail
umask 077

installer_version=1
repository=oines/astral
service=astral-server.service
tag=latest
root=${XDG_DATA_HOME:-${HOME:?HOME must be set}/.local/share}/astral-server
listen=
public_url=
tls_cert=
tls_key=
listen_set=0
public_set=0
cert_set=0
key_set=0
download_only=
dry_run=0
stage=
unit_temp=
link_temp=
mutating=0
complete=0
had_unit=0
had_config=0
was_active=0
was_enabled=0
old_current=

die() { printf 'Astral Server: %s\n' "$*" >&2; exit 1; }
usage() {
  cat <<'HELP'
Astral Server installer (Linux amd64/arm64, current user only)

Usage: bash install-server.sh [OPTIONS]
  --tag TAG             Published vMAJOR.MINOR.PATCH tag, or latest (default)
  --root DIRECTORY      Dedicated installation/data root (absolute path)
  --listen HOST:PORT    Server listen address (first install: 127.0.0.1:4545)
  --public-url URL      HTTPS public URL, normally provided by a reverse proxy
  --tls-cert FILE       Optional absolute PEM certificate path (with --tls-key)
  --tls-key FILE        Optional absolute PEM private key path (with --tls-cert)
  --download-only DIR   Download and verify the full distribution; execute nothing
  --dry-run            Print the plan; no network, writes or service operations
  --version            Print the installer format version
  --help               Show this help

Updates preserve omitted listen/public-url/TLS settings. Installation requires
an existing systemd user manager and never uses sudo or enables user lingering.
Tags contain three integers from 0 to 9007199254740991, without leading zeroes,
prerelease identifiers or build metadata.
Unsigned candidates and releases not marked public_release_ready are rejected.
HELP
}
need_value() { [[ $# -ge 2 && -n $2 ]] || die "$1 requires a value"; }
while [[ $# -gt 0 ]]; do
  case $1 in
    --help|-h) usage; exit 0 ;;
    --version) printf 'Astral Server installer %s\n' "$installer_version"; exit 0 ;;
    --tag) need_value "$@"; tag=$2; shift 2 ;;
    --root) need_value "$@"; root=$2; shift 2 ;;
    --listen) need_value "$@"; listen=$2; listen_set=1; shift 2 ;;
    --public-url) [[ $# -ge 2 ]] || die '--public-url requires a value'; public_url=$2; public_set=1; shift 2 ;;
    --tls-cert) [[ $# -ge 2 ]] || die '--tls-cert requires a value'; tls_cert=$2; cert_set=1; shift 2 ;;
    --tls-key) [[ $# -ge 2 ]] || die '--tls-key requires a value'; tls_key=$2; key_set=1; shift 2 ;;
    --download-only) need_value "$@"; download_only=$2; shift 2 ;;
    --dry-run) dry_run=1; shift ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
done
release_tag_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
valid_release_tag() {
  local part LC_ALL=C
  [[ $1 =~ $release_tag_pattern ]] || return 1
  for part in "${BASH_REMATCH[@]:1}"; do
    # Compare lengths and decimal strings so oversized input cannot overflow
    # Bash arithmetic. Match JavaScript's Number.MAX_SAFE_INTEGER exactly.
    [[ ${#part} -lt 16 || ( ${#part} -eq 16 && ( $part < 9007199254740991 || $part == 9007199254740991 ) ) ]] || return 1
  done
}
[[ $tag == latest ]] || valid_release_tag "$tag" || die 'release tag must be vMAJOR.MINOR.PATCH without leading zeroes or prerelease/build suffixes; each component must be 0-9007199254740991'
for command in python3 uname; do command -v "$command" >/dev/null 2>&1 || die "required command is missing: $command"; done
root=$(python3 -c 'import os,sys; p=sys.argv[1]; assert os.path.isabs(p), "--root must be absolute"; print(os.path.realpath(p))' "$root")
[[ $root != / && $root != "$HOME" ]] || die '--root must be a dedicated Astral directory'
config=$root/install-config.json
unit_dir=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user
unit=$unit_dir/$service
ready=$root/run/ready.json
config_value() {
  python3 - "$config" "$1" "$2" <<'PY'
import json,sys
with open(sys.argv[1], encoding='utf-8') as f: value=json.load(f).get(sys.argv[2], sys.argv[3])
if not isinstance(value,str) or any(ord(c)<32 or ord(c)==127 for c in value): raise SystemExit('Invalid saved installer configuration')
print(value)
PY
}
if [[ -f $config ]]; then
  [[ $listen_set == 1 ]] || listen=$(config_value listen 127.0.0.1:4545)
  [[ $public_set == 1 ]] || public_url=$(config_value public_url '')
  [[ $cert_set == 1 ]] || tls_cert=$(config_value tls_cert '')
  [[ $key_set == 1 ]] || tls_key=$(config_value tls_key '')
fi
listen=${listen:-127.0.0.1:4545}
# Fail before any databases are opened. The Go Server applies its own URL/TLS
# validation again on startup; this preflight does not replace that contract.
python3 - "$root" "$unit_dir" "$listen" "$public_url" "$tls_cert" "$tls_key" "$download_only" <<'PY'
import ipaddress,os,sys,urllib.parse
root,units,listen,public,cert,key,download=sys.argv[1:]
for value in sys.argv[1:]:
    if any(ord(c)<32 or ord(c)==127 for c in value): raise SystemExit('Control characters are not allowed in installer arguments')
if not os.path.isabs(units): raise SystemExit('XDG_CONFIG_HOME must be absolute')
try:
    address=urllib.parse.urlsplit('//'+listen)
    host=address.hostname
    port=address.port
    if not host or port is None or not 1<=port<=65535 or address.path or address.username or address.password: raise ValueError()
except ValueError: raise SystemExit('--listen must be HOST:PORT with a fixed port between 1 and 65535')
try: loopback=ipaddress.ip_address(host).is_loopback
except ValueError: loopback=host.lower()=='localhost'
if public:
    url=urllib.parse.urlsplit(public)
    if url.scheme!='https' or not url.hostname or url.username or url.password or url.fragment or url.query or url.path not in ('','/'):
        raise SystemExit('--public-url must be an HTTPS origin without credentials, query or fragment')
if not loopback and not public: raise SystemExit('A non-loopback listener requires an HTTPS --public-url; prefer a loopback listener behind an HTTPS proxy')
if bool(cert)!=bool(key): raise SystemExit('--tls-cert and --tls-key must be supplied together')
for path in (cert,key):
    if path and (not os.path.isabs(path) or not os.path.isfile(path)): raise SystemExit('TLS paths must name existing absolute files')
if download and (not os.path.isabs(download) or os.path.lexists(download) or not os.path.isdir(os.path.dirname(download))):
    raise SystemExit('--download-only requires a new absolute directory whose parent exists')
PY
case $(uname -m) in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) die 'unsupported CPU architecture (requires amd64 or arm64)' ;; esac
if [[ $dry_run == 1 ]]; then
  printf 'No changes made. Plan: Linux %s; GitHub %s release %s\n' "$arch" "$repository" "$tag"
  printf 'Installation root: %s\nListen: %s\n' "$root" "$listen"
  if [[ -n $download_only ]]; then printf 'Download and verify only: %s\n' "$download_only"; else printf 'User service: %s\nData: %s/data\n' "$unit" "$root"; fi
  printf 'Requires a published ready release; installation also requires Linux and a running systemd user manager.\n'
  exit 0
fi
[[ $(uname -s) == Linux ]] || die 'this installer supports Linux only'
for command in curl sha256sum awk mktemp mkdir mv ln rm chmod cp; do command -v "$command" >/dev/null 2>&1 || die "required command is missing: $command"; done
if [[ -z $download_only ]]; then
  for command in systemctl flock grep readlink find; do command -v "$command" >/dev/null 2>&1 || die "required command is missing: $command"; done
  systemctl --user show-environment >/dev/null 2>&1 || die 'no systemd user manager is available; use a normal Linux login session with systemd --user (including for root); no system service will be installed'
  mkdir -p "$unit_dir"
  exec 9>"$unit_dir/.astral-server-install.lock"
  flock -n 9 || die 'another Astral Server installer is running for this user'
  if [[ -e $unit || -L $unit ]]; then
    [[ -f $unit && ! -L $unit ]] || die "refusing to replace an unmanaged service definition: $unit"
    grep -Fqx "# Astral Server installer root: $root" "$unit" || die 'an Astral Server service exists for a different root or was not created by this installer'
  fi
  [[ ! -e $root/current || -L $root/current ]] || die 'current must be an installer-managed release symlink'
  mkdir -p "$root/releases"
  stage=$(mktemp -d "$root/.install.XXXXXX")
else
  stage=$(mktemp -d "${download_only%/*}/.astral-download.XXXXXX")
fi

cleanup() {
  status=$?
  trap - EXIT INT TERM HUP
  set +e
  recovery_failed=0
  if [[ $status != 0 && $mutating == 1 && $complete == 0 ]]; then
    printf 'Installation failed; restoring the previous program and user service.\n' >&2
    systemctl --user stop "$service" >/dev/null 2>&1 || recovery_failed=1
    if [[ -n $old_current ]]; then
      ln -s "$old_current" "$root/.current.restore.$$" && mv -Tf "$root/.current.restore.$$" "$root/current" || recovery_failed=1
    else rm -f "$root/current" || recovery_failed=1; fi
    if [[ $had_unit == 1 ]]; then cp "$stage/previous.service" "$unit" || recovery_failed=1; else rm -f "$unit" || recovery_failed=1; fi
    if [[ $had_config == 1 ]]; then cp "$stage/previous-config.json" "$config" || recovery_failed=1; else rm -f "$config" || recovery_failed=1; fi
    systemctl --user daemon-reload || recovery_failed=1
    if [[ $was_enabled == 1 ]]; then systemctl --user enable "$service" >/dev/null 2>&1 || recovery_failed=1
    else systemctl --user disable "$service" >/dev/null 2>&1 || true; fi
    if [[ $was_active == 1 ]]; then systemctl --user start "$service" && systemctl --user is-active --quiet "$service" || recovery_failed=1; fi
    printf 'Existing data, master.key and Computer files were not deleted or rolled back.\n' >&2
    if [[ $recovery_failed == 1 ]]; then printf 'Automatic service recovery was incomplete. Backups remain in %s; inspect systemctl --user status %s.\n' "$stage" "$service" >&2; fi
  fi
  [[ -z $unit_temp ]] || rm -f "$unit_temp"
  [[ -z $link_temp ]] || rm -f "$link_temp"
  if [[ $recovery_failed == 0 && -n $stage ]]; then rm -rf "$stage"; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
fetch() {
  curl --fail --location --silent --show-error --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 1200 --retry 2 "$1" -o "$2"
}
if [[ $tag == latest ]]; then api=https://api.github.com/repos/$repository/releases/latest; else api=https://api.github.com/repos/$repository/releases/tags/$tag; fi
fetch "$api" "$stage/release.json" || die "no accessible published release for $tag; the repository may not have a ready release yet"
requested_tag=$tag
tag=$(python3 - "$stage/release.json" "$requested_tag" <<'PY'
import json,re,sys
with open(sys.argv[1],encoding='utf-8') as f: release=json.load(f)
tag=release.get('tag_name','')
match=re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)',tag) if isinstance(tag,str) else None
if release.get('draft') is not False or not match or any(len(part)>16 or int(part)>9007199254740991 for part in match.groups()): raise SystemExit('Release is unpublished or its tag is not vMAJOR.MINOR.PATCH without leading zeroes or prerelease/build suffixes, with each component 0-9007199254740991')
if sys.argv[2]!='latest' and tag!=sys.argv[2]: raise SystemExit('Release tag mismatch')
if sys.argv[2]=='latest' and release.get('prerelease') is not False: raise SystemExit('latest must resolve to a stable published release')
print(tag)
PY
)
distribution=$stage/release/distribution
mkdir -p "$distribution"
base=https://github.com/$repository/releases/download/$tag
fetch "$base/SHA256SUMS" "$distribution/SHA256SUMS"
verify() {
  local file=$1 sums=$2 expected actual
  expected=$(awk -v file="$file" '$2 == file {print $1}' "$sums")
  [[ $expected =~ ^[0-9a-f]{64}$ ]] || die "missing, duplicate or malformed checksum: $file"
  actual=$(sha256sum "$distribution/$file" | awk '{print $1}')
  [[ $actual == "$expected" ]] || die "checksum mismatch: $file"
}
fetch "$base/release-manifest.json" "$distribution/release-manifest.json"
verify release-manifest.json "$distribution/SHA256SUMS"
metadata=$(python3 - "$distribution/release-manifest.json" "$tag" <<'PY'
import json,re,sys
with open(sys.argv[1],encoding='utf-8') as f: m=json.load(f)
names=['astral-'+os+'-'+arch+('.exe' if os=='windows' else '') for os in ('darwin','linux','windows') for arch in ('amd64','arm64')]
required=names+['checksums.txt','LICENSE.txt','THIRD_PARTY_NOTICES.txt']
if m.get('schema_version')!=1 or m.get('public_release_ready') is not True or m.get('purpose')=='unsigned-candidate':
    raise SystemExit('This release is a candidate or is not marked public_release_ready; refusing installation')
if m.get('tag')!=sys.argv[2] or not isinstance(m.get('version'),str) or 'v'+m['version']!=m['tag']: raise SystemExit('Manifest version/tag mismatch')
match=re.fullmatch(r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)',m['version'])
if not match or any(len(part)>16 or int(part)>9007199254740991 for part in match.groups()): raise SystemExit('Manifest version must be MAJOR.MINOR.PATCH without leading zeroes or prerelease/build suffixes, with each component 0-9007199254740991')
if not re.fullmatch(r'[0-9a-f]{40}',m.get('source_sha','')): raise SystemExit('Invalid manifest source revision')
host,core=m.get('host_schema'),m.get('core_format')
if type(host) is not int or type(core) is not int or host<1 or core<1 or m.get('data_formats')!={'host':host,'core':core}: raise SystemExit('Invalid or inconsistent data-format metadata')
if sorted(m.get('distribution_names',[]))!=sorted(names): raise SystemExit('Manifest must describe all six distribution binaries')
assets=m.get('assets',[])
if not isinstance(assets,list) or any(not isinstance(a,dict) for a in assets): raise SystemExit('Invalid asset inventory')
inventory={a.get('name'):a for a in assets}
if len(inventory)!=len(assets) or any(name not in inventory for name in required): raise SystemExit('Missing or duplicate distribution assets')
for name in required:
    a=inventory[name]
    if not isinstance(a.get('sha256'),str) or not re.fullmatch(r'[0-9a-f]{64}',a['sha256']) or type(a.get('size')) is not int or a['size']<1: raise SystemExit('Invalid asset digest or size')
print(m['version'])
print(host,core)
PY
)
version=${metadata%%$'\n'*}
formats=${metadata#*$'\n'}
assets=(astral-darwin-amd64 astral-darwin-arm64 astral-linux-amd64 astral-linux-arm64 astral-windows-amd64.exe astral-windows-arm64.exe checksums.txt LICENSE.txt THIRD_PARTY_NOTICES.txt)
for name in "${assets[@]}"; do fetch "$base/$name" "$distribution/$name"; verify "$name" "$distribution/SHA256SUMS"; done
for name in "${assets[@]:0:6}"; do verify "$name" "$distribution/checksums.txt"; done
python3 - "$distribution" <<'PY'
import hashlib,json,pathlib,sys
root=pathlib.Path(sys.argv[1])
with (root/'release-manifest.json').open(encoding='utf-8') as f: manifest=json.load(f)
for asset in manifest['assets']:
    name=asset.get('name','')
    if name not in {p.name for p in root.iterdir()}: continue
    data=(root/name).read_bytes()
    if len(data)!=asset['size'] or hashlib.sha256(data).hexdigest()!=asset['sha256']: raise SystemExit('Asset does not match manifest: '+name)
PY
if [[ -n $download_only ]]; then
  mv "$distribution" "$download_only"
  printf 'Verified Astral %s distribution downloaded to %s. No program was executed and no service or data was changed.\n' "$version" "$download_only"
  exit 0
fi
chmod 700 "$distribution/astral-linux-$arch"
mkdir -p "$stage/release/bin"
ln -s "../distribution/astral-linux-$arch" "$stage/release/bin/astral"
new_program=$stage/release/bin/astral
[[ $("$new_program" version) == "$version" ]] || die 'downloaded executable version differs from the release manifest'
new_formats=$("$new_program" version --data-formats) || die 'downloaded executable does not expose data-format metadata'
[[ $new_formats == "$formats" && $new_formats =~ ^[0-9]+\ [0-9]+$ ]] || die 'downloaded executable data formats differ from the manifest'
old_program=
if [[ -L $root/current ]]; then
  old_current=$(readlink "$root/current")
  old_program=$root/current/bin/astral
elif [[ -L $root/data-program ]]; then old_program=$root/data-program/bin/astral; fi
if [[ -n $old_program ]]; then
  resolved=$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$old_program")
  [[ $resolved == "$root/releases/"* && -x $old_program ]] || die 'existing program is not an installer-managed executable'
  old_formats=$("$old_program" version --data-formats) || die 'existing program lacks data-format metadata; use a separate empty root or follow explicit migration instructions'
  [[ $old_formats =~ ^[0-9]+\ [0-9]+$ ]] || die 'existing program lacks machine-readable data formats; use a separate empty root or follow explicit migration instructions'
  [[ $old_formats == "$new_formats" ]] || die "data format change ($old_formats -> $new_formats): automatic migration is unavailable; the old service is unchanged"
elif [[ -d $root/data && -n $(find "$root/data" -mindepth 1 -print -quit) ]]; then
  die 'existing nonempty data has no trusted installed program/format metadata; use a separate empty root or migrate explicitly'
fi
# A full distribution cohort gets its own immutable directory, not just the
# current host binary. Retain old cohorts for service recovery and inspection.
cohort=$(sha256sum "$distribution/SHA256SUMS" | awk '{print $1}')
release=$root/releases/$tag-${cohort:0:16}
if [[ -e $release ]]; then
  [[ ! -L $release ]] || die 'an existing release directory must not be a symlink'
  for name in SHA256SUMS release-manifest.json "${assets[@]}"; do
    [[ -f $release/distribution/$name ]] || die 'an existing release directory is incomplete; inspect it before retrying'
    [[ $(sha256sum "$release/distribution/$name" | awk '{print $1}') == $(sha256sum "$distribution/$name" | awk '{print $1}') ]] || die 'an existing release directory is damaged; inspect it before retrying'
  done
  [[ -x $release/bin/astral ]] || die 'an existing release executable is damaged'
  [[ $(readlink "$release/bin/astral") == "../distribution/astral-linux-$arch" ]] || die 'an existing release executable does not select the verified host binary'
else mv "$stage/release" "$release"; fi
if [[ -f $unit ]]; then cp "$unit" "$stage/previous.service"; had_unit=1; fi
if [[ -f $config ]]; then cp "$config" "$stage/previous-config.json"; had_config=1; fi
if systemctl --user is-active --quiet "$service"; then was_active=1; fi
if systemctl --user is-enabled --quiet "$service"; then was_enabled=1; fi
python3 - "$stage/new-config.json" "$listen" "$public_url" "$tls_cert" "$tls_key" <<'PY'
import json,sys
with open(sys.argv[1],'w',encoding='utf-8') as f: json.dump(dict(zip(('listen','public_url','tls_cert','tls_key'),sys.argv[2:])),f,ensure_ascii=False)
PY
unit_temp=$(mktemp "$unit_dir/.astral-server.service.XXXXXX")
python3 - "$unit_temp" "$root" "$listen" "$public_url" "$tls_cert" "$tls_key" <<'PY'
import sys
target,root,listen,public,cert,key=sys.argv[1:]
# The ':' executable prefix disables environment substitution. In particular,
# executable paths do not undergo the same '$$' expansion as ordinary arguments.
# Keep dollars literal in both places; percent specifiers still need escaping.
def quote(value): return '"'+value.replace('\\','\\\\').replace('"','\\"').replace('%','%%')+'"'
args=[root+'/current/bin/astral','serve','--listen',listen,'--data',root+'/data/server','--computers-dir',root+'/data/computers','--distribution-dir',root+'/current/distribution','--ready-file',root+'/run/ready.json']
if public: args+=['--public-url',public]
if cert: args+=['--tls-cert',cert,'--tls-key',key]
with open(target,'w',encoding='utf-8') as f:
    f.write('# Astral Server installer root: '+root+'\n[Unit]\nDescription=Astral Server\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nWorkingDirectory=/\nExecStart=:'+ ' '.join(map(quote,args))+'\nRestart=on-failure\nRestartSec=3\nTimeoutStopSec=30\nUMask=0077\n\n[Install]\nWantedBy=default.target\n')
PY
mutating=1
if [[ $had_unit == 1 ]]; then systemctl --user stop "$service"; fi
mkdir -p "$root/data/server" "$root/data/computers" "$root/run"
rm -f "$ready"
link_temp=$root/.current.$$
ln -s "$release" "$link_temp"
mv -Tf "$link_temp" "$root/current"
link_temp=
# Retain a trusted format probe even if the very first startup fails after
# creating data. It does not activate a service and is never a database backup.
if [[ ! -e $root/data-program && ! -L $root/data-program ]]; then ln -s "$release" "$root/data-program"; fi
mv "$stage/new-config.json" "$config"
mv "$unit_temp" "$unit"
unit_temp=
systemctl --user daemon-reload
systemctl --user enable "$service"
systemctl --user start "$service"
server_url=
for ((attempt=0; attempt<45; attempt++)); do
  pid=$(systemctl --user show "$service" --property=MainPID --value)
  if [[ $pid =~ ^[1-9][0-9]*$ && -f $ready ]]; then
    if server_url=$(python3 - "$ready" "$pid" "$root/data/server/owner.token" <<'PY'
import json,sys,urllib.parse
with open(sys.argv[1],encoding='utf-8') as f: ready=json.load(f)
if ready.get('pid')!=int(sys.argv[2]) or ready.get('token_file')!=sys.argv[3]: raise SystemExit(1)
url=ready.get('url','')
if urllib.parse.urlsplit(url).scheme not in ('http','https'): raise SystemExit(1)
print(url)
PY
    ); then sleep 1; systemctl --user is-active --quiet "$service" && break; fi
  fi
  server_url=
  sleep 1
done
if [[ -z $server_url ]]; then
  # Capture only manager state before rollback removes the attempted unit.
  # Never print the journal, Owner token, ready-file contents or environment.
  systemctl --user show "$service" --property=ExecMainCode,ExecMainStatus,Result >&2 || true
  die "Server did not become ready; inspect journalctl --user -u $service"
fi
systemctl --user is-active --quiet "$service" || die 'Server exited during startup'
complete=1
printf '\nAstral Server %s is running: %s\n' "$version" "$server_url"
printf 'Service: systemctl --user status %s\nLogs: journalctl --user -u %s\n' "$service" "$service"
printf 'Data: %s/data\nOwner token file (contents are not printed): %s/data/server/owner.token\n' "$root" "$root"
if [[ $listen == 127.0.0.1:4545 && -z $tls_cert ]]; then
  printf 'From your computer: ssh -N -L 4545:127.0.0.1:4545 USER@VPS\nThen open http://127.0.0.1:4545 in a browser.\n'
else
  printf 'For this custom listener, forward its host/port over SSH and use the matching HTTP/TLS URL in your browser.\n'
  printf 'Default-listener example: ssh -N -L 4545:127.0.0.1:4545 USER@VPS\n'
fi
if [[ -n $public_url ]]; then printf 'Configured public URL: %s (configure and verify the HTTPS proxy/TLS separately).\n' "$public_url"; fi
linger=no
if command -v loginctl >/dev/null 2>&1; then linger=$(loginctl show-user "$(id -u)" --property=Linger --value 2>/dev/null || printf unknown); fi
if [[ $linger != yes ]]; then
  printf '\nUser lingering is not enabled or could not be verified. This user service is not guaranteed to survive logout or start after reboot.\n'
  printf 'Ask the VPS administrator to enable it explicitly: loginctl enable-linger %s\n' "$(id -un)"
  printf 'The installer did not run that administrator command.\n'
fi
