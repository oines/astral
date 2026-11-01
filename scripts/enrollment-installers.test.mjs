import test from 'node:test'
import assert from 'node:assert/strict'
import { promises as fs } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { createServer } from 'node:http'
import { createHash } from 'node:crypto'

const exec = promisify(execFile)
const directEnvironment = { ...process.env, ASTRAL_GITHUB_PROXY: '' }

test('real root Daemon acceptance refuses a developer account before commands or installation', async () => {
  const environment = { ...process.env, CI: '', GITHUB_ACTIONS: '', RUNNER_TEMP: '' }
  await assert.rejects(exec(process.execPath, ['scripts/check-daemon-update.mjs', '--root-user'], { env: environment, timeout: 10_000 }), error => {
    assert.match(error.stderr, /requires a disposable GitHub Actions user; never run on a developer account/)
    return true
  })
})

test('Windows PowerShell hashes installer assets without module discovery or Get-FileHash', { skip: process.platform !== 'win32', timeout: 30000 }, async t => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'astral-powershell-hash-test-'))
  t.after(() => fs.rm(root, { recursive: true, force: true }))
  const content = Buffer.from([0, 255, 10, 13, 128, 1, 254, 42])
  const file = path.join(root, 'release asset.exe')
  const empty = path.join(root, 'empty asset.exe')
  await fs.writeFile(file, content)
  await fs.writeFile(empty, '')
  const script = path.join(root, 'hash.ps1')
  await fs.writeFile(script, `param([string]$Installer, [string]$File, [string]$Empty)
$ErrorActionPreference = 'Stop'
# The installer still uses these built-in cmdlets. Resolve each one with module
# discovery disabled so this regression does not simply move to the next step.
foreach ($name in @('Add-Type', 'Out-Null', 'Invoke-WebRequest', 'Join-Path', 'Where-Object', 'Test-Path', 'Move-Item', 'Remove-Item')) {
  if (-not $ExecutionContext.InvokeCommand.GetCommand($name, [Management.Automation.CommandTypes]::Cmdlet)) {
    throw "The installer requires an unavailable built-in cmdlet: $name"
  }
}
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($Installer, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw 'The installer has PowerShell syntax errors.' }
$hashFunction = $ast.Find({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Get-FileSHA256' }, $true)
if (-not $hashFunction) { throw 'The installer hash function is missing.' }
. ([scriptblock]::Create($hashFunction.Extent.Text))
function Get-FileHash { throw 'Get-FileHash is unavailable in this environment.' }
[Console]::WriteLine((Get-FileSHA256 $File))
[Console]::WriteLine((Get-FileSHA256 $Empty))
`)
  const environment = Object.fromEntries(Object.entries(process.env).filter(([key]) => key.toUpperCase() !== 'PSMODULEPATH'))
  const { stdout } = await exec('powershell.exe', [
    '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', script,
    '-Installer', path.resolve('internal/platform/installers/install.ps1'), '-File', file, '-Empty', empty,
  ], { env: { ...environment, PSModulePath: '' }, timeout: 25000 })
  assert.deepEqual(stdout.trim().split(/\r?\n/), [
    createHash('sha256').update(content).digest('hex'),
    createHash('sha256').update('').digest('hex'),
  ])
})

test('Shell enrollment checks the selected HTTP address before installing or downloading', { skip: process.platform === 'win32' }, async t => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'astral-installer-test-'))
  t.after(() => fs.rm(root, { recursive: true, force: true }))
  const bin = path.join(root, 'bin')
  await fs.mkdir(bin)
  const log = path.join(root, 'curl.log')
  await fs.writeFile(path.join(bin, 'curl'), '#!/bin/sh\nprintf "%s\\n" "$*" > "$ASTRAL_TEST_CURL_LOG"\nexit 7\n', { mode: 0o755 })
  const installation = path.join(root, 'data')
  await assert.rejects(exec('sh', ['internal/platform/installers/install.sh', '--server', 'http://server.tailnet.ts.net:4545', '--server-pin', 'a'.repeat(64), '--enrollment', 'id', '--token', 'token'], {
    env: { ...directEnvironment, PATH: `${bin}:/usr/bin:/bin:/usr/sbin:/sbin`, XDG_DATA_HOME: installation, ASTRAL_TEST_CURL_LOG: log },
  }), error => {
    assert.match(error.stderr, /Cannot reach Astral at http:\/\/server.tailnet.ts.net:4545/)
    assert.doesNotMatch(error.stderr, /requires HTTPS/)
    return true
  })
  const request = await fs.readFile(log, 'utf8')
  assert.match(request, /http:\/\/server.tailnet.ts.net:4545\/health/)
  assert.doesNotMatch(request, /downloads/)
  await assert.rejects(fs.access(installation))
})

test('Shell update downloads the official release without a Server URL, pin, or health preflight', { skip: process.platform === 'win32' }, async t => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'astral-update-installer-test-'))
  t.after(() => fs.rm(root, { recursive: true, force: true }))
  const bin = path.join(root, 'bin')
  await fs.mkdir(bin)
  const log = path.join(root, 'curl.log')
  await fs.writeFile(path.join(bin, 'curl'), '#!/bin/sh\nprintf "%s\\n" "$*" > "$ASTRAL_TEST_CURL_LOG"\nexit 7\n', { mode: 0o755 })
  await assert.rejects(exec('sh', ['internal/platform/installers/install.sh', '--update', '--root', path.join(root, 'existing-computer')], {
    env: { ...directEnvironment, PATH: `${bin}:/usr/bin:/bin:/usr/sbin:/sbin`, XDG_DATA_HOME: path.join(root, 'data'), ASTRAL_TEST_CURL_LOG: log },
  }), error => {
    assert.equal(error.code, 7)
    assert.doesNotMatch(error.stderr, /Missing Astral Server|public-key pin|Cannot reach Astral/)
    return true
  })
  const request = await fs.readFile(log, 'utf8')
  assert.match(request, /https:\/\/github\.com\/oines\/astral\/releases\/latest\/download\/astral-/)
  assert.match(request, /--location --proto =https --proto-redir =https/)
  assert.doesNotMatch(request, /\/health|\/downloads\//)
})

async function shellProxyFixture(t) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'astral-enrollment-proxy-'))
  t.after(() => fs.rm(root, { recursive: true, force: true }))
  const bin = path.join(root, 'commands')
  const payload = path.join(root, 'payload')
  await fs.mkdir(bin)
  await fs.mkdir(payload)
  await fs.symlink(process.execPath, path.join(bin, 'node'))
  const binaryName = `astral-${process.platform === 'darwin' ? 'darwin' : 'linux'}-${process.arch === 'x64' ? 'amd64' : process.arch}`
  const binary = Buffer.from('#!/bin/sh\nif [ "$1" = version ]; then printf "0.1.9\\n"; exit; fi\nprintf "%s\\n" "$@" > "$ASTRAL_TEST_EXEC_LOG"\nprintf "%s" "${ASTRAL_GITHUB_PROXY:-}" > "$ASTRAL_TEST_PROXY_LOG"\nif [ "${ASTRAL_TEST_CONNECT_FAIL:-}" = 1 ]; then exit 77; fi\n')
  await fs.writeFile(path.join(payload, binaryName), binary)
  await fs.writeFile(path.join(payload, 'checksums.txt'), `${createHash('sha256').update(binary).digest('hex')}  ${binaryName}\n`)
  const log = path.join(root, 'downloads.jsonl')
  const execution = path.join(root, 'execution.txt')
  const selection = path.join(root, 'proxy.txt')
  await fs.writeFile(path.join(bin, 'curl'), `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const args = process.argv.slice(2);
const url = args.find(arg => /^https?:/.test(arg));
fs.appendFileSync(process.env.ASTRAL_TEST_CURL_LOG, JSON.stringify(url) + '\\n');
const target = args[args.indexOf('-o') + 1];
if (url === 'http://server.tailnet.ts.net:4545/health') { fs.writeFileSync(target, '{}'); process.exit(0); }
const prefix = 'https://github.com/oines/astral/releases/latest/download/';
const at = url.indexOf(prefix);
if (at < 0) process.exit(64);
const name = url.slice(at + prefix.length);
if (name !== ${JSON.stringify(binaryName)} && name !== 'checksums.txt') process.exit(64);
if (name === 'checksums.txt' && process.env.ASTRAL_TEST_BAD_CHECKSUM === '1') fs.writeFileSync(target, '0'.repeat(64) + '  ' + ${JSON.stringify(binaryName)} + '\\n');
else fs.copyFileSync(path.join(${JSON.stringify(payload)}, name), target);
`, { mode: 0o755 })
  return {
    root, bin, payload, log, execution, selection, binaryName,
    env: { ...directEnvironment, PATH: `${bin}:/usr/bin:/bin:/usr/sbin:/sbin`, XDG_DATA_HOME: path.join(root, 'data'), ASTRAL_TEST_CURL_LOG: log, ASTRAL_TEST_EXEC_LOG: execution, ASTRAL_TEST_PROXY_LOG: selection },
  }
}

test('Shell GitHub proxy covers both public downloads and never receives Server or pairing arguments', { skip: process.platform === 'win32' }, async t => {
  const fixture = await shellProxyFixture(t)
  const original = ['--server', 'http://server.tailnet.ts.net:4545', '--server-pin', 'a'.repeat(64), '--enrollment', 'fixture-enrollment', '--token', 'fixture-token', '--root', path.join(fixture.root, 'Computer space $%')]
  const { stdout } = await exec('sh', ['internal/platform/installers/install.sh', ...original, '--github-proxy', 'https://proxy.example/prefix'], { env: { ...fixture.env, ASTRAL_GITHUB_PROXY: 'https://unused.example/' } })
  const urls = (await fs.readFile(fixture.log, 'utf8')).trim().split('\n').map(JSON.parse)
  assert.deepEqual(urls, [
    'http://server.tailnet.ts.net:4545/health',
    `https://proxy.example/prefix/https://github.com/oines/astral/releases/latest/download/${fixture.binaryName}`,
    'https://proxy.example/prefix/https://github.com/oines/astral/releases/latest/download/checksums.txt',
  ])
  assert.deepEqual((await fs.readFile(fixture.execution, 'utf8')).trim().split('\n'), ['computer', 'connect', ...original])
  assert.equal(await fs.readFile(fixture.selection, 'utf8'), 'https://proxy.example/prefix/')
  assert.match(stdout, /GitHub downloads use the selected proxy: https:\/\/proxy\.example\/prefix\//)
  assert.doesNotMatch(stdout, /fixture-token|fixture-enrollment/)
  assert.equal(await fs.readlink(path.join(fixture.bin, 'astral')), path.join(fixture.env.XDG_DATA_HOME, 'astral/bin/current/astral'))
  const terminal = await exec('sh', ['-c', 'astral version'], { env: fixture.env })
  assert.equal(terminal.stdout.trim(), '0.1.9')
})

test('Shell command remains usable on the same PATH and follows a verified Computer upgrade', { skip: process.platform === 'win32' }, async t => {
  const fixture = await shellProxyFixture(t)
  const args = ['internal/platform/installers/install.sh', '--update', '--root', path.join(fixture.root, 'computer')]
  await exec('sh', args, { env: fixture.env })
  const first = await fs.readlink(path.join(fixture.env.XDG_DATA_HOME, 'astral/bin/current'))
  const binary = Buffer.from((await fs.readFile(path.join(fixture.payload, fixture.binaryName), 'utf8')).replace('0.1.9', '0.1.10'))
  await fs.writeFile(path.join(fixture.payload, fixture.binaryName), binary)
  await fs.writeFile(path.join(fixture.payload, 'checksums.txt'), `${createHash('sha256').update(binary).digest('hex')}  ${fixture.binaryName}\n`)
  await exec('sh', args, { env: fixture.env })
  const second = await fs.readlink(path.join(fixture.env.XDG_DATA_HOME, 'astral/bin/current'))
  assert.notEqual(second, first)
  const terminal = await exec('sh', ['-c', 'astral version'], { env: fixture.env })
  assert.equal(terminal.stdout.trim(), '0.1.10')
})

test('Shell restores the previous command when Computer setup fails after command verification', { skip: process.platform === 'win32' }, async t => {
  const fixture = await shellProxyFixture(t)
  const args = ['internal/platform/installers/install.sh', '--update']
  await exec('sh', args, { env: fixture.env })
  const current = path.join(fixture.env.XDG_DATA_HOME, 'astral/bin/current')
  const previous = await fs.readlink(current)
  const binary = Buffer.from((await fs.readFile(path.join(fixture.payload, fixture.binaryName), 'utf8')).replace('0.1.9', '0.1.10'))
  await fs.writeFile(path.join(fixture.payload, fixture.binaryName), binary)
  await fs.writeFile(path.join(fixture.payload, 'checksums.txt'), `${createHash('sha256').update(binary).digest('hex')}  ${fixture.binaryName}\n`)
  await assert.rejects(exec('sh', args, { env: { ...fixture.env, ASTRAL_TEST_CONNECT_FAIL: '1' } }), error => {
    assert.equal(error.code, 77)
    return true
  })
  assert.equal(await fs.readlink(current), previous)
  const terminal = await exec('sh', ['-c', 'astral version'], { env: fixture.env })
  assert.equal(terminal.stdout.trim(), '0.1.9')
})

test('Shell refuses an unrelated command before downloads and preserves a managed Server command', { skip: process.platform === 'win32' }, async t => {
  const fixture = await shellProxyFixture(t)
  const command = path.join(fixture.bin, 'astral')
  const unrelated = '#!/bin/sh\nprintf unrelated\\n\n'
  await fs.writeFile(command, unrelated, { mode: 0o700 })
  const args = ['internal/platform/installers/install.sh', '--update']
  await assert.rejects(exec('sh', args, { env: fixture.env }), error => {
    assert.match(error.stderr, /unrelated astral command/)
    return true
  })
  assert.equal(await fs.readFile(command, 'utf8'), unrelated)
  await assert.rejects(fs.access(fixture.log))
  await fs.rm(command)
  const serverRoot = path.join(fixture.root, 'server')
  const serverRelease = path.join(serverRoot, 'releases/managed')
  await fs.mkdir(path.join(serverRelease, 'bin'), { recursive: true })
  await fs.writeFile(path.join(serverRelease, 'bin/astral'), '#!/bin/sh\nprintf "0.1.8\\n"\n', { mode: 0o700 })
  await fs.writeFile(path.join(serverRoot, 'installation.json'), JSON.stringify({ schema: 1, root: serverRoot, data_directory: `${serverRoot}/data/server`, uid: process.getuid(), service: 'astral-server.service' }))
  await fs.symlink(serverRelease, path.join(serverRoot, 'current'))
  await fs.symlink(path.join(serverRoot, 'current/bin/astral'), command)
  await exec('sh', args, { env: fixture.env })
  assert.equal(await fs.readlink(command), path.join(serverRoot, 'current/bin/astral'))
  const terminal = await exec('sh', ['-c', 'astral version'], { env: fixture.env })
  assert.equal(terminal.stdout.trim(), '0.1.8')
})

test('Shell checks minimum sudo access for an existing PATH directory before downloads', { skip: process.platform === 'win32' }, async t => {
  const fixture = await shellProxyFixture(t)
  const source = await fs.readFile('internal/platform/installers/install.sh', 'utf8')
  const start = source.indexOf('# BEGIN Computer command entry\n')
  const end = source.indexOf('# END Computer command entry\n', start)
  assert.ok(start >= 0 && end > start)
  const log = path.join(fixture.root, 'sudo.log')
  await fs.writeFile(path.join(fixture.bin, 'sudo'), '#!/bin/sh\nprintf "%s\\n" "$*" >> "$ASTRAL_TEST_SUDO_LOG"\n[ "$1" = -n ] && [ "$2" = true ]\n', { mode: 0o700 })
  const script = `set -eu
install_base=$1; command_path=; command_needs_sudo=0; command_reused=0
${source.slice(start, end)}
command_directory_writable() { return 1; }
select_command_entry
printf 'SELECTED=%s SUDO=%s\\n' "$command_path" "$command_needs_sudo"
`
  const result = await exec('sh', ['-c', script, 'fixture', path.join(fixture.root, 'data/astral/bin')], { env: { ...fixture.env, PATH: `${fixture.bin}:/usr/local/bin:/usr/bin:/bin`, ASTRAL_TEST_SUDO_LOG: log } })
  assert.match(result.stdout, /SELECTED=\/usr\/local\/bin\/astral SUDO=1/)
  assert.equal(await fs.readFile(log, 'utf8'), '-n true\n')
  await assert.rejects(fs.access(fixture.log))
})

test('Shell GitHub proxy environment is optional and explicit empty selects direct downloads', { skip: process.platform === 'win32' }, async t => {
  for (const direct of [false, true]) await t.test(direct ? 'explicit direct' : 'environment proxy', async t => {
    const fixture = await shellProxyFixture(t)
    const args = ['--update', '--root', path.join(fixture.root, 'existing')]
    if (direct) args.push('--github-proxy', '')
    await exec('sh', ['internal/platform/installers/install.sh', ...args], { env: { ...fixture.env, ASTRAL_GITHUB_PROXY: 'https://gh-proxy.org/' } })
    const urls = (await fs.readFile(fixture.log, 'utf8')).trim().split('\n').map(JSON.parse)
    const proxy = direct ? '' : 'https://gh-proxy.org/'
    assert.deepEqual(urls, [`${proxy}https://github.com/oines/astral/releases/latest/download/${fixture.binaryName}`, `${proxy}https://github.com/oines/astral/releases/latest/download/checksums.txt`])
    assert.deepEqual((await fs.readFile(fixture.execution, 'utf8')).trim().split('\n'), ['computer', 'update', '--update', '--root', path.join(fixture.root, 'existing')])
    assert.equal(await fs.readFile(fixture.selection, 'utf8'), proxy)
  })
})

test('Shell proxy downloads still reject an altered binary checksum before execution', { skip: process.platform === 'win32' }, async t => {
  const fixture = await shellProxyFixture(t)
  await assert.rejects(exec('sh', ['internal/platform/installers/install.sh', '--update', '--github-proxy=https://gh-proxy.org/'], { env: { ...fixture.env, ASTRAL_TEST_BAD_CHECKSUM: '1' } }), error => {
    assert.match(error.stderr, /checksum did not match/)
    return true
  })
  await assert.rejects(fs.access(fixture.execution))
})

test('Shell rejects an invalid GitHub proxy before network requests or installation', { skip: process.platform === 'win32' }, async t => {
  const fixture = await shellProxyFixture(t)
  for (const proxy of ['http://proxy.example/', 'https://user:password@proxy.example/', 'https://proxy.example/?token=secret', 'https://proxy.example/#fragment', 'https://proxy.example/ ', 'https://proxy.example/\\path', 'https://proxy.example:70000/', 'https:///']) {
    await assert.rejects(exec('sh', ['internal/platform/installers/install.sh', '--update', '--github-proxy', proxy], { env: fixture.env }), error => {
      assert.equal(error.code, 2)
      assert.match(error.stderr, /GitHub proxy must be an HTTPS prefix/)
      return true
    })
  }
  await assert.rejects(fs.access(fixture.log))
  await assert.rejects(fs.access(fixture.env.XDG_DATA_HOME))
})


test('Windows PowerShell enrollment stops at an unavailable Server before installing or downloading', { skip: process.platform !== 'win32', timeout: 30000 }, async t => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'astral-powershell-installer-test-'))
  t.after(() => fs.rm(root, { recursive: true, force: true }))
  const requests = []
  const unavailable = createServer((request, response) => {
    requests.push(request.url)
    response.writeHead(503, { 'Content-Type': 'text/plain', Connection: 'close' })
    response.end('Fixture Server is unavailable.')
  })
  await new Promise((resolve, reject) => unavailable.once('error', reject).listen(0, '127.0.0.1', resolve))
  t.after(() => new Promise(resolve => { unavailable.closeAllConnections(); unavailable.close(resolve) }))
  const address = `http://127.0.0.1:${unavailable.address().port}`
  const installation = path.join(root, 'LocalAppData')
  const environment = Object.fromEntries(Object.entries(process.env).filter(([key]) => key.toUpperCase() !== 'LOCALAPPDATA'))
  await assert.rejects(exec('powershell.exe', [
    '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass',
    '-File', path.resolve('internal/platform/installers/install.ps1'),
    '-Server', address, '-ServerPin', 'a'.repeat(64), '-Enrollment', 'fixture-enrollment', '-Token', 'fixture-token',
  ], { env: { ...environment, LOCALAPPDATA: installation }, timeout: 25000 }), error => {
    assert.ok(error.stderr.includes(`Cannot reach Astral at ${address}`), error.stderr)
    assert.doesNotMatch(error.stderr, /requires HTTPS/)
    return true
  })
  assert.deepEqual(requests, ['/health'])
  // PowerShell itself may initialize its module cache under LOCALAPPDATA.
  // Astral must not create its installation root or download any artifact.
  await assert.rejects(fs.access(path.join(installation, 'Astral')))
})

test('Windows PowerShell update needs no Server arguments and reaches the release downloader', { skip: process.platform !== 'win32', timeout: 30000 }, async t => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'astral-powershell-update-test-'))
  t.after(() => fs.rm(root, { recursive: true, force: true }))
  const source = await fs.readFile('internal/platform/installers/install.ps1', 'utf8')
  // Test-only replacement after the real function declaration: no network,
  // published asset, actual executable or production download override.
  const fixture = source.replace('[Net.ServicePointManager]::SecurityProtocol =', "function Get-OfficialAsset { throw 'fixture official release reached' }\n[Net.ServicePointManager]::SecurityProtocol =")
  const script = path.join(root, 'install.ps1')
  await fs.writeFile(script, fixture)
  const environment = Object.fromEntries(Object.entries(process.env).filter(([key]) => key.toUpperCase() !== 'LOCALAPPDATA'))
  await assert.rejects(exec('powershell.exe', [
    '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', script,
    '-Update', '-Root', path.join(root, 'existing-computer'),
  ], { env: { ...environment, LOCALAPPDATA: path.join(root, 'LocalAppData') }, timeout: 25000 }), error => {
    assert.match(error.stderr, /fixture official release reached/)
    assert.doesNotMatch(error.stderr, /Missing Astral Server|public-key pin|Cannot reach Astral/)
    return true
  })
})

test('Windows PowerShell applies an explicit GitHub proxy only to official public assets', { skip: process.platform !== 'win32', timeout: 60000 }, async t => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'astral-powershell-proxy-test-'))
  t.after(() => fs.rm(root, { recursive: true, force: true }))
  const source = await fs.readFile('internal/platform/installers/install.ps1', 'utf8')
  const fixture = source.replace('[Net.ServicePointManager]::SecurityProtocol =', `function Get-OfficialAsset([string]$Name, [string]$Target) {
  [IO.File]::WriteAllText($env:ASTRAL_TEST_URI_FILE, (Get-OfficialAssetUri $Name).AbsoluteUri)
  throw 'fixture official release reached'
}
[Net.ServicePointManager]::SecurityProtocol =`)
  const script = path.join(root, 'install.ps1')
  await fs.writeFile(script, fixture)
  const healthRequests = []
  const server = createServer((request, response) => {
    healthRequests.push(request.url)
    response.writeHead(200, { 'Content-Type': 'application/json', Connection: 'close' })
    response.end('{"status":"ok"}')
  })
  await new Promise((resolve, reject) => server.once('error', reject).listen(0, '127.0.0.1', resolve))
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve) }))
  const serverURL = `http://127.0.0.1:${server.address().port}`
  const environment = Object.fromEntries(Object.entries(process.env).filter(([key]) => !['LOCALAPPDATA', 'ASTRAL_GITHUB_PROXY'].includes(key.toUpperCase())))
  for (const [name, extra, proxy] of [
    ['environment', [], 'https://gh-proxy.org/'],
    ['explicit', ['-GitHubProxy', 'https://proxy.example/path'], 'https://proxy.example/path/'],
    ['gnu option', ['--github-proxy', 'https://proxy.example/path/'], 'https://proxy.example/path/'],
    ['explicit direct', ['-GitHubProxy', ''], ''],
  ]) await t.test(name, async () => {
    const output = path.join(root, `uri-${name}.txt`)
    await assert.rejects(exec('powershell.exe', [
      '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', script,
      '-Server', serverURL, '-ServerPin', 'a'.repeat(64), '-Enrollment', 'fixture-enrollment', '-Token', 'fixture-token', ...extra,
    ], { env: { ...environment, LOCALAPPDATA: path.join(root, 'LocalAppData'), ASTRAL_GITHUB_PROXY: 'https://gh-proxy.org/', ASTRAL_TEST_URI_FILE: output }, timeout: 12000 }), error => {
      assert.match(error.stderr, /fixture official release reached/)
      return true
    })
    const url = await fs.readFile(output, 'utf8')
    assert.match(url, new RegExp(`^${proxy.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}https://github\\.com/oines/astral/releases/latest/download/astral-windows-(amd64|arm64)\\.exe$`))
    assert.doesNotMatch(url, /fixture-token|fixture-enrollment|127\.0\.0\.1/)
  })
  assert.deepEqual(healthRequests, ['/health', '/health', '/health', '/health'])
})

test('Windows PowerShell preserves download origin restrictions in direct and proxy modes', { skip: process.platform !== 'win32', timeout: 30000 }, async t => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'astral-powershell-proxy-origin-test-'))
  t.after(() => fs.rm(root, { recursive: true, force: true }))
  const script = path.join(root, 'origins.ps1')
  await fs.writeFile(script, `param([string]$Installer)
$ErrorActionPreference = 'Stop'
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($Installer, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw 'The installer has PowerShell syntax errors.' }
foreach ($name in @('Get-OfficialAssetUri', 'Assert-OfficialDownloadUri')) {
  $helper = $ast.Find({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name }, $true)
  if (-not $helper) { throw 'A download helper is missing.' }
  . ([scriptblock]::Create($helper.Extent.Text))
}
function Reject([scriptblock]$Operation) {
  $rejected = $false
  try { & $Operation } catch { $rejected = $true }
  if (-not $rejected) { throw 'An untrusted download was accepted.' }
}
$GitHubProxy = ''
$script:GitHubProxyUri = $null
if ((Get-OfficialAssetUri 'checksums.txt').AbsoluteUri -ne 'https://github.com/oines/astral/releases/latest/download/checksums.txt') { throw 'Wrong direct asset URL.' }
Assert-OfficialDownloadUri ([Uri]'https://github.com/oines/astral/releases/latest/download/checksums.txt')
Assert-OfficialDownloadUri ([Uri]'https://release-assets.githubusercontent.com/signed?signature=fixture')
Reject { Assert-OfficialDownloadUri ([Uri]'http://github.com/oines/astral/file') }
Reject { Assert-OfficialDownloadUri ([Uri]'https://unexpected.example/file') }
Reject { Assert-OfficialDownloadUri ([Uri]'https://user:secret@github.com/file') }
$GitHubProxy = 'https://proxy.example/prefix/'
$script:GitHubProxyUri = [Uri]$GitHubProxy
if ((Get-OfficialAssetUri 'checksums.txt').AbsoluteUri -ne 'https://proxy.example/prefix/https://github.com/oines/astral/releases/latest/download/checksums.txt') { throw 'Wrong proxy asset URL.' }
Assert-OfficialDownloadUri (Get-OfficialAssetUri 'checksums.txt')
Reject { Assert-OfficialDownloadUri ([Uri]'https://github.com/oines/astral/file') }
Reject { Assert-OfficialDownloadUri ([Uri]'https://proxy.example/other/file') }
Reject { Assert-OfficialDownloadUri ([Uri]'https://unexpected.example/prefix/file') }
Reject { Get-OfficialAssetUri '../private' }
[Console]::WriteLine('Public download boundaries passed.')
`)
  const { stdout } = await exec('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', script, '-Installer', path.resolve('internal/platform/installers/install.ps1')], { timeout: 25000 })
  assert.match(stdout, /Public download boundaries passed/)
})

test('Windows PowerShell recognizes managed junctions through short paths without accepting other targets', { skip: process.platform !== 'win32', timeout: 60000 }, async t => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'astral-powershell-junction-test-'))
  t.after(() => fs.rm(root, { recursive: true, force: true }))
  const localData = path.join(root, 'Local Application Data')
  const installBase = path.join(localData, 'Astral/bin')
  const commands = path.join(root, 'commands')
  await fs.mkdir(installBase, { recursive: true })
  await fs.mkdir(commands)
  const longLocalData = await fs.realpath(localData)
  const longInstallBase = path.join(longLocalData, 'Astral/bin')
  const current = path.join(longInstallBase, 'current')
  const managed = path.join(longInstallBase, 'a'.repeat(64))
  await fs.mkdir(managed)
  // Build a real 8.3 alias, rather than changing the fixture to use only long
  // paths and hiding a failure users can also hit through LOCALAPPDATA.
  const shortPathScript = path.join(root, 'short-path.ps1')
  await fs.writeFile(shortPathScript, `param([string]$Directory)
$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @'
using System.Runtime.InteropServices;
using System.Text;
public static class AstralFixtureShortPath {
  [DllImport("kernel32.dll", EntryPoint = "GetShortPathNameW", CharSet = CharSet.Unicode, ExactSpelling = true, SetLastError = true)]
  public static extern uint GetShortPathName(string path, StringBuilder buffer, uint capacity);
}
'@
$buffer = [Text.StringBuilder]::new(32768)
$length = [AstralFixtureShortPath]::GetShortPathName($Directory, $buffer, $buffer.Capacity)
if ($length -eq 0 -or $length -ge $buffer.Capacity) { throw 'Cannot create the fixture short path.' }
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
[Console]::WriteLine($buffer.ToString())
`)
  const { stdout } = await exec('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', shortPathScript, '-Directory', longLocalData], { timeout: 12000 })
  const shortLocalData = stdout.trim()
  const original = await fs.readFile('internal/platform/installers/install.ps1', 'utf8')
  const fixture = original.replace('[Net.ServicePointManager]::SecurityProtocol =', "function Get-OfficialAsset { throw 'fixture official release reached' }\n[Net.ServicePointManager]::SecurityProtocol =")
  assert.notEqual(fixture, original)
  const installer = path.join(root, 'install.ps1')
  await fs.writeFile(installer, fixture)
  const environment = Object.fromEntries(Object.entries(process.env).filter(([key]) => !['LOCALAPPDATA', 'ASTRAL_GITHUB_PROXY', 'PATH'].includes(key.toUpperCase())))
  const run = selectedLocalData => exec('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', installer, '-Update'], {
    env: { ...environment, PATH: `${commands};${process.env.PATH}`, LOCALAPPDATA: selectedLocalData, ASTRAL_GITHUB_PROXY: '' }, timeout: 12000,
  })
  await fs.symlink(managed, current, 'junction')
  for (const [name, selectedLocalData] of [['long path', longLocalData], ['short alias', shortLocalData]]) {
    await t.test(name, { skip: name === 'short alias' && shortLocalData.toLowerCase() === longLocalData.toLowerCase() ? 'This volume has no 8.3 alias.' : false }, async () => {
      await assert.rejects(run(selectedLocalData), error => {
        assert.match(error.stderr, /fixture official release reached/)
        assert.doesNotMatch(error.stderr, /installer-managed junction/)
        return true
      })
      assert.equal(await fs.realpath(current), managed)
    })
  }
  for (const [name, target] of [
    ['another installation with the same prefix', path.join(`${longInstallBase}-other`, 'b'.repeat(64))],
    ['a nested release', path.join(longInstallBase, 'nested', 'c'.repeat(64))],
    ['a non-SHA256 release name', path.join(longInstallBase, 'not-a-release')],
  ]) await t.test(name, async () => {
    await fs.unlink(current)
    await fs.mkdir(target, { recursive: true })
    await fs.symlink(target, current, 'junction')
    await assert.rejects(run(shortLocalData), error => {
      assert.match(error.stderr, /installer-managed junction/)
      assert.doesNotMatch(error.stderr, /fixture official release reached/)
      return true
    })
    assert.equal(await fs.realpath(current), target)
  })
  await t.test('a regular directory', async () => {
    await fs.unlink(current)
    await fs.mkdir(current)
    await assert.rejects(run(shortLocalData), error => {
      assert.match(error.stderr, /installer-managed junction/)
      assert.doesNotMatch(error.stderr, /fixture official release reached/)
      return true
    })
    assert.equal((await fs.lstat(current)).isDirectory(), true)
    assert.equal((await fs.lstat(current)).isSymbolicLink(), false)
  })
})

test('Windows PowerShell keeps astral usable across upgrades, passes the proxy and preserves unrelated commands', { skip: process.platform !== 'win32', timeout: 60000 }, async t => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'astral-powershell-proxy-child-中文 space $%-'))
  t.after(() => fs.rm(root, { recursive: true, force: true }))
  const original = await fs.readFile('internal/platform/installers/install.ps1', 'utf8')
  const fixture = original.replace('[Net.ServicePointManager]::SecurityProtocol =', `function Get-OfficialAsset([string]$Name, [string]$Target) {
  if ($Name -eq 'checksums.txt') { [IO.File]::WriteAllText($Target, (Get-FileSHA256 $env:ASTRAL_TEST_BINARY_FILE) + '  astral-windows-' + $arch + '.exe') }
  else { [IO.File]::Copy($env:ASTRAL_TEST_BINARY_FILE, $Target) }
}
[Net.ServicePointManager]::SecurityProtocol =`)
  assert.notEqual(fixture, original)
  const installer = path.join(root, 'install.ps1')
  const driver = path.join(root, 'driver.ps1')
  await fs.writeFile(installer, fixture)
  await fs.writeFile(driver, `param([string]$Installer, [string]$Proxy)
$ErrorActionPreference = 'Stop'
if (-not (Test-Path -LiteralPath $env:ASTRAL_TEST_BINARY_FILE)) {
  Add-Type -OutputType ConsoleApplication -OutputAssembly $env:ASTRAL_TEST_BINARY_FILE -TypeDefinition @"
using System;
using System.IO;
public class AstralFixture {
  public static int Main(string[] args) {
    if (args.Length == 1 && args[0] == "version") { Console.WriteLine("$env:ASTRAL_TEST_VERSION"); return 0; }
    File.WriteAllText(Environment.GetEnvironmentVariable("ASTRAL_TEST_PROXY_FILE"), Environment.GetEnvironmentVariable("ASTRAL_GITHUB_PROXY") ?? "");
    if (Environment.GetEnvironmentVariable("ASTRAL_TEST_CONNECT_FAIL") == "1") return 77;
    return 0;
  }
}
"@
}
$env:ASTRAL_GITHUB_PROXY = 'https://parent.example/'
& $Installer -Update -GitHubProxy $Proxy
if ($env:ASTRAL_GITHUB_PROXY -ne 'https://parent.example/') { throw 'The installer changed the calling session environment.' }
`)
  const commands = path.join(root, 'commands')
  await fs.mkdir(commands)
  const environment = Object.fromEntries(Object.entries(process.env).filter(([key]) => !['LOCALAPPDATA', 'ASTRAL_GITHUB_PROXY', 'PATH'].includes(key.toUpperCase())))
  const targets = []
  for (const [selected, version] of [['https://selected.example/path', '0.1.9'], ['', '0.1.10']]) {
    const output = path.join(root, selected ? 'selected.txt' : 'direct.txt')
    const env = { ...environment, PATH: `${commands};${process.env.PATH}`, LOCALAPPDATA: path.join(root, 'LocalAppData'), ASTRAL_TEST_PROXY_FILE: output, ASTRAL_TEST_BINARY_FILE: path.join(root, `fixture-${version}.exe`), ASTRAL_TEST_VERSION: version }
    await exec('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', driver, '-Installer', installer, '-Proxy', selected], {
      env, timeout: 20000,
    })
    assert.equal(await fs.readFile(output, 'utf8'), selected ? `${selected}/` : '')
    const terminal = await exec('cmd.exe', ['/d', '/c', 'astral version'], { env, timeout: 12000 })
    assert.equal(terminal.stdout.trim(), version)
    targets.push(await fs.realpath(path.join(env.LOCALAPPDATA, 'Astral/bin/current')))
  }
  assert.notEqual(targets[0], targets[1])
  const failedEnvironment = { ...environment, PATH: `${commands};${process.env.PATH}`, LOCALAPPDATA: path.join(root, 'LocalAppData'), ASTRAL_TEST_PROXY_FILE: path.join(root, 'failed.txt'), ASTRAL_TEST_BINARY_FILE: path.join(root, 'fixture-0.1.11.exe'), ASTRAL_TEST_VERSION: '0.1.11', ASTRAL_TEST_CONNECT_FAIL: '1' }
  await assert.rejects(exec('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', driver, '-Installer', installer, '-Proxy', ''], {
    env: failedEnvironment, timeout: 20000,
  }), error => {
    assert.match(error.stderr, /Astral exited with code 77/)
    return true
  })
  assert.equal(await fs.realpath(path.join(failedEnvironment.LOCALAPPDATA, 'Astral/bin/current')), targets[1])
  assert.equal((await exec('cmd.exe', ['/d', '/c', 'astral version'], { env: failedEnvironment, timeout: 12000 })).stdout.trim(), '0.1.10')
  const command = path.join(commands, 'astral.cmd')
  const unrelated = '@echo unrelated\r\n'
  await fs.writeFile(command, unrelated)
  await assert.rejects(exec('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', installer, '-Update'], {
    env: { ...environment, PATH: `${commands};${process.env.PATH}`, LOCALAPPDATA: path.join(root, 'LocalAppData'), ASTRAL_GITHUB_PROXY: '' }, timeout: 12000,
  }), error => {
    assert.match(error.stderr, /unrelated astral command/)
    return true
  })
  assert.equal(await fs.readFile(command, 'utf8'), unrelated)
})
