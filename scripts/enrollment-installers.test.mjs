import test from 'node:test'
import assert from 'node:assert/strict'
import { promises as fs } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { createServer } from 'node:http'

const exec = promisify(execFile)

test('Shell enrollment checks the selected HTTP address before installing or downloading', { skip: process.platform === 'win32' }, async t => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'astral-installer-test-'))
  t.after(() => fs.rm(root, { recursive: true, force: true }))
  const bin = path.join(root, 'bin')
  await fs.mkdir(bin)
  const log = path.join(root, 'curl.log')
  await fs.writeFile(path.join(bin, 'curl'), '#!/bin/sh\nprintf "%s\\n" "$*" > "$ASTRAL_TEST_CURL_LOG"\nexit 7\n', { mode: 0o755 })
  const installation = path.join(root, 'data')
  await assert.rejects(exec('sh', ['internal/platform/installers/install.sh', '--server', 'http://server.tailnet.ts.net:4545', '--enrollment', 'id', '--token', 'token'], {
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
    '-Server', address, '-Enrollment', 'fixture-enrollment', '-Token', 'fixture-token',
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
