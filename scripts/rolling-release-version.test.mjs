import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { createBuildMetadata, applyBuildVersion, releaseWorkflowPath, validateBuildMetadata, versionForRun } from './rolling-release-version.mjs'
import { readReleaseVersion, repositoryRoot } from './release-version.mjs'

const sourceSHA = '1'.repeat(40)
const producerEnvironment = {
  GITHUB_REPOSITORY: 'oines/astral', GITHUB_SHA: sourceSHA,
  GITHUB_RUN_ID: '123', GITHUB_RUN_NUMBER: '1', GITHUB_RUN_ATTEMPT: '1',
  GITHUB_WORKFLOW_REF: `oines/astral/${releaseWorkflowPath}@refs/heads/main`,
}

test('rolling versions are reserved by run number and unchanged on retry', () => {
  assert.equal(versionForRun(1), '0.1.14')
  assert.equal(versionForRun('3'), '0.1.16')
  const first = createBuildMetadata(producerEnvironment)
  const retried = createBuildMetadata({ ...producerEnvironment, GITHUB_RUN_ATTEMPT: '2' })
  assert.equal(first.version, retried.version)
  assert.equal(first.build_run_id, retried.build_run_id)
  assert.equal(retried.build_run_attempt, 2)
  assert.equal(versionForRun(Number.MAX_SAFE_INTEGER - 13), `0.1.${Number.MAX_SAFE_INTEGER}`)
})

test('invalid counters never become reused or imprecise versions', () => {
  for (const counter of [undefined, null, true, '', '0', 0, -1, 1.5, '01', '+1', '1e3', ' 1', '1\n', Number.NaN, Number.MAX_SAFE_INTEGER - 12, '9007199254740992']) assert.throws(() => versionForRun(counter), undefined, String(counter))
})

test('frozen metadata binds repository, workflow, source, version and attempt', () => {
  const metadata = createBuildMetadata(producerEnvironment)
  assert.deepEqual(metadata, {
    schema_version: 1, repository: 'oines/astral', source_sha: sourceSHA, version: '0.1.14',
    build_run_id: 123, build_run_attempt: 1, build_run_number: 1,
    build_run_url: 'https://github.com/oines/astral/actions/runs/123', workflow_path: releaseWorkflowPath,
  })
  assert.equal(validateBuildMetadata(metadata, { version: '0.1.14', sourceSHA, repository: 'oines/astral' }), metadata)
  for (const change of [
    { schema_version: 2 }, { extra: 'unsupported' }, { repository: '../astral' }, { source_sha: 'short' },
    { version: '0.1.15' }, { build_run_id: '123' }, { build_run_attempt: 0 }, { build_run_number: 2 },
    { build_run_url: 'https://github.com/oines/astral/actions/runs/456' }, { workflow_path: '.github/workflows/untrusted.yml' },
  ]) assert.throws(() => validateBuildMetadata({ ...metadata, ...change }))
  for (const expected of [{ repository: 'other/astral' }, { sourceSHA: '2'.repeat(40) }, { version: '0.1.15' }]) assert.throws(() => validateBuildMetadata(metadata, expected), /expected source/)
  for (const reference of [undefined, `oines/astral/${releaseWorkflowPath}@refs/heads/other`, 'oines/astral/.github/workflows/untrusted.yml@refs/heads/main']) assert.throws(() => createBuildMetadata({ ...producerEnvironment, GITHUB_WORKFLOW_REF: reference }), /prepare-release.yml on main/)
})

async function fixture(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'astral-version-fixture-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  await mkdir(path.join(root, 'apps/desktop'), { recursive: true })
  for (const name of ['package.json', 'apps/desktop/package.json']) await writeFile(path.join(root, name), JSON.stringify({ name: 'fixture', version: '0.1.13', private: true }))
  const git = args => execFileSync('git', args, { cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 15_000 })
  git(['init', '--quiet'])
  git(['add', 'package.json', 'apps/desktop/package.json'])
  git(['-c', 'commit.gpgsign=false', '-c', 'user.name=fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '--quiet', '-m', 'fixture baseline'])
  const sha = git(['rev-parse', 'HEAD']).trim()
  return { root, git, metadata: createBuildMetadata(producerEnvironment, sha) }
}

test('version overrides leave matching development manifests unchanged', async t => {
  const { root } = await fixture(t)
  assert.equal(await readReleaseVersion(root, {}), '0.1.13')
  assert.equal(await readReleaseVersion(root, { ASTRAL_RELEASE_VERSION: '0.1.48' }), '0.1.48')
  assert.equal(JSON.parse(await readFile(path.join(root, 'apps/desktop/package.json'), 'utf8')).version, '0.1.13')
  await assert.rejects(readReleaseVersion(root, { ASTRAL_RELEASE_VERSION: '' }), /Release version/)
  await writeFile(path.join(root, 'apps/desktop/package.json'), JSON.stringify({ version: '0.1.12' }))
  await assert.rejects(readReleaseVersion(root, { ASTRAL_RELEASE_VERSION: '0.1.48' }), /versions differ/)
})

test('overlay changes only package versions, exports env, and preserves the source commit', async t => {
  const { root, metadata, git } = await fixture(t)
  const environmentFile = path.join(root, 'github-env')
  // A consumer's default-branch GITHUB_SHA is not the producer source.
  await applyBuildVersion(metadata, { root, environment: { GITHUB_SHA: '2'.repeat(40), GITHUB_ENV: environmentFile, GITHUB_REPOSITORY: 'oines/astral' } })
  for (const name of ['package.json', 'apps/desktop/package.json']) assert.deepEqual(JSON.parse(await readFile(path.join(root, name), 'utf8')), { name: 'fixture', version: metadata.version, private: true })
  assert.equal(git(['rev-parse', 'HEAD']).trim(), metadata.source_sha)
  assert.deepEqual(git(['diff', '--name-only']).trim().split('\n').sort(), ['apps/desktop/package.json', 'package.json'])
  assert.equal(await readFile(environmentFile, 'utf8'), 'ASTRAL_RELEASE_VERSION=0.1.14\n')
})

test('wrong checkout or override rejects before changing either manifest', async t => {
  const { root, metadata } = await fixture(t)
  const before = await readFile(path.join(root, 'package.json'), 'utf8')
  await assert.rejects(applyBuildVersion({ ...metadata, source_sha: '2'.repeat(40) }, { root, environment: {} }), /Checked-out source/)
  await assert.rejects(applyBuildVersion(metadata, { root, environment: { ASTRAL_RELEASE_VERSION: '0.1.15' } }), /override differs/)
  await assert.rejects(applyBuildVersion(metadata, { root, environment: { GITHUB_REPOSITORY: 'other/astral' } }), /repository does not match/)
  assert.equal(await readFile(path.join(root, 'package.json'), 'utf8'), before)
})

test('freeze and apply CLI exchange exact metadata and GitHub outputs', async t => {
  const { root, git, metadata } = await fixture(t)
  await mkdir(path.join(root, 'scripts'))
  for (const name of ['rolling-release-version.mjs', 'release-version.mjs', 'computer-image-contract.mjs']) await copyFile(path.join(repositoryRoot, 'scripts', name), path.join(root, 'scripts', name))
  const output = path.join(root, 'outputs')
  const environment = { ...process.env, ...producerEnvironment, GITHUB_SHA: metadata.source_sha, GITHUB_OUTPUT: output }
  delete environment.ASTRAL_RELEASE_VERSION
  delete environment.GITHUB_ENV
  const run = args => execFileSync(process.execPath, [path.join(root, 'scripts/rolling-release-version.mjs'), ...args], { cwd: root, env: environment, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 15_000 })
  const frozen = JSON.parse(run(['freeze', '--output', 'release-build.json']))
  assert.deepEqual(frozen, metadata)
  assert.deepEqual(JSON.parse(await readFile(path.join(root, 'release-build.json'), 'utf8')), metadata)
  assert.equal(await readFile(output, 'utf8'), `version=0.1.14\ntag=v0.1.14\nsource_sha=${metadata.source_sha}\n`)
  assert.throws(() => run(['freeze', '--output', 'release-build.json']), /EEXIST/)
  assert.equal(git(['diff', '--name-only']).trim(), '')
  assert.deepEqual(JSON.parse(run(['apply', '--metadata', 'release-build.json'])), { version: '0.1.14', tag: 'v0.1.14', source_sha: metadata.source_sha })
  assert.equal(await readReleaseVersion(root, {}), metadata.version)
})
