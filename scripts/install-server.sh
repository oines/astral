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
allow_remote=true
restrict_hostnames=false
github_proxy=${ASTRAL_GITHUB_PROXY:-}
github_proxy_set=0
if [[ ${ASTRAL_GITHUB_PROXY+x} ]]; then github_proxy_set=1; fi
listen_set=0
public_set=0
remote_set=0
if [[ ${ASTRAL_ALLOW_REMOTE_MANAGEMENT+x} ]]; then allow_remote=$ASTRAL_ALLOW_REMOTE_MANAGEMENT; remote_set=1; fi
download_only=
dry_run=0
uninstall=0
install_options=0
stage=
unit_temp=
link_temp=
mutating=0
complete=0
had_unit=0
had_config=0
config_snapshot=0
had_marker=0
marker_snapshot=0
was_active=0
was_enabled=0
old_current=
recovery_program=
previous_upgrade=
upgrade_baseline_ready=0
command_path=
command_needs_sudo=0
command_created=0

die() { printf 'Astral Server: %s\n' "$*" >&2; exit 1; }
usage() {
  cat <<'HELP'
Astral Server installer (Linux amd64/arm64, current user only)

Usage: bash install-server.sh [OPTIONS]
  --tag TAG             Published vMAJOR.MINOR.PATCH tag, or latest (default)
  --root DIRECTORY      Dedicated installation/data root (absolute path)
  --listen HOST:PORT    HTTP listen address (first install: 0.0.0.0:4545)
  --public-url URL      HTTP or reverse-proxy HTTPS address; empty selects automatic
  --allow-remote-management[=true|false]
                       Persist a service startup override (Server default: true)
                       Or use ASTRAL_ALLOW_REMOTE_MANAGEMENT=true|false
  --github-proxy PREFIX Optional HTTPS GitHub download proxy, for example
                       https://gh-proxy.org/ (or ASTRAL_GITHUB_PROXY; empty disables)
  --download-only DIR   Download and verify the full distribution; execute nothing
  --uninstall           Remove this user's managed Server; retain data and recovery files
  --dry-run            Print the plan; no network, writes or service operations
  --version            Print the installer format version
  --help               Show this help

Listen and access-address options update saved App settings. Remote-management
overrides use the user-service environment; omitted explicit choices are preserved.
The Server serves HTTP; configure HTTPS on your own reverse proxy. Installation prepares
user lingering and starts the user manager only if needed. These account-wide setup
actions and command registration may request sudo; the Server stays in the current user.
Uninstall does not disable lingering, delete data or remove Docker. Initial Web account setup requires a
terminal, or ASTRAL_ADMIN_USERNAME and ASTRAL_ADMIN_PASSWORD_FILE.
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
    --tag) need_value "$@"; tag=$2; install_options=1; shift 2 ;;
    --root) need_value "$@"; root=$2; shift 2 ;;
    --listen) need_value "$@"; listen=$2; listen_set=1; install_options=1; shift 2 ;;
    --public-url) [[ $# -ge 2 ]] || die '--public-url requires a value'; public_url=$2; public_set=1; install_options=1; shift 2 ;;
    --allow-remote-management) allow_remote=true; remote_set=1; install_options=1; shift ;;
    --allow-remote-management=true|--allow-remote-management=false) allow_remote=${1#*=}; remote_set=1; install_options=1; shift ;;
    --github-proxy) [[ $# -ge 2 ]] || die '--github-proxy requires a value'; github_proxy=$2; github_proxy_set=1; install_options=1; shift 2 ;;
    --github-proxy=*) github_proxy=${1#*=}; github_proxy_set=1; install_options=1; shift ;;
    --download-only) need_value "$@"; download_only=$2; install_options=1; shift 2 ;;
    --uninstall) uninstall=1; shift ;;
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
unit_dir=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user
unit=$unit_dir/$service
# BEGIN user service preparation
user_service_environment() {
  local uid
  uid=$(id -u)
  export XDG_RUNTIME_DIR=/run/user/$uid
  export DBUS_SESSION_BUS_ADDRESS=unix:path=$XDG_RUNTIME_DIR/bus
}
authorize_system_preparation() {
  command -v sudo >/dev/null 2>&1 || die 'administrator access is required to prepare this user service, but sudo is unavailable; ask an administrator to complete the printed action, then retry'
  if ! sudo -n true 2>/dev/null; then
    if ! { exec 8<>/dev/tty; } 2>/dev/null; then die 'user-service preparation requires sudo authentication in a terminal; the existing Astral installation was not changed'; fi
    sudo -v <&8 || die 'administrator access for user-service preparation was not granted; the existing Astral installation was not changed'
    exec 8>&-
  fi
}
prepare_user_service() {
  local uid account linger deadline
  uid=$(id -u); account=$(id -un)
  user_service_environment
  command -v loginctl >/dev/null 2>&1 || die 'loginctl is required to prepare persistent user services; the existing Astral installation was not changed'
  linger=$(loginctl show-user "$uid" --property=Linger --value 2>/dev/null || true)
  if [[ $linger != yes ]]; then
    printf 'Preparing account %s (UID %s): loginctl enable-linger %s\n' "$account" "$uid" "$uid"
    printf 'Lingering lets this account\047s user services run after logout and start at boot; uninstalling Astral will leave this account-wide setting enabled.\n'
    if [[ $uid == 0 ]]; then loginctl enable-linger "$uid" || die 'could not enable user lingering; the existing Astral installation was not changed'
    elif ! loginctl --no-ask-password enable-linger "$uid" 2>/dev/null; then
      authorize_system_preparation
      sudo -n loginctl enable-linger "$uid" || die 'could not enable user lingering; the existing Astral installation was not changed'
    fi
    [[ $(loginctl show-user "$uid" --property=Linger --value 2>/dev/null || true) == yes ]] || die 'user lingering could not be verified; the existing Astral installation was not changed'
  fi
  if ! systemctl --user show-environment >/dev/null 2>&1 && ! systemctl is-active --quiet "user@$uid.service"; then
    printf 'Starting account %s\047s user service manager: systemctl start user@%s.service\n' "$account" "$uid"
    if [[ $uid == 0 ]]; then systemctl start "user@$uid.service" || die 'could not start the user service manager; the existing Astral installation was not changed'
    else
      authorize_system_preparation
      sudo -n systemctl start "user@$uid.service" || die 'could not start the user service manager; the existing Astral installation was not changed'
    fi
  fi
  deadline=$((SECONDS+15))
  until systemctl --user show-environment >/dev/null 2>&1; do
    ((SECONDS<deadline)) || die 'the user service manager is not reachable; verify systemd user-session D-Bus support and /run/user/UID/bus, then retry. The existing Astral installation was not changed'
    sleep 1
  done
}
# END user service preparation
# BEGIN managed Server uninstall
uninstall_server() {
  python3 - "$root" "$unit" "$dry_run" "$(command -v astral || true)" <<'PYUNINSTALL'
import hashlib,json,os,pathlib,re,shutil,subprocess,sys
root,unit,dry,selected=map(str,sys.argv[1:])
uid=os.getuid()
def fail(message): raise SystemExit('Astral Server: '+message)
def owned(path,directory=False):
    try:
        value=os.lstat(path)
        return value.st_uid==uid and not os.path.islink(path) and (os.path.isdir(path) if directory else os.path.isfile(path))
    except OSError: return False
def run(*args,check=True):
    result=subprocess.run(args,text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    if check and result.returncode: fail('command failed: '+' '.join(args)+'; '+result.stderr.strip())
    return result
if not os.path.isabs(unit) or any(ord(c)<32 or ord(c)==127 for c in root+unit): fail('installation and unit paths must be absolute and contain no control characters')
marker_path=root+'/installation.json'
if not owned(root,True) or not owned(marker_path): fail('installation ownership could not be verified: '+root)
try:
    with open(marker_path,encoding='utf-8') as f: marker=json.load(f)
except (OSError,ValueError): fail('installation metadata could not be read; inspect '+marker_path)
if not isinstance(marker,dict): fail('installation metadata must be an object; inspect '+marker_path)
expected={'schema':1,'root':root,'data_directory':root+'/data/server','uid':uid,'service':'astral-server.service','unit_path':unit}
if any(marker.get(k)!=v for k,v in expected.items()): fail('installation metadata does not match this account, root and user service: '+marker_path)
if marker.get('uninstalled') is True and not os.path.lexists(unit):
    print('Astral Server is already uninstalled. Retained data and recovery files: '+root)
    sys.exit(0)
def quote(value): return '"'+value.replace('\\','\\\\').replace('"','\\"').replace('%','%%')+'"'
args=[root+'/current/bin/astral','serve','--installation-root',root,'--data',root+'/data/server','--computers-dir',root+'/data/computers','--distribution-dir',root+'/current/distribution','--ready-file',root+'/run/ready.json']
environment=''
if 'github_proxy' in marker:
    if not isinstance(marker['github_proxy'],str): fail('invalid saved GitHub proxy')
    environment+='Environment='+quote('ASTRAL_GITHUB_PROXY='+marker['github_proxy'])+'\n'
if 'allow_remote_management' in marker:
    if type(marker['allow_remote_management']) is not bool: fail('invalid saved remote-management setting')
    environment+='Environment='+quote('ASTRAL_ALLOW_REMOTE_MANAGEMENT='+str(marker['allow_remote_management']).lower())+'\n'
expected_unit='# Astral Server installer root: '+root+'\n[Unit]\nDescription=Astral Server\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nWorkingDirectory=/\n'+environment+'ExecStart=:'+ ' '.join(map(quote,args))+'\nRestart=on-failure\nRestartSec=3\nTimeoutStopSec=30\nUMask=0077\n\n[Install]\nWantedBy=default.target\n'
if not owned(unit) or pathlib.Path(unit).read_text()!=expected_unit: fail('the service file was modified or is not owned by this installation; inspect '+unit)
def service_state():
    output=run('systemctl','--user','show','astral-server.service','--property=Id,LoadState,ActiveState,MainPID,FragmentPath,DropInPaths','--no-pager').stdout
    return dict(line.split('=',1) for line in output.splitlines() if '=' in line)
state=service_state()
if state.get('Id')!='astral-server.service' or state.get('LoadState')!='loaded' or state.get('FragmentPath')!=unit or state.get('DropInPaths'):
    fail('the loaded service has a different definition or overrides; inspect systemctl --user cat astral-server.service')
releases=root+'/releases'
current=root+'/current'
cohort=os.path.realpath(current)
if not os.path.islink(current) or os.path.dirname(cohort)!=releases or not owned(releases,True) or not owned(cohort,True): fail('current does not select an owned release inside '+releases)
native=os.path.realpath(current+'/bin/astral')
if os.path.dirname(native)!=cohort+'/distribution' or not owned(native): fail('the installed program does not belong to its release')
try:
    manifest=json.loads(pathlib.Path(cohort+'/distribution/release-manifest.json').read_text())
    asset=next(a for a in manifest['assets'] if a['name']==os.path.basename(native))
    if hashlib.sha256(pathlib.Path(native).read_bytes()).hexdigest()!=asset['sha256']: fail('the installed recovery program was modified; files were retained')
except (OSError,ValueError,KeyError,StopIteration): fail('the recovery program could not be verified; files were retained')
entry=marker.get('command_path',selected)
if entry:
    if not isinstance(entry,str) or not os.path.isabs(entry) or any(ord(c)<32 for c in entry): fail('invalid saved command entry')
    if os.path.lexists(entry) and (not os.path.islink(entry) or os.readlink(entry)!=current+'/bin/astral'): fail('the command entry no longer belongs to this installation; inspect '+entry)
shared=False
retained=set([cohort])
# A Computer installation can reuse this terminal command while its daemon
# runs its own downloaded executable. Its registration, not just ExecStart,
# therefore keeps the shared command available for maintenance.
config_root=pathlib.Path(os.environ.get('XDG_CONFIG_HOME') or str(pathlib.Path.home()/'.config'))/'astral'
computer_roots=list((config_root/'computers').glob('*/*'))
for registration in (config_root/'installations/computers').glob('*.json'):
    try:
        registration_value=json.loads(registration.read_text())
        if isinstance(registration_value.get('root'),str) and os.path.isabs(registration_value['root']): computer_roots.append(pathlib.Path(registration_value['root']))
    except (OSError,ValueError): continue
for computer_root in computer_roots:
    try:
        if not owned(str(computer_root),True): continue
        credentials=computer_root/'credentials.json'
        container=computer_root/'container.json'
        if owned(str(credentials)) and owned(str(computer_root/'service/spec.json')) and json.loads(credentials.read_text()).get('computer_id'):
            shared=True
        elif owned(str(container)):
            value=json.loads(container.read_text())
            if value.get('name') and value.get('identity'): shared=True
    except (OSError,ValueError): continue
# A separate user unit or live process may use this unified program. Retain its
# entry and release rather than breaking another Daemon or foreground command.
for candidate in pathlib.Path(unit).parent.rglob('*'):
    if candidate.is_file() and str(candidate)!=unit and not candidate.is_symlink():
        try: text=candidate.read_text()
        except (OSError,UnicodeError): continue
        if current in text or (entry and entry in text) or re.search(r'(?m)^ExecStart(?:Pre|Post)?=[-+!:@]*"?astral"?(?:\s|$)',text): shared=True
        for release in pathlib.Path(releases).iterdir():
            if str(release) in text: retained.add(str(release))
server_pid=int(state.get('MainPID','0'))
def server_descendant(pid):
    seen=set()
    while pid>1 and pid not in seen:
        if pid==server_pid: return True
        seen.add(pid)
        try:
            status=pathlib.Path('/proc/'+str(pid)+'/status').read_text()
            pid=int(re.search(r'(?m)^PPid:\s*(\d+)',status).group(1))
        except (OSError,ValueError,AttributeError): return False
    return False
for process in pathlib.Path('/proc').glob('[0-9]*'):
    try:
        if process.stat().st_uid!=uid: continue
        pid=int(process.name)
        if pid in (os.getpid(),os.getppid()) or server_descendant(pid): continue
        executable=os.path.realpath(str(process/'exe'))
        command=(process/'cmdline').read_bytes().decode(errors='replace').split('\0')
        if current+'/bin/astral' in command or (entry and entry in command): shared=True
        for release in pathlib.Path(releases).iterdir():
            if executable.startswith(str(release)+'/'): retained.add(str(release))
    except (OSError,ValueError): continue
for directory in os.environ.get('PATH','').split(os.pathsep):
    if not os.path.isabs(directory): continue
    try:
        for candidate in pathlib.Path(directory).iterdir():
            if str(candidate)!=entry and candidate.is_symlink() and os.path.realpath(candidate)==native: shared=True
    except OSError: continue
if shared: print('The astral command is shared with another Computer installation, user service or process; its entry and current release will be retained.')
needs_sudo=bool(entry and os.path.lexists(entry) and not shared and not os.access(os.path.dirname(entry),os.W_OK))
if dry=='1':
    print('No changes made. Plan: stop and disable astral-server.service; remove '+unit)
    print('Retain data, configuration, backups and one verified recovery release: '+root)
    if entry and not shared: print('Remove owned command entry: '+entry)
    if needs_sudo: print('Removing the command entry requires sudo.')
    sys.exit(0)
if needs_sudo:
    print('Removing the owned command entry requires administrator access: '+entry)
    if not shutil.which('sudo'): fail('sudo is unavailable; no service or files were changed')
    if run('sudo','-n','true',check=False).returncode:
        try:
            with open('/dev/tty','r+') as tty:
                if subprocess.run(['sudo','-v'],stdin=tty,stdout=tty,stderr=tty).returncode: fail('administrator access was not granted; no service or files were changed')
        except OSError: fail('sudo authentication requires a terminal; no service or files were changed')
run('systemctl','--user','stop','astral-server.service')
state=service_state()
if state.get('ActiveState') not in ('inactive','failed') or state.get('MainPID')!='0': fail('the Server has not stopped; no program or service files were removed')
run('systemctl','--user','disable','astral-server.service')
if run('systemctl','--user','is-enabled','--quiet','astral-server.service',check=False).returncode==0: fail('the Server is still enabled; no program or service files were removed')
if not owned(unit) or pathlib.Path(unit).read_text()!=expected_unit: fail('the service file changed during uninstall; files were retained')
# Reinstallation needs a trusted old executable for version/format checks and
# migration recovery. Keep one full verified cohort so same-version reinstall
# can also reuse its immutable distribution without a partial-directory error.
data_program=root+'/data-program'
if os.path.lexists(data_program) and not os.path.islink(data_program): fail('data-program is not an installer symlink; files were retained')
temporary=root+'/.data-program.uninstall.'+str(os.getpid())
os.symlink(cohort,temporary)
os.replace(temporary,data_program)
if entry and os.path.lexists(entry) and not shared:
    if not os.path.islink(entry) or os.readlink(entry)!=current+'/bin/astral': fail('the command entry changed during uninstall; files were retained')
    if needs_sudo: run('sudo','-n','rm','--',entry)
    else: os.unlink(entry)
os.unlink(unit)
run('systemctl','--user','daemon-reload')
if not shared: os.unlink(current)
allowed={'astral-darwin-amd64','astral-darwin-arm64','astral-linux-amd64','astral-linux-arm64','astral-windows-amd64.exe','astral-windows-arm64.exe','checksums.txt','LICENSE.txt','THIRD_PARTY_NOTICES.txt','SHA256SUMS','release-manifest.json'}
for release in pathlib.Path(releases).iterdir():
    if str(release) in retained: continue
    if not re.fullmatch(r'v\d+\.\d+\.\d+-[0-9a-f]{16}',release.name) or not owned(str(release),True): continue
    # Unknown files, symlinks or changed ownership are preserved for inspection.
    safe=set(p.name for p in release.iterdir())=={'bin','distribution'}
    for child in release.rglob('*'):
        if child.is_symlink(): safe=safe and child==release/'bin/astral' and os.readlink(child).startswith('../distribution/astral-linux-')
        elif child.is_dir(): safe=safe and child in (release/'bin',release/'distribution') and owned(str(child),True)
        else: safe=safe and child.parent==release/'distribution' and child.name in allowed and owned(str(child))
    if safe: shutil.rmtree(release)
marker['uninstalled']=True
temporary=marker_path+'.uninstall.'+str(os.getpid())
with open(temporary,'x',encoding='utf-8') as f:
    json.dump(marker,f,indent=2); f.write('\n'); f.flush(); os.fsync(f.fileno())
os.replace(temporary,marker_path)
print('Astral Server was stopped, disabled and uninstalled for this account.')
print('Retained data, account, Computer identities, backups and recovery release: '+root)
print('User lingering, Docker, containers and files on other computers were left in place.')
PYUNINSTALL
}
# END managed Server uninstall
if [[ $uninstall == 1 ]]; then
  [[ $install_options == 0 ]] || die '--uninstall accepts only --root and --dry-run, not installation options'
  [[ $(uname -s) == Linux || $dry_run == 1 ]] || die 'this installer supports Linux only'
  for command in systemctl flock id; do command -v "$command" >/dev/null 2>&1 || die "required command is missing: $command"; done
  user_service_environment
  [[ -d $unit_dir ]] || die "no user service installation was found: $unit_dir"
  if [[ $dry_run == 0 ]]; then
    exec 9>"$unit_dir/.astral-server-install.lock"
    flock -n 9 || die 'another Astral Server installer is running for this user'
  fi
  uninstall_server
  exit 0
fi
config=$root/data/server/network.json
if [[ $github_proxy_set == 0 && -f $root/installation.json ]]; then
  github_proxy=$(python3 - "$root/installation.json" <<'PYSAVEDPROXY'
import json,sys
with open(sys.argv[1],encoding='utf-8') as f: value=json.load(f).get('github_proxy','')
if not isinstance(value,str): raise SystemExit('Invalid saved GitHub proxy')
print(value)
PYSAVEDPROXY
)
fi
if [[ $remote_set == 0 && -f $root/installation.json ]]; then
  saved_remote=$(python3 - "$root/installation.json" <<'PYSAVEDREMOTE'
import json,sys
with open(sys.argv[1],encoding='utf-8') as f: value=json.load(f)
if 'allow_remote_management' not in value: print('default')
elif type(value['allow_remote_management']) is bool: print(str(value['allow_remote_management']).lower())
else: raise SystemExit('Invalid saved remote-management override')
PYSAVEDREMOTE
)
  if [[ $saved_remote != default ]]; then allow_remote=$saved_remote; remote_set=1; fi
fi
fresh_install=0
if [[ ! -f $config && ! -e $root/installation.json && ! -L $root/current && ! -L $root/data-program ]]; then fresh_install=1; fi
if [[ $fresh_install == 0 ]]; then restrict_hostnames=true; fi
if [[ -z $download_only && -f $root/install-config.json && ! -f $config ]]; then
  die 'run the documented offline network migration before updating this older installation (docs/deployment.md)'
fi
ready=$root/run/ready.json
config_value() {
  python3 - "$config" "$1" "$2" <<'PY'
import json,sys
with open(sys.argv[1], encoding='utf-8') as f: value=json.load(f).get(sys.argv[2], sys.argv[3])
if isinstance(value,bool): value=str(value).lower()
if not isinstance(value,str) or any(ord(c)<32 or ord(c)==127 for c in value): raise SystemExit('Invalid saved network configuration')
print(value)
PY
}
if [[ -z $download_only && -f $config ]]; then
  [[ $listen_set == 1 ]] || listen=$(config_value listen 0.0.0.0:4545)
  [[ $public_set == 1 ]] || public_url=$(python3 - "$config" <<'PYCFG'
import json,sys
with open(sys.argv[1],encoding='utf-8') as f: values=json.load(f).get('public_urls',[])
if not isinstance(values,list) or any(not isinstance(v,str) or any(ord(c)<32 for c in v) for v in values): raise SystemExit('Invalid saved access addresses')
print(values[0] if values else '')
PYCFG
)
  restrict_hostnames=$(config_value restrict_hostnames true)
fi
case $allow_remote in
  1|t|T|TRUE|True) allow_remote=true ;;
  0|f|F|FALSE|False) allow_remote=false ;;
esac
github_proxy=$(python3 - "$github_proxy" <<'PYPROXY'
import sys,urllib.parse
value=sys.argv[1]
if value:
    try:
        parsed=urllib.parse.urlsplit(value)
        if parsed.scheme!='https' or not parsed.hostname or parsed.username or parsed.password or '?' in value or '#' in value or '\\' in value or any(c.isspace() or ord(c)<32 or ord(c)==127 for c in value): raise ValueError()
        if parsed.port is not None and not 1<=parsed.port<=65535: raise ValueError()
    except ValueError: raise SystemExit('GitHub proxy must be an HTTPS prefix without credentials, query, fragment or whitespace')
    value=value.rstrip('/')+'/'
print(value)
PYPROXY
)
listen=${listen:-0.0.0.0:4545}
# Validate saved network preferences before any databases are opened.
python3 - "$root" "$unit_dir" "$listen" "$public_url" "$allow_remote" "$download_only" <<'PY'
import os,sys,urllib.parse
root,units,listen,public,remote,download=sys.argv[1:]
for value in sys.argv[1:]:
    if any(ord(c)<32 or ord(c)==127 for c in value): raise SystemExit('Control characters are not allowed in installer arguments')
if not os.path.isabs(units): raise SystemExit('XDG_CONFIG_HOME must be absolute')
try:
    address=urllib.parse.urlsplit('//'+listen)
    host=address.hostname
    port=address.port
    if not host or port is None or not 1<=port<=65535 or address.path or address.username or address.password: raise ValueError()
except ValueError: raise SystemExit('--listen must be HOST:PORT with a fixed port between 1 and 65535')
if public:
    url=urllib.parse.urlsplit(public)
    if url.scheme not in ('http','https') or not url.hostname or url.username or url.password or url.fragment or url.query or url.path not in ('','/'):
        raise SystemExit('--public-url must be an HTTP/HTTPS origin without credentials, query or fragment')
if remote not in ('true','false'): raise SystemExit('allow_remote_management must be true or false')
if download and (not os.path.isabs(download) or os.path.lexists(download) or not os.path.isdir(os.path.dirname(download))):
    raise SystemExit('--download-only requires a new absolute directory whose parent exists')
PY
case $(uname -m) in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) die 'unsupported CPU architecture (requires amd64 or arm64)' ;; esac
if [[ $dry_run == 1 ]]; then
  printf 'No changes made. Plan: Linux %s; GitHub %s release %s\n' "$arch" "$repository" "$tag"
  printf 'Installation root: %s\nListen: %s\nRemote management: %s\n' "$root" "$listen" "$allow_remote"
  printf 'Hostname restriction: %s\nGitHub downloads: %s\n' "$restrict_hostnames" "${github_proxy:-direct}"
  if [[ -n $download_only ]]; then printf 'Download and verify only: %s\n' "$download_only"; else printf 'User service: %s\nData: %s/data\n' "$unit" "$root"; fi
  printf 'Requires a published ready release and Linux with systemd; prepare user lingering and start the user manager only when needed (may require sudo).\n'
  exit 0
fi
# BEGIN managed command entry
command_directory_writable() { [[ -d $1 && -w $1 && -x $1 ]]; }
authorize_command_registration() {
  if ! sudo -n true 2>/dev/null; then
    if ! { exec 8<>/dev/tty; } 2>/dev/null; then die "registering $command_path requires sudo authentication in a terminal; command registration did not complete"; fi
    sudo -v <&8 || die 'administrator access for command registration was not granted'
    exec 8>&-
  fi
}
select_command_entry() {
  local existing directory candidate
  existing=$(command -v astral || true)
  if [[ -n $existing ]]; then
    [[ $existing == /* && -L $existing && $(readlink "$existing") == "$root/current/bin/astral" ]] || die "an unrelated astral command already exists: $existing; installation did not replace it"
    command_path=$existing
    return
  fi
  local -a directories
  IFS=: read -r -a directories <<< "${PATH:-}"
  for directory in "${directories[@]}"; do
    [[ $directory == /* && $directory != *$'\n'* && $directory != *$'\r'* ]] || continue
    if command_directory_writable "$directory"; then command_path=${directory%/}/astral; break; fi
  done
  if [[ -z $command_path ]]; then
    for candidate in /usr/local/bin /usr/bin; do
      for directory in "${directories[@]}"; do
        if [[ ${directory%/} == "$candidate" && -d $candidate ]]; then command_path=$candidate/astral; command_needs_sudo=1; break 2; fi
      done
    done
    [[ -n $command_path ]] || die 'no writable directory or supported administrator-writable directory exists on the current PATH; ask the administrator to provide command installation access, then retry'
    command -v sudo >/dev/null 2>&1 || die "registering $command_path requires administrator access, but sudo is unavailable; installation has not started"
    printf 'Registering astral at %s requires sudo; the Server service will still run as the current user.\n' "$command_path"
    authorize_command_registration
  fi
  if [[ -e $command_path || -L $command_path ]]; then
    [[ -L $command_path && $(readlink "$command_path") == "$root/current/bin/astral" ]] || die "refusing to overwrite an unrelated command: $command_path"
  fi
}
install_command_entry() {
  if [[ ! -e $command_path && ! -L $command_path ]]; then
    if [[ $command_needs_sudo == 1 ]]; then authorize_command_registration; sudo -n ln -s -- "$root/current/bin/astral" "$command_path"
    else ln -s -- "$root/current/bin/astral" "$command_path"; fi
    command_created=1
  fi
  local actual
  actual=$(command -v astral || true)
  [[ -n $actual && -L $actual && $(readlink "$actual") == "$root/current/bin/astral" ]] || die 'the installed astral command is not the command selected by the current PATH'
  [[ $(astral version) == "$version" ]] || die 'the astral command does not run the newly installed version'
}
# END managed command entry
[[ $(uname -s) == Linux ]] || die 'this installer supports Linux only'
for command in curl sha256sum awk mktemp mkdir mv ln rm chmod cp; do command -v "$command" >/dev/null 2>&1 || die "required command is missing: $command"; done
if [[ -z $download_only ]]; then
  for command in systemctl flock grep readlink find id; do command -v "$command" >/dev/null 2>&1 || die "required command is missing: $command"; done
  mkdir -p "$unit_dir"
  exec 9>"$unit_dir/.astral-server-install.lock"
  flock -n 9 || die 'another Astral Server installer is running for this user'
  if [[ -e $unit || -L $unit ]]; then
    [[ -f $unit && ! -L $unit ]] || die "refusing to replace an unmanaged service definition: $unit"
    grep -Fqx "# Astral Server installer root: $root" "$unit" || die 'an Astral Server service exists for a different root or was not created by this installer'
  fi
  prepare_user_service
  select_command_entry
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
    if [[ -n $recovery_program && -n ${old_program:-} ]]; then
      # A program may fail before opening data or even initializing its CLI.
      # Only a real migration journal requires the new recovery command.
      if [[ -f $root/data/server/migration-state.json ]]; then
        "$recovery_program" migrate recover --data "$root/data/server" || recovery_failed=1
      fi
      if [[ $upgrade_baseline_ready == 1 && -f $root/data/server/last-upgrade.json ]]; then
        current_upgrade=$(sha256sum "$root/data/server/last-upgrade.json" | awk '{print $1}')
        if [[ $current_upgrade != "$previous_upgrade" ]]; then
          "$recovery_program" migrate restore --data "$root/data/server" --latest || recovery_failed=1
        fi
      fi
    fi
    if [[ -n $old_current ]]; then
      ln -s "$old_current" "$root/.current.restore.$$" && mv -Tf "$root/.current.restore.$$" "$root/current" || recovery_failed=1
    else rm -f "$root/current" || recovery_failed=1; fi
    if [[ $had_unit == 1 ]]; then cp "$stage/previous.service" "$unit" || recovery_failed=1; else rm -f "$unit" || recovery_failed=1; fi
    if [[ $config_snapshot == 1 ]]; then
      if [[ $had_config == 1 ]]; then cp "$stage/previous-config.json" "$config" || recovery_failed=1; else rm -f "$config" || recovery_failed=1; fi
    fi
    if [[ $marker_snapshot == 1 ]]; then
      if [[ $had_marker == 1 ]]; then cp "$stage/previous-installation.json" "$root/installation.json" || recovery_failed=1; else rm -f "$root/installation.json" || recovery_failed=1; fi
    fi
    systemctl --user daemon-reload || recovery_failed=1
    if [[ $was_enabled == 1 ]]; then systemctl --user enable "$service" >/dev/null 2>&1 || recovery_failed=1
    else systemctl --user disable "$service" >/dev/null 2>&1 || true; fi
    if [[ $was_active == 1 && $recovery_failed == 0 ]]; then systemctl --user start "$service" && systemctl --user is-active --quiet "$service" || recovery_failed=1; fi
    printf 'Computer files were retained; any attempted data migration was restored before restarting the previous Server.\n' >&2
    if [[ $recovery_failed == 1 ]]; then printf 'Automatic service recovery was incomplete. Backups remain in %s; inspect systemctl --user status %s.\n' "$stage" "$service" >&2; fi
  fi
  if [[ $status != 0 && $complete == 0 && $command_created == 1 && -L $command_path && $(readlink "$command_path") == "$root/current/bin/astral" ]]; then
    if [[ $command_needs_sudo == 1 ]]; then sudo -n rm -- "$command_path" || recovery_failed=1
    else rm -- "$command_path" || recovery_failed=1; fi
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
  local source=$1
  [[ $source == "https://api.github.com/repos/$repository/releases/"* || $source == "https://github.com/$repository/releases/download/"* ]] || die 'refusing an unofficial GitHub download source'
  curl --fail --location --silent --show-error --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 1200 --retry 2 "${github_proxy}${source}" -o "$2"
}
if [[ -n $github_proxy ]]; then printf 'GitHub downloads will use the selected third-party proxy: %s\n' "$github_proxy"; fi
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
baselines=m.get('migration_baselines',{})
if m.get('migration_status')!='goose' or baselines!={'host':3,'core':11} or m.get('migration_targets')!={'host':host,'core':core} or host<3 or core<11: raise SystemExit('Invalid Goose migration ranges')
if 'core_schema' in m and (type(m['core_schema']) is not int or m['core_schema']<11 or m.get('storage_migrations')!={'core_sqlite':{'baseline':11,'target':m['core_schema']}}): raise SystemExit('Invalid Core SQLite migration metadata')
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
python3 - "$distribution/release-manifest.json" "$new_program" <<'PYSCHEMA'
import json,subprocess,sys
with open(sys.argv[1],encoding='utf-8') as f: manifest=json.load(f)
if 'core_schema' in manifest:
    executable=json.loads(subprocess.check_output([sys.argv[2],'version','--json'],timeout=15))
    if any(executable.get(key)!=manifest.get(key) for key in ('core_schema','storage_migrations')): raise SystemExit('Executable SQLite schema differs from release metadata')
PYSCHEMA
# BEGIN required Server capabilities
require_server_installer_capabilities() {
  local auth_help hostname_help serve_help
  # FlagSet help returns before installation discovery, local control, or data
  # access. Older binaries must never receive new strict network.json fields.
  auth_help=$("$new_program" auth status --help 2>&1 || true)
  hostname_help=$("$new_program" config hostnames --help 2>&1 || true)
  serve_help=$("$new_program" serve --help 2>&1 || true)
  if [[ $auth_help != *'Usage of auth status:'* || $hostname_help != *'Usage of config hostnames:'* || $serve_help != *'-restrict-hostnames'* ]]; then
    die 'the selected release does not support this installer; use the installer included with that older release to install it. The existing service and settings were not changed'
  fi
}
require_server_installer_capabilities
# END required Server capabilities
old_program=
if [[ -L $root/current ]]; then
  old_current=$(readlink "$root/current")
  old_program=$root/current/bin/astral
elif [[ -L $root/data-program ]]; then old_program=$root/data-program/bin/astral; fi
if [[ -n $old_program ]]; then
  resolved=$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$old_program")
  [[ $resolved == "$root/releases/"* && -x $old_program ]] || die 'existing program is not an installer-managed executable'
  old_version=$("$old_program" version) || die 'existing program lacks version metadata'
  python3 - "$old_version" "$version" <<'PYVERSION'
import re,sys
versions=[]
for value in sys.argv[1:]:
    if not re.fullmatch(r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)',value): raise SystemExit('Invalid installed or target Server version')
    versions.append(tuple(map(int,value.split('.'))))
if versions[1]<versions[0]: raise SystemExit('Server downgrades are not supported; the current service is unchanged')
PYVERSION
  old_formats=$("$old_program" version --data-formats) || die 'existing program lacks data-format metadata; use a separate empty root or follow explicit migration instructions'
  [[ $old_formats =~ ^[0-9]+\ [0-9]+$ ]] || die 'existing program lacks machine-readable data formats; use a separate empty root or follow explicit migration instructions'
  python3 - "$old_formats" "$new_formats" <<'PY'
import sys
old=list(map(int,sys.argv[1].split())); new=list(map(int,sys.argv[2].split()))
if len(old)!=2 or len(new)!=2 or any(not baseline<=a<=b for baseline,a,b in zip((3,11),old,new)):
    raise SystemExit('Existing data is outside the supported forward migration chain; the old service is unchanged')
PY
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
if systemctl --user is-active --quiet "$service"; then was_active=1; fi
if systemctl --user is-enabled --quiet "$service"; then was_enabled=1; fi
# Stop only after release validation, then read the last committed App settings.
# Taking this snapshot before stopping could lose a setting saved during update.
mutating=1
if [[ $had_unit == 1 ]]; then systemctl --user stop "$service"; fi
recovery_program=$release/bin/astral
# Finish an earlier interrupted attempt before recording this attempt's base.
# Recovery can itself restore an older last-upgrade record from the snapshot.
if [[ -f $root/data/server/migration-state.json ]]; then
  "$recovery_program" migrate recover --data "$root/data/server" || die 'the previous migration could not be recovered; data and backups were retained'
fi
if [[ -f $root/data/server/last-upgrade.json ]]; then previous_upgrade=$(sha256sum "$root/data/server/last-upgrade.json" | awk '{print $1}'); fi
upgrade_baseline_ready=1
if [[ -f $config ]]; then cp "$config" "$stage/previous-config.json"; had_config=1; fi
config_snapshot=1
if [[ -f $root/installation.json ]]; then cp "$root/installation.json" "$stage/previous-installation.json"; had_marker=1; fi
marker_snapshot=1
python3 - "$stage/new-config.json" "$config" "$listen_set" "$listen" "$public_set" "$public_url" "$fresh_install" <<'PY'
import json,os,sys
output,previous,listen_set,listen,public_set,public,fresh=sys.argv[1:]
value={}
if os.path.isfile(previous):
    with open(previous,encoding='utf-8') as f: value=json.load(f)
if listen_set=='1': value['listen']=listen
if public_set=='1': value['public_urls']=[public] if public else []
if fresh=='1': value['restrict_hostnames']=False
with open(output,'w',encoding='utf-8') as f: json.dump(value,f,ensure_ascii=False)
PY
unit_temp=$(mktemp "$unit_dir/.astral-server.service.XXXXXX")
python3 - "$unit_temp" "$root" "$github_proxy" "$remote_set" "$allow_remote" <<'PY'
import sys
target,root,proxy,remote_set,remote=sys.argv[1:]
# The ':' executable prefix disables environment substitution. In particular,
# executable paths do not undergo the same '$$' expansion as ordinary arguments.
# Keep dollars literal in both places; percent specifiers still need escaping.
def quote(value): return '"'+value.replace('\\','\\\\').replace('"','\\"').replace('%','%%')+'"'
args=[root+'/current/bin/astral','serve','--installation-root',root,'--data',root+'/data/server','--computers-dir',root+'/data/computers','--distribution-dir',root+'/current/distribution','--ready-file',root+'/run/ready.json']
environment='Environment='+quote('ASTRAL_GITHUB_PROXY='+proxy)+'\n'
if remote_set=='1': environment+='Environment='+quote('ASTRAL_ALLOW_REMOTE_MANAGEMENT='+remote)+'\n'
with open(target,'w',encoding='utf-8') as f:
    f.write('# Astral Server installer root: '+root+'\n[Unit]\nDescription=Astral Server\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nWorkingDirectory=/\n'+environment+'ExecStart=:'+ ' '.join(map(quote,args))+'\nRestart=on-failure\nRestartSec=3\nTimeoutStopSec=30\nUMask=0077\n\n[Install]\nWantedBy=default.target\n')
PY
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
systemctl --user is-enabled --quiet "$service" || die 'Server automatic startup could not be verified'
systemctl --user start "$service"
server_url=
startup_deadline=$((SECONDS+45))
maintenance_pid=
maintenance_active=0
while :; do
  pid=$(systemctl --user show "$service" --property=MainPID --value)
  if [[ -n $maintenance_pid && $pid != "$maintenance_pid" ]]; then die 'Server exited or restarted during data maintenance'; fi
  maintaining=0
  if [[ $pid =~ ^[1-9][0-9]*$ && -f $root/data/server/startup-maintenance.json ]]; then
    if python3 - "$root/data/server/startup-maintenance.json" "$pid" <<'PY'
import json,sys
with open(sys.argv[1],encoding='utf-8') as f: state=json.load(f)
if state.get('pid')!=int(sys.argv[2]) or state.get('phase')!='preparing-data': raise SystemExit(1)
PY
    then
      maintaining=1
      maintenance_pid=$pid
      if ((maintenance_active==0)); then printf 'Preparing Astral data; waiting for backup and migration. Press Ctrl-C to cancel.\n'; fi
    fi
  fi
  if ((maintaining==0)); then
    if ((maintenance_active==1)); then startup_deadline=$((SECONDS+45)); fi
    if ((SECONDS>=startup_deadline)); then break; fi
  fi
  maintenance_active=$maintaining
  if ((maintaining==0)) && [[ $pid =~ ^[1-9][0-9]*$ && -f $ready ]]; then
    if server_url=$(python3 - "$ready" "$pid" "$root/data/server" <<'PY'
import json,sys,urllib.parse
with open(sys.argv[1],encoding='utf-8') as f: ready=json.load(f)
if ready.get('pid')!=int(sys.argv[2]) or ready.get('data_directory')!=sys.argv[3]: raise SystemExit(1)
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
  # Never print the journal, credentials, ready-file contents or environment.
  systemctl --user show "$service" --property=ExecMainCode,ExecMainStatus,Result >&2 || true
  die "Server did not become ready; inspect journalctl --user -u $service"
fi
systemctl --user is-active --quiet "$service" || die 'Server exited during startup'
# Capability is explicit and belongs to this user's exact root/unit, never a
# guessed service name. The marker is outside migrated Server data snapshots.
python3 - "$stage/installation.json" "$root" "$unit" "$github_proxy" "$remote_set" "$allow_remote" "$command_path" <<'PYINSTALL'
import json,os,sys
temporary,root,unit,proxy,remote_set,remote=sys.argv[1:7]
value={'schema':1,'root':root,'data_directory':root+'/data/server','uid':os.getuid(),'service':'astral-server.service','unit_path':unit,'github_proxy':proxy}
if remote_set=='1': value['allow_remote_management']=remote=='true'
if len(sys.argv)>7: value['command_path']=sys.argv[7]
with open(temporary,'x',encoding='utf-8') as f:
    json.dump(value,f,indent=2)
    f.write('\n')
    f.flush()
    os.fsync(f.fileno())
os.replace(temporary,root+'/installation.json')
PYINSTALL
install_command_entry
# BEGIN initial Web account
ensure_web_account() {
  local configured status
  [[ -n $old_program ]] || complete=1
  if ! status=$("$root/current/bin/astral" auth status --data "$root/data/server"); then
    die 'the Server is installed, but Web account status could not be checked; inspect the service and rerun this installer'
  fi
  configured=$(python3 -c 'import json,sys; v=json.load(sys.stdin).get("configured");
if type(v) is not bool: raise SystemExit("Invalid Web account status")
print(str(v).lower())' <<< "$status") || die 'the Server returned invalid Web account status'
  # Program activation is committed before interactive account setup. An
  # interrupted setup must leave the installed service available for a retry.
  complete=1
  if [[ $configured == false ]]; then
    printf '\nSet up the Web sign-in account for this Server.\n'
    if [[ -n ${ASTRAL_ADMIN_USERNAME:-} && -n ${ASTRAL_ADMIN_PASSWORD_FILE:-} ]]; then
      "$root/current/bin/astral" auth setup --data "$root/data/server" || die 'Web account setup did not complete; the installed Server was retained. Rerun the installer to finish setup'
    else
      if ! { exec 8<>/dev/tty; } 2>/dev/null; then die 'Web account setup is incomplete: run the installer in a terminal, or set both ASTRAL_ADMIN_USERNAME and ASTRAL_ADMIN_PASSWORD_FILE, then rerun; the installed Server was retained'; fi
      if ! "$root/current/bin/astral" auth setup --data "$root/data/server" <&8 >&8 2>&8; then
        exec 8>&-
        die 'Web account setup did not complete; the installed Server was retained. Rerun the installer to finish setup'
      fi
      exec 8>&-
    fi
    status=$("$root/current/bin/astral" auth status --data "$root/data/server") || die 'Web account setup result could not be checked; the installed Server was retained'
    [[ $(python3 -c 'import json,sys; print(json.load(sys.stdin).get("configured") is True)' <<< "$status") == True ]] || die 'Web account setup is still incomplete; the installed Server was retained'
  fi
}
# END initial Web account
ensure_web_account
printf '\nAstral Server %s is ready: %s\n' "$version" "$server_url"
printf 'Service: systemctl --user status %s\nLogs: journalctl --user -u %s\n' "$service" "$service"
printf 'Data: %s/data\nCommand: %s\n' "$root" "$command_path"
printf 'Running in the background as %s. You can close this terminal; the enabled user service starts at boot and user lingering keeps it running after logout.\n' "$(id -un)"
if [[ $allow_remote == true ]]; then
  printf 'Open the Server IP or domain on its configured HTTP port and sign in with your Web account.\n'
else
  printf 'Remote Web access is disabled by the explicit service startup override; use local or SSH access.\n'
fi
printf 'For private SSH access: ssh -N -L 4545:127.0.0.1:4545 USER@VPS, then open http://127.0.0.1:4545.\n'
if [[ -n $public_url ]]; then printf 'Configured access address: %s (verify target connectivity; configure HTTPS on your proxy if needed).\n' "$public_url"; fi
