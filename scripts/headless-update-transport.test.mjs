import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { promisify } from 'node:util'
import { fileURLToPath } from 'node:url'
import { headlessUpdateTransport } from './headless-update-transport.mjs'
import { prepareHostMigrationFixture, retainedFixtureCookie } from './check-headless-install.mjs'

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

test('native Owner requests and restart polling retain the authenticated browser session without repeated logins', async () => {
  let logins = 0
  const cookie = retainedFixtureCookie(async origin => {
    assert.equal(origin, 'http://127.0.0.1:4545')
    assert.ok(++logins <= 12, 'The real per-minute login budget was exhausted')
    return 'astral_session=temporary-authenticated-browser'
  })
  const beforeRestart = await cookie('http://127.0.0.1:4545')
  const requests = await Promise.all(Array.from({ length: 40 }, () => cookie('http://127.0.0.1:4545/')))
  assert.ok(requests.every(value => value === beforeRestart))
  assert.equal(await cookie('http://127.0.0.1:4545'), beforeRestart, 'A replacement Server must validate the persisted original session')
  assert.equal(logins, 1)
})

test('a failed initial login can retry, and distinct Server origins have separate sessions', async () => {
  let logins = 0
  const cookie = retainedFixtureCookie(async origin => {
    if (++logins === 1) throw new Error('Server is not ready')
    return `authenticated:${origin}`
  })
  await assert.rejects(cookie('http://127.0.0.1:4545'), /Server is not ready/)
  assert.equal(await cookie('http://127.0.0.1:4545'), 'authenticated:http://127.0.0.1:4545')
  assert.equal(await cookie('http://127.0.0.1:4546'), 'authenticated:http://127.0.0.1:4546')
  assert.equal(logins, 3)
})

test('headless recovery fixture only resets Host migration state and preserves current Core schema and ledger', { skip: process.platform === 'win32' }, async t => {
  const data = await mkdtemp(path.join(tmpdir(), 'astral-headless-migration-fixture-'))
  t.after(() => rm(data, { recursive: true, force: true }))
  await execute('python3', ['-c', `import pathlib,sqlite3,sys
for name,version in (('astral.db',6),('core.db',12)):
    with sqlite3.connect(pathlib.Path(sys.argv[1])/name) as db:
        db.execute('CREATE TABLE goose_db_version(version_id INTEGER,is_applied INTEGER)')
        db.execute('INSERT INTO goose_db_version VALUES(?,1)',(version,))
        db.execute('CREATE TABLE retained_content(body TEXT)')
        db.execute("INSERT INTO retained_content VALUES('keep actual data')")
        db.execute('PRAGMA user_version='+str(version))
`, data], { encoding: 'utf8', timeout: 5000 })
  const coreBefore = await readFile(path.join(data, 'core.db'))
  await prepareHostMigrationFixture(data)
  assert.deepEqual(await readFile(path.join(data, 'core.db')), coreBefore, 'Fixture changed the current Core database')
  const state = await execute('python3', ['-c', `import json,pathlib,sqlite3,sys
result={}
for name in ('astral.db','core.db'):
    with sqlite3.connect(pathlib.Path(sys.argv[1])/name) as db:
        result[name]={'schema':db.execute('PRAGMA user_version').fetchone()[0],
          'ledger':db.execute("SELECT count(*) FROM sqlite_schema WHERE name='goose_db_version'").fetchone()[0],
          'content':db.execute('SELECT body FROM retained_content').fetchone()[0]}
print(json.dumps(result))
`, data], { encoding: 'utf8', timeout: 5000 })
  assert.deepEqual(JSON.parse(state.stdout), {
    'astral.db': { schema: 3, ledger: 0, content: 'keep actual data' },
    'core.db': { schema: 12, ledger: 1, content: 'keep actual data' },
  })
})
