import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { mkdir, mkdtemp, readFile, readlink, realpath, rm, symlink, writeFile } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

const execute = promisify(execFile)
const installer = fileURLToPath(new URL('./install-server.sh', import.meta.url))
const source = await readFile(installer, 'utf8')
const installerEnvironment = { ...process.env }
delete installerEnvironment.ASTRAL_ALLOW_REMOTE_MANAGEMENT
const supported = { skip: process.platform === 'win32' }
const accepted = ['0.0.0', '0.1.0', '1.0.0', '1.13.0', '0.1.1024', '9007199254740991.0.0', '0.9007199254740991.0', '0.0.9007199254740991', '9007199254740991.9007199254740991.9007199254740991']
const rejected = ['', '1.2', '1.2.3.4', '01.2.3', '1.02.3', '1.2.03', '1.2.3-beta.1', '1.2.3+build', '-1.2.3', '1.-2.3', '1.2.-3', 'v1.2.3', '1.2.3\n', '1.2.3 ', '١.2.3', '9007199254740992.0.0', '0.9007199254740992.0', '0.0.9007199254740992', '18446744073709551616.0.0']

async function fixture(t) {
  const directory = await mkdtemp(path.join(tmpdir(), 'astral-installer-version-'))
  t.after(() => rm(directory, { recursive: true, force: true }))
  return directory
}

async function run(command, args, options = {}) {
  try { return { code: 0, ...await execute(command, args, { encoding: 'utf8', timeout: 5000, env: installerEnvironment, ...options }) } }
  catch (error) {
    if (typeof error.code !== 'number') throw error
    return { code: error.code, stdout: error.stdout, stderr: error.stderr }
  }
}

// Run the installer's actual embedded validators against tiny JSON files,
// without its network, executable startup or systemd installation paths.
function pythonBlock(marker) {
  const markerAt = source.indexOf(marker)
  assert.ok(markerAt >= 0, `Missing installer validator: ${marker}`)
  const start = source.indexOf("<<'PY'\n", markerAt)
  const end = source.indexOf('\nPY\n', start)
  assert.ok(start >= 0 && end > start, `Missing Python body: ${marker}`)
  return source.slice(start + "<<'PY'\n".length, end)
}
const releaseValidator = pythonBlock('tag=$(python3 - ')
const manifestValidator = pythonBlock('metadata=$(python3 - ')
const forwardFormatValidator = pythonBlock('python3 - "$old_formats" "$new_formats"')

function manifest(version) {
  const names = ['darwin', 'linux', 'windows'].flatMap(system => ['amd64', 'arm64'].map(arch => `astral-${system}-${arch}${system === 'windows' ? '.exe' : ''}`))
  return {
    schema_version: 1, purpose: 'release', public_release_ready: true,
    version, tag: `v${version}`, source_sha: 'a'.repeat(40),
    host_schema: 3, core_format: 11, data_formats: { host: 3, core: 11 },
    migration_status: 'goose', migration_baselines: { host: 3, core: 11 }, migration_targets: { host: 3, core: 11 },
    distribution_names: names,
    assets: [...names, 'checksums.txt', 'LICENSE.txt', 'THIRD_PARTY_NOTICES.txt'].map(name => ({ name, size: 1, sha256: 'b'.repeat(64) })),
  }
}

test('installer dry-run accepts ordinary numeric tags and rejects suffixes, leading zeroes and overflow', supported, async t => {
  const directory = await fixture(t)
  for (const version of accepted) {
    const result = await run('bash', [installer, '--tag', `v${version}`, '--root', path.join(directory, 'server'), '--dry-run'])
    assert.equal(result.code, 0, `${version}: ${result.stderr}`)
    assert.ok(result.stdout.includes(`release v${version}`))
  }
  for (const version of rejected) {
    const result = await run('bash', [installer, '--tag', `v${version}`, '--root', path.join(directory, 'server'), '--dry-run'])
    assert.notEqual(result.code, 0, `Accepted invalid tag v${version}`)
    assert.match(result.stderr, /vMAJOR\.MINOR\.PATCH/)
  }
  const latest = await run('bash', [installer, '--tag', 'latest', '--root', path.join(directory, 'server'), '--dry-run'])
  assert.equal(latest.code, 0, latest.stderr)
})

test('release API and manifest validators use the same version bounds as the command line', supported, async t => {
  const directory = await fixture(t)
  const releaseFile = path.join(directory, 'release.json')
  const manifestFile = path.join(directory, 'manifest.json')
  for (const version of [...accepted, ...rejected]) {
    await writeFile(releaseFile, JSON.stringify({ tag_name: `v${version}`, draft: false, prerelease: false }))
    await writeFile(manifestFile, JSON.stringify(manifest(version)))
    for (const [validator, file] of [[releaseValidator, releaseFile], [manifestValidator, manifestFile]]) {
      const result = await run('python3', ['-c', validator, file, `v${version}`])
      if (accepted.includes(version)) assert.equal(result.code, 0, `${version}: ${result.stderr}`)
      else assert.notEqual(result.code, 0, `Accepted invalid version ${version}`)
    }
  }
})

test('ordinary release versions retain published, stable-latest and public-ready gates', supported, async t => {
  const directory = await fixture(t)
  const file = path.join(directory, 'release.json')
  for (const release of [
    { tag_name: 'v0.1.0', draft: true, prerelease: false },
    { tag_name: 'v0.1.0', draft: false, prerelease: true },
  ]) {
    await writeFile(file, JSON.stringify(release))
    assert.notEqual((await run('python3', ['-c', releaseValidator, file, 'latest'])).code, 0)
  }
  for (const changes of [{ public_release_ready: false }, { purpose: 'unsigned-candidate' }]) {
    await writeFile(file, JSON.stringify({ ...manifest('0.1.0'), ...changes }))
    const result = await run('python3', ['-c', manifestValidator, file, 'v0.1.0'])
    assert.notEqual(result.code, 0)
    assert.match(result.stderr, /public_release_ready/)
  }
})

test('installer accepts the current Core baseline and rejects the previous baseline', supported, async t => {
  const directory = await fixture(t)
  const file = path.join(directory, 'manifest.json')
  const current = manifest('0.1.0')
  await writeFile(file, JSON.stringify(current))
  assert.equal((await run('python3', ['-c', manifestValidator, file, 'v0.1.0'])).code, 0)
  for (const value of [
    { ...current, migration_baselines: { host: 3, core: 10 } },
    { ...current, core_format: 10, data_formats: { host: 3, core: 10 }, migration_targets: { host: 3, core: 10 } },
  ]) {
    await writeFile(file, JSON.stringify(value))
    const result = await run('python3', ['-c', manifestValidator, file, 'v0.1.0'])
    assert.notEqual(result.code, 0)
    assert.match(result.stderr, /Invalid Goose migration ranges/)
  }
})

test('physical Core schema is validated separately from the logical format pair', supported, async t => {
  const directory = await fixture(t)
  const file = path.join(directory, 'manifest.json')
  const current = { ...manifest('0.1.7'), core_schema: 12, storage_migrations: { core_sqlite: { baseline: 11, target: 12 } } }
  await writeFile(file, JSON.stringify(current))
  const valid = await run('python3', ['-c', manifestValidator, file, 'v0.1.7'])
  assert.equal(valid.code, 0, valid.stderr)
  assert.match(valid.stdout, /3 11\n/)
  for (const changed of [{ ...current, core_schema: 11 }, { ...current, storage_migrations: {} }]) {
    await writeFile(file, JSON.stringify(changed))
    assert.notEqual((await run('python3', ['-c', manifestValidator, file, 'v0.1.7'])).code, 0)
  }
})

test('actual activation loop waits through long maintenance and rejects a restarted process', supported, async t => {
  const loop = source.slice(source.indexOf('startup_deadline=$((SECONDS+45))'), source.indexOf('if [[ -z $server_url ]]; then'))
  assert.ok(loop.includes('while :; do'))
  for (const mode of ['complete', 'restart', 'ordinary-timeout']) {
    const directory = path.join(await fixture(t), mode)
    await mkdir(path.join(directory, 'data/server'), { recursive: true })
    const script = `set -eu
root=$1
mode=$2
ready=$root/ready.json
service=fixture
server_url=
printf '0' > "$root/count"
die() { printf '%s\\n' "$*" >&2; exit 1; }
sleep() { SECONDS=$((SECONDS+1300)); }
systemctl() {
  if [[ $* == *--property=MainPID* ]]; then
    n=$(cat "$root/count"); n=$((n+1)); printf '%s' "$n" > "$root/count"
    if [[ $mode == restart && $n -gt 1 ]]; then printf '43'; return; fi
    if [[ $mode != ordinary-timeout ]]; then
      if ((n<3)); then printf '{"pid":42,"phase":"preparing-data"}' > "$root/data/server/startup-maintenance.json"
      else
        rm -f "$root/data/server/startup-maintenance.json"
        python3 -c 'import json,sys; json.dump({"pid":42,"url":"http://127.0.0.1:4545","data_directory":sys.argv[1]+"/data/server"},open(sys.argv[1]+"/ready.json","w"))' "$root"
      fi
    fi
    printf '42'
  fi
}
${loop}
[[ -n $server_url ]] || die 'ordinary startup timed out'
printf 'READY\\n'
`
    const result = await run('bash', ['-c', script, 'fixture', directory, mode])
    if (mode === 'complete') { assert.equal(result.code, 0, result.stderr); assert.match(result.stdout, /READY/) }
    else { assert.notEqual(result.code, 0); assert.match(result.stderr, mode === 'restart' ? /restarted/ : /timed out/) }
  }
})

test('forward-format preflight rejects private Core formats and downgrades before updating a service', supported, async () => {
  for (const [previous, next] of [['3 11', '3 11'], ['3 11', '3 12'], ['3 12', '4 14']]) {
    const result = await run('python3', ['-c', forwardFormatValidator, previous, next])
    assert.equal(result.code, 0, `${previous} → ${next}: ${result.stderr}`)
  }
  for (const [previous, next] of [['3 10', '3 11'], ['2 11', '3 11'], ['3 12', '3 11'], ['4 11', '3 11'], ['3', '3 11'], ['3 11 1', '3 11']]) {
    const result = await run('python3', ['-c', forwardFormatValidator, previous, next])
    assert.notEqual(result.code, 0, `Accepted unsupported forward formats ${previous} → ${next}`)
    assert.match(result.stderr, /outside the supported forward migration chain/)
  }
})

test('fresh HTTP deployment enables remote Web access without hostname restriction and accepts explicit opt-out', supported, async t => {
  const directory = await fixture(t)
  const root = path.join(directory, 'server')
  const args = [installer, '--root', root, '--dry-run']
  const defaults = await run('bash', args)
  assert.equal(defaults.code, 0, defaults.stderr)
  assert.match(defaults.stdout, /Listen: 0\.0\.0\.0:4545\nRemote management: true\nHostname restriction: false/)
  const remote = await run('bash', [...args, '--listen', '0.0.0.0:4545', '--public-url', 'http://100.80.1.2:4545', '--allow-remote-management'])
  assert.equal(remote.code, 0, remote.stderr)
  assert.match(remote.stdout, /Remote management: true/)
  const local = await run('bash', [...args, '--allow-remote-management=false'])
  assert.equal(local.code, 0, local.stderr)
  assert.match(local.stdout, /Remote management: false/)
  for (const extra of [['--tls-cert', '/tmp/cert'], ['--allow-remote-management=maybe'], ['--public-url', 'http://user:password@host:4545']]) {
    assert.notEqual((await run('bash', [...args, ...extra])).code, 0)
  }
})

test('reinstallation preserves listen and access addresses while legacy saved remote false no longer closes Web access', supported, async t => {
  const directory = await fixture(t)
  const root = path.join(directory, 'server')
  await mkdir(path.join(root, 'data/server'), { recursive: true })
  const settings = path.join(root, 'data/server/network.json')
  const original = JSON.stringify({ listen: '127.0.0.1:8888', public_urls: ['http://host:8888'], allow_remote_management: false })
  await writeFile(settings, original)
  const args = [installer, '--root', root, '--dry-run']
  const saved = await run('bash', args)
  assert.equal(saved.code, 0, saved.stderr)
  assert.match(saved.stdout, /Listen: 127\.0\.0\.1:8888\nRemote management: true/)
  const explicit = await run('bash', [...args, '--listen', '0.0.0.0:9999', '--allow-remote-management=true'])
  assert.equal(explicit.code, 0, explicit.stderr)
  assert.match(explicit.stdout, /Listen: 0\.0\.0\.0:9999\nRemote management: true/)
  assert.equal(await readFile(settings, 'utf8'), original, 'dry-run changed saved preferences')
})

test('download-only can obtain the new maintenance program before migrating an older installation', supported, async t => {
  const directory = await fixture(t)
  const root = path.join(directory, 'legacy-server')
  await mkdir(root)
  const old = JSON.stringify({ listen: '127.0.0.1:4545', tls_cert: '/old/cert', tls_key: '/old/key' })
  await writeFile(path.join(root, 'install-config.json'), old)
  const args = [installer, '--root', root, '--dry-run']
  assert.notEqual((await run('bash', args)).code, 0, 'ordinary update skipped required offline migration')
  const download = await run('bash', [...args, '--download-only', path.join(directory, 'distribution')])
  assert.equal(download.code, 0, download.stderr)
  assert.match(download.stdout, /Download and verify only/)
  assert.equal(await readFile(path.join(root, 'install-config.json'), 'utf8'), old)
})

function labelledPythonBlock(marker, label) {
  const at = source.indexOf(marker)
  const token = `<<'${label}'\n`
  const start = source.indexOf(token, at)
  const end = source.indexOf(`\n${label}\n`, start)
  assert.ok(at >= 0 && start >= 0 && end > start, `Missing installer block ${label}`)
  return source.slice(start + token.length, end)
}

test('managed installation records the exact user/root/unit outside Server migrations and starts with an explicit root flag', supported, async t => {
  const directory = await fixture(t)
  const root = path.join(directory, 'Server 中文 space $%')
  await mkdir(root)
  const unit = path.join(directory, 'astral-server.service')
  const generated = await run('python3', ['-c', pythonBlock('python3 - "$unit_temp" "$root"'), unit, root, 'https://proxy.example/path%20prefix/', '0', 'true'])
  assert.equal(generated.code, 0, generated.stderr)
  const definition = await readFile(unit, 'utf8')
  assert.ok(definition.includes('Environment="ASTRAL_GITHUB_PROXY=https://proxy.example/path%%20prefix/"\n'), 'The service does not retain the chosen GitHub download proxy')
  assert.ok(!definition.includes('ASTRAL_ALLOW_REMOTE_MANAGEMENT='), 'The default Server must not receive an explicit remote-management override')
  const quotedRoot = `"${root.replaceAll('\\', '\\\\').replaceAll('"', '\\"').replaceAll('%', '%%')}"`
  assert.ok(definition.includes(`"--installation-root" ${quotedRoot} "--data"`), 'The actual service does not carry its installation root')
  const metadata = labelledPythonBlock('python3 - "$stage/installation.json"', 'PYINSTALL')
  const temporary = path.join(directory, 'installation.pending')
  const result = await run('bash', ['-c', 'umask 077; exec python3 "$@"', 'fixture', '-c', metadata, temporary, root, unit, 'https://proxy.example/', '0', 'true'])
  assert.equal(result.code, 0, result.stderr)
  const marker = JSON.parse(await readFile(path.join(root, 'installation.json'), 'utf8'))
  assert.deepEqual(marker, { schema: 1, root, data_directory: `${root}/data/server`, uid: process.getuid(), service: 'astral-server.service', unit_path: unit, github_proxy: 'https://proxy.example/' })
})

test('explicit remote-management choices persist only as marker booleans and service startup environment', supported, async t => {
  const directory = await fixture(t)
  const root = path.join(directory, 'server')
  await mkdir(root)
  const unit = path.join(directory, 'astral-server.service')
  const unitWriter = pythonBlock('python3 - "$unit_temp" "$root"')
  const markerWriter = labelledPythonBlock('python3 - "$stage/installation.json"', 'PYINSTALL')
  for (const selected of [false, true]) {
    const value = String(selected)
    const generated = await run('python3', ['-c', unitWriter, unit, root, '', '1', value])
    assert.equal(generated.code, 0, generated.stderr)
    const definition = await readFile(unit, 'utf8')
    assert.ok(definition.includes(`Environment="ASTRAL_GITHUB_PROXY="\nEnvironment="ASTRAL_ALLOW_REMOTE_MANAGEMENT=${value}"\nExecStart=:`))
    const temporary = path.join(directory, 'installation.pending')
    const recorded = await run('python3', ['-c', markerWriter, temporary, root, unit, '', '1', value])
    assert.equal(recorded.code, 0, recorded.stderr)
    const marker = JSON.parse(await readFile(path.join(root, 'installation.json'), 'utf8'))
    assert.equal(marker.allow_remote_management, selected)
    const retained = await run('bash', [installer, '--root', root, '--dry-run'])
    assert.equal(retained.code, 0, retained.stderr)
    assert.ok(retained.stdout.includes(`Remote management: ${value}\n`))
    const environment = await run('bash', [installer, '--root', root, '--dry-run'], { env: { ...installerEnvironment, ASTRAL_ALLOW_REMOTE_MANAGEMENT: String(!selected) } })
    assert.equal(environment.code, 0, environment.stderr)
    assert.ok(environment.stdout.includes(`Remote management: ${!selected}\n`))
    const flag = await run('bash', [installer, '--root', root, '--dry-run', `--allow-remote-management=${value}`], { env: { ...installerEnvironment, ASTRAL_ALLOW_REMOTE_MANAGEMENT: String(!selected) } })
    assert.equal(flag.code, 0, flag.stderr)
    assert.ok(flag.stdout.includes(`Remote management: ${value}\n`))
  }
})

test('installer version preflight refuses application downgrade even when data formats match', supported, async () => {
  const validator = labelledPythonBlock('python3 - "$old_version" "$version"', 'PYVERSION')
  for (const [old, next] of [['0.1.0', '0.1.0'], ['0.1.0', '0.1.3'], ['0.1.9', '0.1.10']]) assert.equal((await run('python3', ['-c', validator, old, next])).code, 0)
  for (const [old, next] of [['0.1.3', '0.1.0'], ['0.2.0', '0.1.999'], ['dev', '0.1.0']]) assert.notEqual((await run('python3', ['-c', validator, old, next])).code, 0)
})

test('only a fresh install writes the new network defaults; reinstallation preserves explicit and legacy fields', supported, async t => {
  const directory = await fixture(t)
  const settings = path.join(directory, 'network.json')
  const pending = path.join(directory, 'pending.json')
  const writer = pythonBlock('python3 - "$stage/new-config.json"')
  const fresh = await run('python3', ['-c', writer, pending, settings, '0', '0.0.0.0:4545', '0', '', '1'])
  assert.equal(fresh.code, 0, fresh.stderr)
  assert.deepEqual(JSON.parse(await readFile(pending)), { restrict_hostnames: false })
  for (const value of [
    { allow_remote_management: false, restrict_hostnames: true, allowed_hostnames: ['astral.example.com'] },
    { public_urls: ['https://existing.example.com'], unknown_saved_field: 'retained' },
  ]) {
    await writeFile(settings, JSON.stringify(value))
    const saved = await run('python3', ['-c', writer, pending, settings, '0', '', '0', '', '0'])
    assert.equal(saved.code, 0, saved.stderr)
    assert.deepEqual(JSON.parse(await readFile(pending)), value)
  }
})

function installerSection(name) {
  const start = source.indexOf(`# BEGIN ${name}\n`)
  const end = source.indexOf(`# END ${name}\n`, start)
  assert.ok(start >= 0 && end > start)
  return source.slice(start, end)
}
const requiredCapabilities = installerSection('required Server capabilities')
const commandEntry = installerSection('managed command entry')
const accountSetup = installerSection('initial Web account')
const shellQuote = value => `'${value.replaceAll("'", "'\\''")}'`

test('installer rejects old CLI capabilities before any service or network-setting mutation', supported, async t => {
  const directory = await fixture(t)
  const program = path.join(directory, 'astral')
  await writeFile(program, `#!/bin/sh
case "$1 $2" in
  'auth status') [ "\${ASTRAL_TEST_OLD_CLI:-}" != auth ] && printf 'Usage of auth status:\\n' ;;
  'config hostnames') [ "\${ASTRAL_TEST_OLD_CLI:-}" != config ] && printf 'Usage of config hostnames:\\n' ;;
  'serve --help') [ "\${ASTRAL_TEST_OLD_CLI:-}" != serve ] && printf 'Usage of serve:\\n  -restrict-hostnames\\n' ;;
esac
exit 1
`, { mode: 0o700 })
  const script = `set -eu
new_program=$1
die() { printf '%s\\n' "$*" >&2; exit 1; }
${requiredCapabilities}
printf 'CAPABILITIES_ACCEPTED\\n'
`
  const accepted = await run('bash', ['-c', script, 'fixture', program])
  assert.equal(accepted.code, 0, accepted.stderr)
  assert.match(accepted.stdout, /CAPABILITIES_ACCEPTED/)
  for (const missing of ['auth', 'config', 'serve']) {
    const result = await run('bash', ['-c', script, 'fixture', program], { env: { ...process.env, ASTRAL_TEST_OLD_CLI: missing } })
    assert.notEqual(result.code, 0)
    assert.match(result.stderr, /use the installer included with that older release/)
    assert.doesNotMatch(result.stdout, /CAPABILITIES_ACCEPTED/)
  }
  assert.ok(source.indexOf('# BEGIN required Server capabilities') < source.indexOf('mutating=1\n'), 'Capability validation must precede stopping the old service and replacing settings')
})

async function commandFixture(t) {
  const directory = await fixture(t)
  const commands = path.join(directory, 'commands')
  const root = path.join(directory, 'Server 中文 space $%')
  await mkdir(commands)
  await mkdir(path.join(root, 'releases/first/bin'), { recursive: true })
  await mkdir(path.join(root, 'releases/second/bin'), { recursive: true })
  for (const [cohort, version] of [['first', '0.1.9'], ['second', '0.1.10']]) {
    await writeFile(path.join(root, `releases/${cohort}/bin/astral`), `#!/bin/sh\n[ "$1" = version ] || exit 64\nprintf '%s\\n' '${version}'\n`, { mode: 0o700 })
  }
  await symlink(path.join(root, 'releases/first'), path.join(root, 'current'))
  return { directory, commands, root, env: { ...process.env, PATH: `${commands}:/usr/bin:/bin` } }
}

test('command registration is usable on the unchanged PATH and follows current across upgrades', supported, async t => {
  const f = await commandFixture(t)
  const registration = `set -eu
root=$1; version=$2; command_path=; command_needs_sudo=0; command_created=0
die() { printf '%s\\n' "$*" >&2; exit 1; }
${commandEntry}
select_command_entry
install_command_entry
`
  let result = await run('bash', ['-c', registration, 'fixture', f.root, '0.1.9'], { env: f.env })
  assert.equal(result.code, 0, result.stderr)
  const entry = path.join(f.commands, 'astral')
  assert.equal(await readlink(entry), path.join(f.root, 'current/bin/astral'))
  let terminal = await run('bash', ['--noprofile', '--norc', '-c', 'astral version'], { env: f.env })
  assert.equal(terminal.stdout.trim(), '0.1.9')
  await rm(path.join(f.root, 'current'))
  await symlink(path.join(f.root, 'releases/second'), path.join(f.root, 'current'))
  result = await run('bash', ['-c', registration, 'fixture', f.root, '0.1.10'], { env: f.env })
  assert.equal(result.code, 0, result.stderr)
  terminal = await run('bash', ['--noprofile', '--norc', '-c', 'astral version'], { env: f.env })
  assert.equal(terminal.stdout.trim(), '0.1.10')
})

test('command registration refuses an unrelated command instead of shadowing or replacing it', supported, async t => {
  const f = await commandFixture(t)
  const entry = path.join(f.commands, 'astral')
  const original = '#!/bin/sh\nprintf unrelated\\n\n'
  await writeFile(entry, original, { mode: 0o700 })
  const result = await run('bash', ['-c', `root=$1; command_path=; command_needs_sudo=0
die() { printf '%s\\n' "$*" >&2; exit 1; }
${commandEntry}
select_command_entry`, 'fixture', f.root], { env: f.env })
  assert.notEqual(result.code, 0)
  assert.match(result.stderr, /unrelated astral command/)
  assert.equal(await readFile(entry, 'utf8'), original)
})

test('a non-writable PATH selects an existing system command directory and checks sudo before installation', supported, async t => {
  const f = await commandFixture(t)
  await writeFile(path.join(f.commands, 'sudo'), '#!/bin/sh\n[ "$1" = -n ] && [ "$2" = true ]\n', { mode: 0o700 })
  const result = await run('bash', ['-c', `root=$1; command_path=; command_needs_sudo=0
die() { printf '%s\\n' "$*" >&2; exit 1; }
${commandEntry}
command_directory_writable() { return 1; }
select_command_entry
printf 'SELECTED=%s SUDO=%s\\n' "$command_path" "$command_needs_sudo"`, 'fixture', f.root], { env: { ...f.env, PATH: `${f.commands}:/usr/local/bin:/usr/bin:/bin` } })
  assert.equal(result.code, 0, result.stderr)
  assert.match(result.stdout, /SELECTED=\/usr\/local\/bin\/astral SUDO=1/)
})

async function accountFixture(t, configured = false) {
  const directory = await fixture(t)
  const root = path.join(directory, 'server')
  await mkdir(path.join(root, 'current/bin'), { recursive: true })
  await writeFile(path.join(root, 'status.json'), JSON.stringify({ configured }))
  await writeFile(path.join(root, 'password'), 'fixture-password-not-printed\n', { mode: 0o600 })
  await writeFile(path.join(root, 'current/bin/astral'), `#!/usr/bin/env bash
set -eu
root=${shellQuote(root)}
[[ $1 == auth && $3 == --data && $4 == "$root/data/server" ]] || exit 64
printf '%s\\n' "$2" >> "$root/calls"
case $2 in
  status) cat "$root/status.json" ;;
  setup)
    if [[ \${ASTRAL_AUTH_FIXTURE_MODE:-} == tty ]]; then
      [[ -t 0 && -t 1 ]] || exit 65
      printf 'Fixture account name: '
      read -r account
      [[ $account == fixture-user ]] || exit 66
    else
      [[ \${ASTRAL_ADMIN_USERNAME:-} == fixture-user && -f \${ASTRAL_ADMIN_PASSWORD_FILE:-} ]] || exit 67
    fi
    printf '{"configured":true}' > "$root/status.json" ;;
  *) exit 68 ;;
esac
`, { mode: 0o700 })
  const script = `set -eu
root=$1; old_program=; complete=0
die() { printf '%s\\n' "$*" >&2; printf '%s' "$complete" > "$root/complete"; exit 1; }
${accountSetup}
ensure_web_account
printf 'READY\\n'
`
  return { root, script, env: { ...process.env, ASTRAL_ADMIN_USERNAME: '', ASTRAL_ADMIN_PASSWORD_FILE: '' } }
}

test('unattended first setup uses the installed data directory and normal upgrades do not ask again', supported, async t => {
  const f = await accountFixture(t)
  const env = { ...f.env, ASTRAL_ADMIN_USERNAME: 'fixture-user', ASTRAL_ADMIN_PASSWORD_FILE: path.join(f.root, 'password') }
  const first = await run('bash', ['-c', f.script, 'fixture', f.root], { env, detached: true })
  assert.equal(first.code, 0, first.stderr)
  assert.match(first.stdout, /READY/)
  assert.equal(await readFile(path.join(f.root, 'calls'), 'utf8'), 'status\nsetup\nstatus\n')
  const upgrade = await run('bash', ['-c', f.script, 'fixture', f.root], { env: f.env, detached: true })
  assert.equal(upgrade.code, 0, upgrade.stderr)
  assert.equal(await readFile(path.join(f.root, 'calls'), 'utf8'), 'status\nsetup\nstatus\nstatus\n')
})

test('without terminal or unattended credentials setup is incomplete and never reports success', supported, async t => {
  const f = await accountFixture(t)
  const result = await run('bash', ['-c', f.script, 'fixture', f.root], { env: f.env, detached: true })
  assert.notEqual(result.code, 0)
  assert.match(result.stderr, /Web account setup is incomplete/)
  assert.doesNotMatch(result.stdout, /READY/)
  assert.equal(await readFile(path.join(f.root, 'complete'), 'utf8'), '1', 'The installed service must remain available to finish setup')
  assert.deepEqual(JSON.parse(await readFile(path.join(f.root, 'status.json'))), { configured: false })
})

test('piped installation reads account interaction from the controlling terminal', supported, async t => {
  const f = await accountFixture(t)
  const driver = `import os,pty,select,subprocess,sys,time
pid,fd=pty.fork()
if pid==0:
    result=subprocess.run(['bash','-s','--',sys.argv[1]],input=sys.argv[2],text=True)
    os._exit(result.returncode)
output=b''; sent=False; deadline=time.monotonic()+4
while time.monotonic()<deadline:
    if select.select([fd],[],[],0.1)[0]:
        try: chunk=os.read(fd,65536)
        except OSError: break
        if not chunk: break
        output+=chunk
        if b'Fixture account name:' in output and not sent:
            os.write(fd,b'fixture-user\\n'); sent=True
done,status=os.waitpid(pid,0)
sys.stdout.write(output.decode())
sys.exit(os.waitstatus_to_exitcode(status))
`
  const result = await run('python3', ['-c', driver, f.root, f.script], { env: { ...f.env, ASTRAL_AUTH_FIXTURE_MODE: 'tty' } })
  assert.equal(result.code, 0, result.stderr)
  assert.match(result.stdout, /READY/)
  assert.equal(await readFile(path.join(f.root, 'calls'), 'utf8'), 'status\nsetup\nstatus\n')
})

test('GitHub proxy is explicit, validates HTTPS prefixes and can override or disable environment configuration', supported, async t => {
  const directory = await fixture(t)
  const args = [installer, '--root', path.join(directory, 'server'), '--dry-run']
  const selected = await run('bash', [...args, '--github-proxy', 'https://gh-proxy.org'], { env: { ...process.env, ASTRAL_GITHUB_PROXY: 'https://other.example/' } })
  assert.equal(selected.code, 0, selected.stderr)
  assert.match(selected.stdout, /GitHub downloads: https:\/\/gh-proxy\.org\//)
  const direct = await run('bash', [...args, '--github-proxy', ''], { env: { ...process.env, ASTRAL_GITHUB_PROXY: 'https://other.example/' } })
  assert.equal(direct.code, 0, direct.stderr)
  assert.match(direct.stdout, /GitHub downloads: direct/)
  for (const invalid of ['http://proxy.example/', 'https://name:password@proxy.example/', 'https://proxy.example/?token=x', 'https://proxy.example/#fragment', 'https://proxy.example/?', 'https://proxy.example/#', 'https://proxy.example/ bad', 'https://proxy.example/\\path', 'https://proxy.example:0/', 'https://proxy.example:70000/']) {
    const result = await run('bash', [...args, '--github-proxy', invalid])
    assert.notEqual(result.code, 0)
    assert.match(result.stderr, /GitHub proxy must be an HTTPS prefix/)
  }
})

test('repeat installation retains the selected GitHub proxy unless an option or defined environment replaces it', supported, async t => {
  const directory = await fixture(t)
  const root = path.join(directory, 'server')
  await mkdir(root)
  await writeFile(path.join(root, 'installation.json'), JSON.stringify({ github_proxy: 'https://saved.example/prefix/' }))
  const env = { ...process.env }
  delete env.ASTRAL_GITHUB_PROXY
  const args = [installer, '--root', root, '--dry-run']
  const retained = await run('bash', args, { env })
  assert.equal(retained.code, 0, retained.stderr)
  assert.match(retained.stdout, /GitHub downloads: https:\/\/saved\.example\/prefix\//)
  const direct = await run('bash', args, { env: { ...env, ASTRAL_GITHUB_PROXY: '' } })
  assert.equal(direct.code, 0, direct.stderr)
  assert.match(direct.stdout, /GitHub downloads: direct/)
  const explicit = await run('bash', [...args, '--github-proxy=https://option.example'], { env: { ...env, ASTRAL_GITHUB_PROXY: 'https://environment.example/' } })
  assert.equal(explicit.code, 0, explicit.stderr)
  assert.match(explicit.stdout, /GitHub downloads: https:\/\/option\.example\//)
})

test('all Server release downloads use the selected proxy and retain checksum rejection', supported, async t => {
  const directory = await fixture(t)
  const commands = path.join(directory, 'commands')
  const payload = path.join(directory, 'payload')
  await mkdir(commands); await mkdir(payload)
  const value = manifest('0.1.9')
  const sha = data => createHash('sha256').update(data).digest('hex')
  const files = new Map(value.distribution_names.map(name => [name, Buffer.from('Do not execute fixture '+name)]))
  files.set('LICENSE.txt', Buffer.from('fixture license'))
  files.set('THIRD_PARTY_NOTICES.txt', Buffer.from('fixture notices'))
  files.set('checksums.txt', Buffer.from([...files].filter(([name]) => value.distribution_names.includes(name)).map(([name, data]) => `${sha(data)}  ${name}\n`).join('')))
  value.assets = [...files].map(([name, data]) => ({ name, size: data.length, sha256: sha(data) }))
  files.set('release-manifest.json', Buffer.from(JSON.stringify(value)))
  files.set('SHA256SUMS', Buffer.from([...files].map(([name, data]) => `${sha(data)}  ${name}\n`).join('')))
  files.set('release.json', Buffer.from(JSON.stringify({ tag_name: 'v0.1.9', draft: false, prerelease: false })))
  for (const [name, data] of files) await writeFile(path.join(payload, name), data)
  await writeFile(path.join(commands, 'uname'), '#!/bin/sh\ncase $1 in -s) echo Linux;; -m) echo x86_64;; *) exit 64;; esac\n', { mode: 0o700 })
  await writeFile(path.join(commands, 'sha256sum'), '#!/usr/bin/env python3\nimport hashlib,pathlib,sys\nprint(hashlib.sha256(pathlib.Path(sys.argv[1]).read_bytes()).hexdigest()+"  "+sys.argv[1])\n', { mode: 0o700 })
  await writeFile(path.join(commands, 'curl'), `#!/usr/bin/env python3
import pathlib,sys,shutil,os
args=sys.argv[1:]; url=next(v for v in args if v.startswith('https://'))
prefix='https://gh-proxy.org/'
if not url.startswith(prefix): raise SystemExit('A GitHub request bypassed the selected proxy')
original=url[len(prefix):]
if original=='https://api.github.com/repos/oines/astral/releases/latest': name='release.json'
elif original.startswith('https://github.com/oines/astral/releases/download/v0.1.9/'):
    name=original.rsplit('/',1)[1]
else: raise SystemExit('Unexpected GitHub source')
with open(os.environ['ASTRAL_TEST_REQUEST_LOG'],'a') as f: f.write(original+'\\n')
shutil.copyfile(pathlib.Path(os.environ['ASTRAL_TEST_PAYLOAD'])/name,args[args.index('-o')+1])
`, { mode: 0o700 })
  const log = path.join(directory, 'requests')
  const env = { ...process.env, PATH: `${commands}:${process.env.PATH}`, ASTRAL_GITHUB_PROXY: 'https://gh-proxy.org/', ASTRAL_TEST_REQUEST_LOG: log, ASTRAL_TEST_PAYLOAD: payload }
  const destination = path.join(directory, 'downloaded')
  const result = await run('bash', [installer, '--root', path.join(directory, 'server'), '--download-only', destination], { env })
  assert.equal(result.code, 0, result.stderr)
  const requests = (await readFile(log, 'utf8')).trim().split('\n')
  assert.equal(requests.length, 12)
  assert.ok(requests.includes('https://api.github.com/repos/oines/astral/releases/latest'))
  assert.ok(requests.includes('https://github.com/oines/astral/releases/download/v0.1.9/release-manifest.json'))
  assert.deepEqual(await readFile(path.join(destination, 'astral-linux-amd64')), files.get('astral-linux-amd64'))
  await writeFile(path.join(payload, 'astral-linux-amd64'), 'tampered')
  const rejected = await run('bash', [installer, '--root', path.join(directory, 'server'), '--download-only', path.join(directory, 'rejected')], { env })
  assert.notEqual(rejected.code, 0)
  assert.match(rejected.stderr, /checksum mismatch/)
})


const userServicePreparation = installerSection('user service preparation')

test('user-service preparation enables lingering and starts only a missing manager for root or a sudo account', supported, async t => {
  for (const uid of ['0', '501']) {
    const directory = await fixture(t)
    const result = await run('bash', ['-c', `set -euo pipefail
state=$1; test_uid=$2
id() { if [[ $1 == -u ]]; then echo "$test_uid"; else echo test-account; fi; }
loginctl() {
  if [[ $1 == show-user ]]; then [[ -f $state/linger ]] && echo yes || echo no
  elif [[ $1 == --no-ask-password ]]; then return 1
  else echo "linger $*" >> "$state/actions"; touch "$state/linger"; fi
}
systemctl() {
  if [[ $1 == --user ]]; then [[ -f $state/manager ]]
  elif [[ $1 == is-active ]]; then [[ -f $state/manager ]]
  elif [[ $1 == start ]]; then echo "manager $*" >> "$state/actions"; touch "$state/manager"
  else return 64; fi
}
sudo() { echo "sudo $*" >> "$state/actions"; if [[ $1 == -n ]]; then shift; fi; "$@"; }
die() { echo "$*" >&2; exit 1; }
${userServicePreparation}
prepare_user_service
printf 'RUNTIME=%s DBUS=%s\\n' "$XDG_RUNTIME_DIR" "$DBUS_SESSION_BUS_ADDRESS"
`, 'fixture', directory, uid])
    assert.equal(result.code, 0, result.stderr)
    const actions = await readFile(path.join(directory, 'actions'), 'utf8')
    assert.match(actions, new RegExp(`linger enable-linger ${uid}`))
    assert.match(actions, new RegExp(`manager start user@${uid}\\.service`))
    assert.ok(!actions.includes('restart'))
    assert.equal(actions.includes('sudo'), uid !== '0')
    assert.ok(result.stdout.includes(`RUNTIME=/run/user/${uid} DBUS=unix:path=/run/user/${uid}/bus`))
  }
})

test('prepared accounts reuse their manager and privilege denial stops before installation changes', supported, async t => {
  for (const prepared of [true, false]) {
    const directory = await fixture(t)
    const result = await run('bash', ['-c', `set -euo pipefail
state=$1; prepared=$2
id() { if [[ $1 == -u ]]; then echo 501; else echo test-account; fi; }
loginctl() { if [[ $1 == show-user ]]; then echo "$prepared"; else return 1; fi; }
systemctl() { [[ $prepared == yes ]]; }
sudo() { echo denied >> "$state/actions"; return 1; }
die() { echo "$*" >&2; exit 1; }
${userServicePreparation}
prepare_user_service
echo installed > "$state/installation-mutation"
`, 'fixture', directory, prepared ? 'yes' : 'no'], { detached: true })
    if (prepared) {
      assert.equal(result.code, 0, result.stderr)
      await assert.rejects(readFile(path.join(directory, 'actions')), { code: 'ENOENT' })
    } else {
      assert.notEqual(result.code, 0)
      assert.match(result.stderr, /requires sudo authentication/)
      await assert.rejects(readFile(path.join(directory, 'installation-mutation')), { code: 'ENOENT' })
    }
  }
})

const uninstallProgram = labelledPythonBlock('python3 - "$root" "$unit" "$dry_run"', 'PYUNINSTALL')
async function uninstallFixture(t) {
  const directory = await realpath(await fixture(t))
  const root = path.join(directory, 'server space 中文')
  const units = path.join(directory, 'systemd/user')
  const commands = path.join(directory, 'commands')
  const cohort = path.join(root, 'releases/v0.1.9-aaaaaaaaaaaaaaaa')
  const previous = path.join(root, 'releases/v0.1.8-bbbbbbbbbbbbbbbb')
  const unit = path.join(units, 'astral-server.service')
  const entry = path.join(commands, 'astral')
  await mkdir(path.join(root, 'data/server'), { recursive: true })
  await mkdir(path.join(root, 'data/computers'), { recursive: true })
  await mkdir(units, { recursive: true }); await mkdir(commands)
  for (const release of [cohort, previous]) {
    await mkdir(path.join(release, 'distribution'), { recursive: true })
    await mkdir(path.join(release, 'bin'))
    const body = '#!/bin/sh\necho recovery-program\n'
    await writeFile(path.join(release, 'distribution/astral-linux-arm64'), body, { mode: 0o700 })
    await writeFile(path.join(release, 'distribution/release-manifest.json'), JSON.stringify({ assets: [{ name: 'astral-linux-arm64', sha256: createHash('sha256').update(body).digest('hex') }] }))
    await symlink('../distribution/astral-linux-arm64', path.join(release, 'bin/astral'))
  }
  await symlink(cohort, path.join(root, 'current'))
  await symlink(previous, path.join(root, 'data-program'))
  await symlink(`${root}/current/bin/astral`, entry)
  await writeFile(path.join(root, 'data/server/account-and-identity'), 'retained-account-and-server-identity')
  const unitResult = await run('python3', ['-c', pythonBlock('python3 - "$unit_temp" "$root"'), unit, root, '', '0', 'true'])
  assert.equal(unitResult.code, 0, unitResult.stderr)
  const recorded = await run('python3', ['-c', labelledPythonBlock('python3 - "$stage/installation.json"', 'PYINSTALL'), path.join(root, 'pending'), root, unit, '', '0', 'true', entry])
  assert.equal(recorded.code, 0, recorded.stderr)
  const state = path.join(directory, 'state.json')
  await writeFile(state, JSON.stringify({ Id: 'astral-server.service', LoadState: 'loaded', ActiveState: 'active', MainPID: '999999999', FragmentPath: unit, DropInPaths: '', enabled: true }))
  await writeFile(path.join(commands, 'systemctl'), `#!/usr/bin/env python3
import json,os,sys
p=os.environ['ASTRAL_UNINSTALL_TEST_STATE']; args=sys.argv[1:]
with open(p) as f: s=json.load(f)
with open(p+'.actions','a') as f: f.write(' '.join(args)+'\\n')
op=args[1]
if op=='show':
 for k,v in s.items():
  if k!='enabled': print(k+'='+str(v))
elif op=='stop':
 if os.environ.get('ASTRAL_UNINSTALL_STOP_FAILURE'): sys.exit(1)
 s['ActiveState']='inactive'; s['MainPID']='0'
elif op=='disable': s['enabled']=False
elif op=='is-enabled': sys.exit(0 if s['enabled'] else 1)
elif op!='daemon-reload': sys.exit(64)
with open(p,'w') as f: json.dump(s,f)
`, { mode: 0o700 })
  const env = { ...installerEnvironment, PATH: `${commands}:${process.env.PATH}`, XDG_CONFIG_HOME: path.join(directory, 'config'), ASTRAL_UNINSTALL_TEST_STATE: state }
  return { directory, root, cohort, previous, unit, entry, units, state, env, run: (dry = false, extra = {}) => run('python3', ['-c', uninstallProgram, root, unit, dry ? '1' : '0', entry], { env: { ...env, ...extra } }) }
}

test('uninstall removes the owned service and entry, retains account and latest recovery cohort, and is repeatable', supported, async t => {
  const f = await uninstallFixture(t)
  const result = await f.run()
  assert.equal(result.code, 0, result.stderr)
  assert.match(result.stdout, /stopped, disabled and uninstalled/)
  for (const removed of [f.unit, f.entry, path.join(f.root, 'current'), f.previous]) await assert.rejects(readFile(removed), { code: 'ENOENT' })
  assert.equal(await readlink(path.join(f.root, 'data-program')), f.cohort)
  assert.equal(await readFile(path.join(f.root, 'data/server/account-and-identity'), 'utf8'), 'retained-account-and-server-identity')
  assert.equal(JSON.parse(await readFile(path.join(f.root, 'installation.json'))).uninstalled, true)
  const again = await f.run()
  assert.equal(again.code, 0, again.stderr)
  assert.match(again.stdout, /already uninstalled/)
})

test('uninstall dry-run performs no service mutation and shared commands remain usable', supported, async t => {
  const f = await uninstallFixture(t)
  const preview = await f.run(true)
  assert.equal(preview.code, 0, preview.stderr)
  assert.match(preview.stdout, /No changes made/)
  const actions = await readFile(`${f.state}.actions`, 'utf8')
  assert.ok(!/stop|disable|daemon-reload/.test(actions))
  await writeFile(path.join(f.units, 'other-daemon.service'), `[Service]\nExecStart=${f.entry} computer run\n`)
  const result = await f.run()
  assert.equal(result.code, 0, result.stderr)
  assert.match(result.stdout, /shared with another/)
  assert.equal(await readlink(f.entry), `${f.root}/current/bin/astral`)
  assert.equal(await readlink(path.join(f.root, 'current')), f.cohort)
})

test('uninstall refuses changed units, overrides, foreign entries and a Server that cannot stop', supported, async t => {
  for (const mode of ['unit', 'override', 'entry', 'stop']) {
    const f = await uninstallFixture(t)
    if (mode === 'unit') await writeFile(f.unit, `${await readFile(f.unit, 'utf8')}# user change\n`)
    if (mode === 'override') {
      const state = JSON.parse(await readFile(f.state)); state.DropInPaths = '/custom/override.conf'; await writeFile(f.state, JSON.stringify(state))
    }
    if (mode === 'entry') { await rm(f.entry); await writeFile(f.entry, 'unrelated command') }
    const result = await f.run(false, mode === 'stop' ? { ASTRAL_UNINSTALL_STOP_FAILURE: '1' } : {})
    assert.notEqual(result.code, 0, mode)
    await readFile(f.unit)
    assert.equal(await readlink(path.join(f.root, 'current')), f.cohort)
    assert.equal(JSON.parse(await readFile(path.join(f.root, 'installation.json'))).uninstalled, undefined)
    const actions = await readFile(`${f.state}.actions`, 'utf8').catch(() => '')
    assert.ok(!actions.includes('disable'), mode)
  }
})

test('a registered Computer using its own daemon still retains the shared Server terminal command', supported, async t => {
  const f = await uninstallFixture(t)
  const computer = path.join(f.directory, 'separate-computer')
  const index = path.join(f.env.XDG_CONFIG_HOME, 'astral/installations/computers')
  await mkdir(path.join(computer, 'service'), { recursive: true })
  await mkdir(index, { recursive: true })
  await writeFile(path.join(index, 'registered.json'), JSON.stringify({ root: computer }))
  await writeFile(path.join(computer, 'credentials.json'), JSON.stringify({ computer_id: 'independent-computer' }))
  await writeFile(path.join(computer, 'service/spec.json'), JSON.stringify({ program: '/separate/daemon/astral' }))
  const result = await f.run()
  assert.equal(result.code, 0, result.stderr)
  assert.equal(await readlink(f.entry), `${f.root}/current/bin/astral`)
  assert.equal(await readlink(path.join(f.root, 'current')), f.cohort)
  await assert.rejects(readFile(f.unit), { code: 'ENOENT' })
})
