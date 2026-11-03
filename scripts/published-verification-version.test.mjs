import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { chmod, copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { createBuildMetadata, releaseWorkflowPath } from './rolling-release-version.mjs'
import { repositoryRoot } from './release-version.mjs'

const workflow = await readFile(new URL('../.github/workflows/verify-published-update.yml', import.meta.url), 'utf8')
function stepCode(name) {
  const block = workflow.slice(workflow.indexOf(`      - name: ${name}\n`)).split('\n      - name: ')[0]
  const match = / {10}node --input-type=module <<'JS'\n([\s\S]*?) {10}JS/.exec(block)
  assert.ok(match, `Missing executable verification step: ${name}`)
  return match[1].split('\n').map(line => line.replace(/^ {10}/, '')).join('\n')
}
const overlayCode = stepCode('Load and validate the published effective version')
const untouchedCode = stepCode('Require only the frozen version overlay in the published source')

async function fixture(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'astral-published-version-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  await mkdir(path.join(root, 'apps/desktop'), { recursive: true })
  await mkdir(path.join(root, 'scripts'))
  for (const name of ['rolling-release-version.mjs', 'release-version.mjs', 'computer-image-contract.mjs']) await copyFile(path.join(repositoryRoot, 'scripts', name), path.join(root, 'scripts', name))
  for (const name of ['package.json', 'apps/desktop/package.json']) await writeFile(path.join(root, name), JSON.stringify({ name: 'fixture', version: '0.1.13', private: true }))
  await writeFile(path.join(root, 'README.md'), 'source baseline\n')
  const git = args => execFileSync('git', args, { cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 15_000 })
  git(['init', '--quiet'])
  git(['add', '.'])
  git(['-c', 'commit.gpgsign=false', '-c', 'user.name=fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '--quiet', '-m', 'fixture baseline'])
  const source = git(['rev-parse', 'HEAD']).trim()
  const manifest = { schema_version: 1, purpose: 'release', public_release_ready: true, repository: 'oines/astral', source_sha: source, version: '0.1.48', tag: 'v0.1.48' }
  const environment = { ...process.env, GITHUB_REPOSITORY: 'oines/astral', RELEASE_TAG: 'v0.1.48', GITHUB_ENV: path.join(root, 'github-env'), ASTRAL_RELEASE_VERSION: '0.1.48' }
  const execute = (code, prefix = '') => execFileSync(process.execPath, ['--input-type=module', '--eval', prefix + code], { cwd: root, env: environment, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 15_000 })
  const overlay = (value = manifest, { wrongChecksum = false } = {}) => {
    const bytes = JSON.stringify(value)
    const digest = wrongChecksum ? '0'.repeat(64) : createHash('sha256').update(bytes).digest('hex')
    const responses = { 'https://github.com/oines/astral/releases/download/v0.1.48/release-manifest.json': bytes, 'https://github.com/oines/astral/releases/download/v0.1.48/SHA256SUMS': `${digest}  release-manifest.json\n` }
    execute(overlayCode, `globalThis.fetch = async url => { const responses = ${JSON.stringify(responses)}; if (!(url in responses)) throw new Error('Unexpected network request'); return new Response(responses[url]); };\n`)
  }
  return { root, source, manifest, overlay, verify: () => execute(untouchedCode) }
}

test('manual public verification uses the release version while retaining its tagged source', async t => {
  const { root, overlay, verify } = await fixture(t)
  overlay()
  for (const name of ['package.json', 'apps/desktop/package.json']) assert.deepEqual(JSON.parse(await readFile(path.join(root, name), 'utf8')), { name: 'fixture', version: '0.1.48', private: true })
  assert.equal(await readFile(path.join(root, 'github-env'), 'utf8'), 'ASTRAL_RELEASE_VERSION=0.1.48\n')
  verify()
})

test('a rolling public manifest must retain the frozen producer version and source', async t => {
  const { overlay, manifest, source, verify } = await fixture(t)
  const provenance = createBuildMetadata({ GITHUB_REPOSITORY: 'oines/astral', GITHUB_RUN_ID: '123', GITHUB_RUN_ATTEMPT: '1', GITHUB_RUN_NUMBER: '35', GITHUB_WORKFLOW_REF: `oines/astral/${releaseWorkflowPath}@refs/heads/main` }, source)
  assert.equal(provenance.version, manifest.version)
  overlay({ ...manifest, build_provenance: provenance })
  verify()
  assert.throws(() => overlay({ ...manifest, build_provenance: { ...provenance, source_sha: '2'.repeat(40) } }), /expected source/)
})

test('wrong public source, version or checksum rejects before overlaying manifests', async t => {
  const { root, overlay, manifest } = await fixture(t)
  assert.throws(() => overlay({ ...manifest, source_sha: '2'.repeat(40) }))
  assert.throws(() => overlay({ ...manifest, version: '0.1.49' }))
  assert.throws(() => overlay(manifest, { wrongChecksum: true }), /checksum mismatch/)
  assert.equal(JSON.parse(await readFile(path.join(root, 'package.json'), 'utf8')).version, '0.1.13')
})

test('the manual verifier refuses any non-version manifest or tracked-source change', async t => {
  const { root, overlay, verify } = await fixture(t)
  overlay()
  const file = path.join(root, 'package.json')
  const value = JSON.parse(await readFile(file, 'utf8'))
  await writeFile(file, JSON.stringify({ ...value, private: false }))
  assert.throws(verify, /non-version manifest change/)
  await writeFile(file, JSON.stringify(value))
  if (process.platform !== 'win32') {
    await chmod(file, 0o755)
    assert.throws(verify, /tracked paths or file modes/)
    await chmod(file, 0o644)
  }
  await writeFile(path.join(root, 'README.md'), 'source changed\n')
  assert.throws(verify, /modified published source beyond/)
})
