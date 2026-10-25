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
    env: { ...process.env, PATH: `${bin}${path.delimiter}${process.env.PATH}`, XDG_DATA_HOME: installation, ASTRAL_TEST_CURL_LOG: log },
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
    env: { ...process.env, PATH: `${bin}${path.delimiter}${process.env.PATH}`, XDG_DATA_HOME: path.join(root, 'data'), ASTRAL_TEST_CURL_LOG: log },
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
