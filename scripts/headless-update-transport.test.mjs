import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { promisify } from 'node:util'
import { fileURLToPath } from 'node:url'
import { headlessUpdateTransport } from './headless-update-transport.mjs'

const execute = promisify(execFile)
test('the CI-only trusted HTTPS transport serves fixed official URLs and restores selected manager variables', { skip: process.platform === 'win32' }, async t => {
  const temporary = await mkdtemp(path.join(tmpdir(), 'astral-headless-transport-'))
  t.after(() => rm(temporary, { recursive: true, force: true }))
  const payload = path.join(temporary, 'release')
  await mkdir(payload)
  await writeFile(path.join(payload, 'release.json'), '{"tag_name":"v0.1.2"}')
  await writeFile(path.join(payload, 'install-server.sh'), 'verified tiny installer fixture')
  const calls = []
  const command = async (name, args) => {
    if (name === 'openssl') return execute(name, args, { encoding: 'utf8', timeout: 15_000 })
    calls.push([name, ...args])
    assert.ok(['systemd-run', 'systemctl'].includes(name), 'Fixtures must never execute a real service command')
    return { stdout: name === 'systemd-run' ? 'PATH=/previous/path with spaces\0HTTPS_PROXY=http://previous.invalid\0UNRELATED=private\0' : '' }
  }
  const fixture = await headlessUpdateTransport({ temporary, payload, tag: () => 'v0.1.2', environment: { PATH: process.env.PATH }, command })
  try {
    // Use Node's actual TLS verification and standard proxy support; no product
    // URL override, trust-store modification or external request is involved.
    const fetch = url => execute(process.execPath, ['--use-env-proxy', '-e', `fetch(${JSON.stringify(url)}).then(async response => {if(!response.ok)process.exit(1);process.stdout.write(await response.text())}).catch(()=>process.exit(2))`], {
      env: { ...process.env, HTTPS_PROXY: fixture.proxyURL, NO_PROXY: '', NODE_EXTRA_CA_CERTS: fixture.certificate }, encoding: 'utf8', timeout: 10_000,
    })
    assert.equal((await fetch('https://api.github.com/repos/oines/astral/releases/latest')).stdout, '{"tag_name":"v0.1.2"}')
    assert.equal((await fetch('https://github.com/oines/astral/releases/download/v0.1.2/install-server.sh')).stdout, 'verified tiny installer fixture')
    await assert.rejects(fetch('https://github.com/oines/astral/releases/download/v0.1.1/install-server.sh'))
    assert.equal(fixture.requests.length, 3)
  } finally { await fixture.close() }
  assert.ok(calls.some(args => args.includes('PATH=/previous/path with spaces')))
  assert.ok(calls.some(args => args.includes('HTTPS_PROXY=http://previous.invalid')))
  assert.ok(calls.some(args => args.includes('unset-environment') && args.includes('SSL_CERT_FILE')))
  assert.ok(calls.every(args => !args.join(' ').includes('UNRELATED')))
})

test('real headless acceptance refuses a developer account before commands, writes or installation', async () => {
  const environment = { ...process.env }
  for (const name of ['CI', 'GITHUB_ACTIONS', 'RUNNER_TEMP']) delete environment[name]
  await assert.rejects(execute(process.execPath, [fileURLToPath(new URL('./check-headless-install.mjs', import.meta.url))], { env: environment, encoding: 'utf8', timeout: 10_000 }), error => error.code === 1 && /requires a disposable GitHub Actions user/.test(error.stderr))
})
