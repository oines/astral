import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { checkReleasedMigrations, resolvePinnedCoreModule } from './check-migrations.mjs'

const coreModule = 'github.com/oines/astral-core'
const oldVersion = 'v0.0.0-20261027103920-027b81ca29b6'
const newVersion = 'v0.0.0-20261028103920-123456789abc'
const hostSources = ['internal/persistence/migrations/00003_baseline.sql', 'internal/persistence/migrations/00004_records.go']
const coreSources = ['store/sqlite/migrations/00011_baseline.sql', 'store/sqlite/migrations/00012_records.go']
const goMod = version => `module fixture\n\ngo 1.26.0\n\nrequire ${coreModule} ${version}\n`

function write(directory, file, contents) {
  mkdirSync(path.dirname(path.join(directory, file)), { recursive: true })
  writeFileSync(path.join(directory, file), contents)
}

function fixture(t, { released = true } = {}) {
  const temporary = mkdtempSync(path.join(tmpdir(), 'astral-migrations-fixture-'))
  t.after(() => rmSync(temporary, { recursive: true, force: true }))
  const cwd = path.join(temporary, 'host')
  mkdirSync(cwd)
  const git = args => execFileSync('git', args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim()
  git(['init', '-q'])
  write(cwd, 'go.mod', goMod(oldVersion))
  for (const file of hostSources) write(cwd, file, `published Host ${file}\n`)
  git(['add', '.'])
  git(['-c', 'commit.gpgsign=false', '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.test', 'commit', '-qm', 'fixture'])
  if (released) git(['-c', 'tag.gpgsign=false', 'tag', 'v0.1.0'])
  write(cwd, 'go.mod', goMod(newVersion))
  const previous = path.join(temporary, 'previous-core')
  const current = path.join(temporary, 'current-core')
  for (const file of coreSources) {
    write(previous, file, `published Core ${file}\n`)
    write(current, file, `published Core ${file}\n`)
  }
  const calls = []
  const resolveCoreModule = (manifest, options) => {
    assert.equal(options.cwd, cwd)
    calls.push(manifest)
    if (manifest.includes(oldVersion)) return { version: oldVersion, directory: previous }
    assert.ok(manifest.includes(newVersion))
    return { version: newVersion, directory: current }
  }
  return { cwd, git, previous, current, calls, options: { resolveCoreModule } }
}

test('before the first stable Host tag the check returns zero without resolving modules', t => {
  const f = fixture(t, { released: false })
  f.git(['-c', 'tag.gpgsign=false', 'tag', 'v99.0.0-preview'])
  rmSync(path.join(f.cwd, 'go.mod'))
  assert.deepEqual(checkReleasedMigrations(f.cwd, { resolveCoreModule: () => assert.fail('first release must not access Go or the network') }), { baseline: null, checked: 0 })
})

test('published Host and Core SQL/Go sources survive while new steps may be appended', t => {
  const f = fixture(t)
  write(f.cwd, 'internal/persistence/migrations/00005_more.sql', 'new Host step\n')
  write(f.current, 'store/sqlite/migrations/00013_more.sql', 'new Core SQL step\n')
  write(f.current, 'store/sqlite/migrations/00014_more.go', 'new frozen Core Go step\n')
  assert.deepEqual(checkReleasedMigrations(f.cwd, f.options), {
    baseline: 'v0.1.0', checked: 4,
    core: { baseline: oldVersion, current: newVersion, checked: 2 },
  })
  assert.equal(f.calls.length, 2)
  assert.ok(f.calls[0].includes(oldVersion))
  assert.ok(f.calls[1].includes(newVersion))
})

for (const file of coreSources) {
  test(`a changed published Core source fails: ${file}`, t => {
    const f = fixture(t)
    write(f.current, file, 'changed migration\n')
    assert.throws(() => checkReleasedMigrations(f.cwd, f.options), error => error.message === `Published Core migration changed: ${file}. Add a new migration instead.`)
  })
  test(`a removed or renamed published Core source fails: ${file}`, t => {
    const f = fixture(t)
    rmSync(path.join(f.current, file))
    write(f.current, file.replace(/\d+_/, '00099_'), 'renamed migration\n')
    assert.throws(() => checkReleasedMigrations(f.cwd, f.options), error => error.message === `Published Core migration was removed: ${file}`)
  })
}

for (const file of hostSources) {
  test(`the original Host guard rejects changes before resolving Core: ${file}`, t => {
    const f = fixture(t)
    write(f.cwd, file, 'changed migration\n')
    assert.throws(() => checkReleasedMigrations(f.cwd, f.options), /Published migration changed:/)
    assert.equal(f.calls.length, 0)
  })
  test(`the original Host guard rejects removal before resolving Core: ${file}`, t => {
    const f = fixture(t)
    rmSync(path.join(f.cwd, file))
    assert.throws(() => checkReleasedMigrations(f.cwd, f.options), /Published migration was removed:/)
    assert.equal(f.calls.length, 0)
  })
}

test('the highest stable version tag selects both the Host and Core baseline', t => {
  const f = fixture(t)
  for (const tag of ['v0.9.0', 'v0.10.0', 'v99.0.0-preview']) f.git(['-c', 'tag.gpgsign=false', 'tag', tag])
  assert.equal(checkReleasedMigrations(f.cwd, f.options).baseline, 'v0.10.0')
  assert.ok(f.calls[0].includes(oldVersion))
})

test('module resolution only parses and downloads the exact published pin in an isolated workspace', () => {
  const calls = []
  let manifest
  const directory = path.resolve('fixture-module-cache')
  const result = resolvePinnedCoreModule(goMod(oldVersion), {
    command: (executable, args, options) => {
      assert.equal(executable, 'go')
      assert.equal(options.env.GOWORK, 'off')
      assert.ok(options.timeout > 0)
      calls.push(args)
      if (args[1] === 'edit') {
        assert.deepEqual(args.slice(0, 3), ['mod', 'edit', '-json'])
        manifest = args[3]
        assert.equal(path.dirname(manifest), options.cwd)
        assert.equal(readFileSync(manifest, 'utf8'), goMod(oldVersion))
        return JSON.stringify({ Require: [{ Path: coreModule, Version: oldVersion }] })
      }
      assert.deepEqual(args, ['mod', 'download', '-json', `${coreModule}@${oldVersion}`])
      assert.equal(path.dirname(manifest), options.cwd)
      assert.ok(existsSync(manifest))
      return JSON.stringify({ Path: coreModule, Version: oldVersion, Dir: directory })
    },
  })
  assert.deepEqual(result, { version: oldVersion, directory })
  assert.equal(calls.length, 2)
  assert.equal(existsSync(path.dirname(manifest)), false)
})

test('missing pins and Core replacements fail before any module download', () => {
  for (const metadata of [
    { Require: [] },
    { Require: [{ Path: coreModule, Version: 'latest' }] },
    { Require: [{ Path: coreModule, Version: oldVersion }], Replace: [{ Old: { Path: coreModule }, New: { Path: '../private-core' } }] },
  ]) {
    let calls = 0
    assert.throws(() => resolvePinnedCoreModule('fixture', {
      command: (_executable, args) => { calls++; assert.equal(args[1], 'edit'); return JSON.stringify(metadata) },
    }), /must pin exactly one published|without a replace directive/)
    assert.equal(calls, 1)
  }
})

test('failed or mismatched module resolution cannot silently skip the Core guard', () => {
  for (const downloaded of [
    { Error: 'module unavailable' },
    { Path: 'other/module', Version: oldVersion, Dir: path.resolve('other') },
    { Path: coreModule, Version: newVersion, Dir: path.resolve('other') },
    { Path: coreModule, Version: oldVersion, Dir: '../local-core' },
  ]) {
    assert.throws(() => resolvePinnedCoreModule('fixture', {
      command: (_executable, args) => JSON.stringify(args[1] === 'edit' ? { Require: [{ Path: coreModule, Version: oldVersion }] } : downloaded),
    }), /Could not resolve the published/)
  }
})
