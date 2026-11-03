import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { writeFileSync } from 'node:fs'
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { crc32 } from 'node:zlib'
import test from 'node:test'
import { downloadArtifact, resolveInputs } from './rolling-release-inputs.mjs'

// Store-only ZIP fixtures and injected HTTP. No GitHub, credentials, or builds.
function zip(entries) {
  const local = [], central = []
  let offset = 0
  for (const { name, value = '', kind = 'file' } of entries) {
    const filename = Buffer.from(name), bytes = Buffer.from(value), crc = crc32(bytes)
    const header = Buffer.alloc(30)
    header.writeUInt32LE(0x04034b50); header.writeUInt16LE(20, 4); header.writeUInt16LE(0x800, 6)
    header.writeUInt16LE(12 << 11, 10); header.writeUInt16LE((46 << 9) | (11 << 5) | 3, 12)
    header.writeUInt32LE(crc, 14); header.writeUInt32LE(bytes.length, 18); header.writeUInt32LE(bytes.length, 22); header.writeUInt16LE(filename.length, 26)
    local.push(header, filename, bytes)
    const record = Buffer.alloc(46)
    record.writeUInt32LE(0x02014b50); record.writeUInt16LE(0x0314, 4); record.writeUInt16LE(20, 6); record.writeUInt16LE(0x800, 8)
    record.writeUInt16LE(12 << 11, 12); record.writeUInt16LE((46 << 9) | (11 << 5) | 3, 14)
    record.writeUInt32LE(crc, 16); record.writeUInt32LE(bytes.length, 20); record.writeUInt32LE(bytes.length, 24); record.writeUInt16LE(filename.length, 28)
    record.writeUInt32LE(((kind === 'symlink' ? 0xa1ff : 0x81a4) << 16) >>> 0, 38); record.writeUInt32LE(offset, 42)
    central.push(record, filename); offset += header.length + filename.length + bytes.length
  }
  const index = Buffer.concat(central), end = Buffer.alloc(22)
  end.writeUInt32LE(0x06054b50); end.writeUInt16LE(entries.length, 8); end.writeUInt16LE(entries.length, 10); end.writeUInt32LE(index.length, 12); end.writeUInt32LE(offset, 16)
  return Buffer.concat([...local, index, end])
}
const sha256 = bytes => createHash('sha256').update(bytes).digest('hex')
const clone = value => JSON.parse(JSON.stringify(value))
async function fixture(t, { runID = 501, runNumber = 1, runAttempt = 1, metadataAttempt = 1, entries, incompleteAttempt = false } = {}) {
  const root = await mkdtemp(path.join(tmpdir(), 'astral-input-fixture-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const environment = { GITHUB_REPOSITORY: 'oines/astral', GH_TOKEN: 'fixture-only-not-a-credential', GITHUB_OUTPUT: path.join(root, 'outputs') }
  const run = { id: runID, repository: { full_name: 'oines/astral' }, head_repository: { full_name: 'oines/astral' }, path: '.github/workflows/prepare-release.yml', head_branch: 'main', event: 'push', head_sha: 'a'.repeat(40), run_number: runNumber, run_attempt: runAttempt, status: 'completed', conclusion: 'success' }
  const metadata = { schema_version: 1, repository: 'oines/astral', source_sha: run.head_sha, version: `0.1.${13 + runNumber}`, build_run_id: runID, build_run_attempt: metadataAttempt, build_run_number: runNumber, build_run_url: `https://github.com/oines/astral/actions/runs/${runID}`, workflow_path: '.github/workflows/prepare-release.yml' }
  const bytes = zip(entries || [{ name: 'release-build.json', value: JSON.stringify(metadata) }])
  const payloads = new Map([[101, bytes], [102, zip([{ name: 'astral-linux-amd64', value: 'Fixture binary' }])]])
  const artifacts = [
    { id: 101, name: `build-metadata-${metadataAttempt}`, expired: false, size_in_bytes: bytes.length, digest: `sha256:${sha256(bytes)}`, workflow_run: { id: runID, head_sha: run.head_sha } },
    { id: 102, name: `unsigned-distribution-${metadataAttempt}`, expired: false, size_in_bytes: payloads.get(102).length, digest: `sha256:${sha256(payloads.get(102))}`, workflow_run: { id: runID, head_sha: run.head_sha } },
  ]
  if (incompleteAttempt) artifacts.push({ ...clone(artifacts[0]), id: 103, name: `build-metadata-${runAttempt}` })
  const requests = [], recent = [run], ancestry = [], tagState = {}
  const request = async (input, options = {}) => {
    const url = new URL(input)
    requests.push({ url: url.href, options })
    if (url.hostname === 'artifact-store.example.test') {
      assert.equal(options.headers?.Authorization, undefined, 'Never send the GitHub token to artifact storage')
      return new globalThis.Response(payloads.get(Number(url.pathname.slice(1))))
    }
    assert.equal(url.hostname, 'api.github.com')
    assert.equal(options.headers.Authorization, 'Bearer fixture-only-not-a-credential')
    assert.equal(options.redirect, 'manual')
    const route = url.pathname.replace('/repos/oines/astral', '')
    let value
    if (route === `/actions/runs/${runID}`) value = run
    else if (route === '/actions/workflows/prepare-release.yml/runs') value = { workflow_runs: recent }
    else if (route === `/actions/runs/${runID}/artifacts`) value = { artifacts }
    else if (/^\/actions\/artifacts\/\d+$/.test(route)) value = artifacts.find(item => item.id === Number(route.split('/').at(-1)))
    else if (/^\/actions\/artifacts\/\d+\/zip$/.test(route)) return new globalThis.Response(null, { status: 302, headers: { location: `https://artifact-store.example.test/${route.split('/').at(-2)}` } })
    else if (route === `/git/ref/tags/v${metadata.version}`) return new globalThis.Response(JSON.stringify(tagState.reference || {}), { status: tagState.reference ? 200 : 404 })
    else if (tagState.object && route === `/git/tags/${tagState.reference.object.sha}`) value = tagState.object
    else assert.fail('Unexpected API route: ' + route)
    return new globalThis.Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } })
  }
  const options = { runID, outputDirectory: path.join(root, 'resolution'), environment, fetch: request, verifySource: source => ancestry.push(source) }
  return { root, run, metadata, artifacts, payloads, requests, recent, ancestry, options, request, environment, tagState }
}

test('resolve binds immutable artifact IDs and frozen metadata to its exact successful main producer', async t => {
  const f = await fixture(t)
  const result = await resolveInputs(f.options)
  assert.equal(result.eligible, true)
  assert.equal(result.build_run_id, 501)
  assert.equal(result.version, '0.1.14')
  assert.equal(result.tag, 'v0.1.14')
  assert.equal(result.build_metadata_artifact_id, 101)
  assert.equal(result.unsigned_artifact_id, 102)
  assert.equal(result.build_metadata_artifact_digest, f.artifacts[0].digest)
  assert.deepEqual(result.build_metadata, f.metadata)
  assert.deepEqual(f.ancestry, [f.run.head_sha])
  assert.deepEqual(JSON.parse(await readFile(path.join(f.options.outputDirectory, 'resolution.json'), 'utf8')), result)
  assert.match(await readFile(f.environment.GITHUB_OUTPUT, 'utf8'), /^eligible=true\nbuild_run_id=501\nsource_sha=/)
})

test('a newer Prepare request supersedes queued older input without downloading artifacts', async t => {
  const f = await fixture(t)
  f.recent.unshift({ ...f.run, id: 502, run_number: 2, status: 'in_progress', conclusion: null })
  const result = await resolveInputs(f.options)
  assert.equal(result.eligible, false)
  assert.equal(f.requests.length, 2)
})

test('successful CI retry uses the latest complete pair and ignores an incomplete later attempt', async t => {
  const f = await fixture(t, { runAttempt: 2, metadataAttempt: 1, incompleteAttempt: true })
  const result = await resolveInputs(f.options)
  assert.equal(result.eligible, true)
  assert.equal(result.build_metadata.build_run_attempt, 1)
  assert.equal(result.build_metadata_artifact_id, 101)
})

test('untrusted or unsuccessful producer runs fail before artifact access', async t => {
  for (const change of [
    run => { run.head_repository.full_name = 'fork/astral' },
    run => { run.repository.full_name = 'fork/astral' },
    run => { run.head_branch = 'feature' },
    run => { run.path = '.github/workflows/other.yml' },
    run => { run.event = 'pull_request' },
    run => { run.status = 'in_progress' },
    run => { run.conclusion = 'failure' },
    run => { run.head_sha = 'not-a-sha' },
  ]) {
    const f = await fixture(t)
    change(f.run)
    await assert.rejects(resolveInputs(f.options))
    assert.equal(f.requests.length, 1)
  }
  const f = await fixture(t)
  f.options.verifySource = () => { throw new Error('not main history') }
  await assert.rejects(resolveInputs(f.options), /not main history/)
  assert.equal(f.requests.length, 1)
})

test('missing, ambiguous, expired and foreign-source artifact pairs are rejected', async t => {
  for (const change of [
    f => { f.artifacts.pop() },
    f => { f.artifacts.push(clone(f.artifacts[0])) },
    f => { f.artifacts[0].expired = true },
    f => { f.artifacts[0].workflow_run.head_sha = 'b'.repeat(40) },
    f => { f.artifacts[1].workflow_run.id = 500 },
    f => { f.artifacts[0].digest = 'sha256:invalid' },
  ]) {
    const f = await fixture(t)
    change(f)
    await assert.rejects(resolveInputs(f.options), /artifact|Artifact|frozen metadata/)
  }
})

test('metadata from the right archive must still match the selected producer and attempt', async t => {
  for (const change of [
    metadata => { metadata.source_sha = 'b'.repeat(40) },
    metadata => { metadata.build_run_number = 2; metadata.version = '0.1.15' },
    metadata => { metadata.build_run_id = 502; metadata.build_run_url = metadata.build_run_url.replace('501', '502') },
    metadata => { metadata.build_run_attempt = 2 },
  ]) {
    const f = await fixture(t)
    change(f.metadata)
    const bytes = zip([{ name: 'release-build.json', value: JSON.stringify(f.metadata) }])
    f.payloads.set(101, bytes); f.artifacts[0].size_in_bytes = bytes.length; f.artifacts[0].digest = `sha256:${sha256(bytes)}`
    await assert.rejects(resolveInputs(f.options), /metadata does not match/)
  }
})

test('streamed download rejects altered bytes before extraction and preserves existing output', async t => {
  const f = await fixture(t)
  f.payloads.set(102, Buffer.from('Tampered archive'))
  const output = path.join(f.root, 'retained')
  await mkdir(output); await writeFile(path.join(output, 'sentinel'), 'keep')
  await assert.rejects(downloadArtifact({ artifactID: 102, runID: 501, output, expectedSHA: f.run.head_sha, environment: f.environment, fetch: f.request }), /immutable GitHub SHA256 digest/)
  assert.equal(await readFile(path.join(output, 'sentinel'), 'utf8'), 'keep')
})

test('download by immutable ID rejects the wrong run or source', async t => {
  const f = await fixture(t)
  for (const args of [{ runID: 500 }, { runID: 501, expectedSHA: 'b'.repeat(40) }]) {
    await assert.rejects(downloadArtifact({ artifactID: 102, output: path.join(f.root, 'out'), environment: f.environment, fetch: f.request, ...args }), /identity, source/)
  }
})

test('valid download extracts the exact artifact and keeps GitHub authentication off redirects', async t => {
  const f = await fixture(t)
  const output = path.join(f.root, 'out')
  const result = await downloadArtifact({ artifactID: 102, runID: 501, output, expectedSHA: f.run.head_sha, environment: f.environment, fetch: f.request })
  assert.equal(result.id, 102)
  assert.equal(await readFile(path.join(output, 'astral-linux-amd64'), 'utf8'), 'Fixture binary')
  assert.deepEqual(await readdir(output), ['astral-linux-amd64'])
})

test('artifact extraction rejects traversal, aliases, duplicate paths and symbolic links', async t => {
  for (const entries of [
    [{ name: '../escape', value: 'bad' }], [{ name: '/absolute', value: 'bad' }],
    [{ name: 'C:/drive', value: 'bad' }], [{ name: 'nested\\escape', value: 'bad' }],
    [{ name: 'name.', value: 'bad' }], [{ name: 'name ', value: 'bad' }], [{ name: 'CON.txt', value: 'bad' }],
    [{ name: 'same', value: 'a' }, { name: 'SAME', value: 'b' }],
    [{ name: 'link', value: '../escape', kind: 'symlink' }],
  ]) {
    const f = await fixture(t, { entries })
    await assert.rejects(downloadArtifact({ artifactID: 101, runID: 501, output: path.join(f.root, 'out'), environment: f.environment, fetch: f.request }), /Unsafe|duplicate path, link|entry types/)
    await assert.rejects(readFile(path.join(f.root, 'escape')), /ENOENT/)
  }
})


test('Windows extraction passes paths as environment data to fixed PowerShell programs', async t => {
  const f = await fixture(t)
  const output = path.join(f.root, 'space and $literal target')
  const commands = []
  const execute = (program, args, options) => {
    commands.push({ program, args })
    assert.equal(program, 'pwsh')
    assert.deepEqual(args.slice(0, 3), ['-NoProfile', '-NonInteractive', '-Command'])
    assert.ok(!args.includes(output))
    assert.match(options.env.ASTRAL_ARCHIVE_ZIP, /artifact\.zip$/)
    if (args[3].includes('OpenRead')) return JSON.stringify([{ name: 'astral-linux-amd64', type: 'file' }])
    assert.match(args[3], /Expand-Archive -LiteralPath \$env:ASTRAL_ARCHIVE_ZIP -DestinationPath \$env:ASTRAL_ARCHIVE_OUTPUT/)
    assert.equal(options.env.ASTRAL_ARCHIVE_OUTPUT, output)
    writeFileSync(path.join(output, 'astral-linux-amd64'), 'Fixture binary')
  }
  await downloadArtifact({ artifactID: 102, runID: 501, output, expectedSHA: f.run.head_sha, environment: f.environment, fetch: f.request, platform: 'win32', execute })
  assert.equal(commands.length, 2)
  assert.equal(await readFile(path.join(output, 'astral-linux-amd64'), 'utf8'), 'Fixture binary')
})


test('out-of-order success, cancelled and failed wake hints select the latest Prepare after the lock', async t => {
  for (const hintConclusion of ['success', 'cancelled', 'failure']) {
    const f = await fixture(t, { runID: 503, runNumber: 3 })
    f.options.runID = 501
    f.environment.ASTRAL_RELEASE_SELECT_LATEST = 'true'
    const old = { ...f.run, id: 501, run_number: 1, conclusion: hintConclusion }
    const middle = { ...f.run, id: 502, run_number: 2, conclusion: 'success' }
    f.recent.splice(0, f.recent.length, middle, old, f.run)
    const result = await resolveInputs(f.options)
    assert.equal(result.eligible, true)
    assert.equal(result.build_run_id, 503)
    assert.equal(result.version, '0.1.16')
    assert.equal(result.build_metadata.build_run_id, 503)
    assert.match(f.requests[0].url, /prepare-release\.yml\/runs\?/)
    assert.match(f.requests[1].url, /actions\/runs\/503$/)
    assert.ok(!f.requests.some(item => item.url.includes('/actions/runs/501')))
    assert.match(await readFile(f.environment.GITHUB_OUTPUT, 'utf8'), /^eligible=true\nbuild_run_id=503\n/)
  }
})

test('an unfinished or failed latest Prepare blocks publication instead of falling back to older success', async t => {
  for (const [status, conclusion] of [['in_progress', null], ['completed', 'failure'], ['completed', 'cancelled'], ['completed', 'timed_out']]) {
    const f = await fixture(t, { runID: 503, runNumber: 3 })
    f.environment.ASTRAL_RELEASE_SELECT_LATEST = 'true'
    f.options.runID = 501
    f.run.status = status; f.run.conclusion = conclusion
    f.recent.push({ ...f.run, id: 502, run_number: 2, status: 'completed', conclusion: 'success' })
    const result = await resolveInputs(f.options)
    assert.equal(result.eligible, false)
    assert.equal(result.reason, 'not_ready')
    assert.equal(result.build_run_id, 503)
    assert.equal(f.requests.length, 2)
    assert.ok(!f.requests.some(item => item.url.includes('/artifacts')))
  }
})

function alreadyTagged(f) {
  f.tagState.reference = { ref: `refs/tags/v${f.metadata.version}`, object: { type: 'tag', sha: 'e'.repeat(40) } }
  f.tagState.object = { tag: `v${f.metadata.version}`, sha: 'e'.repeat(40), object: { type: 'commit', sha: f.run.head_sha } }
}

test('duplicate wakes and manual requests retain the exact existing source tag without a second publication', async t => {
  for (const selectLatest of [false, true]) {
    const f = await fixture(t)
    if (selectLatest) f.environment.ASTRAL_RELEASE_SELECT_LATEST = 'true'
    alreadyTagged(f)
    const result = await resolveInputs(f.options)
    assert.equal(result.eligible, false)
    assert.equal(result.reason, 'already_tagged')
    assert.equal(result.build_run_id, 501)
    assert.equal(result.tag, 'v0.1.14')
    assert.ok(f.requests.every(item => (item.options.method || 'GET') === 'GET'))
    assert.match(await readFile(f.environment.GITHUB_OUTPUT, 'utf8'), /^eligible=false\nreason=already_tagged\nbuild_run_id=501\n/)
  }
})

test('existing lightweight or different-source tags cannot silently deduplicate a release', async t => {
  for (const change of [
    f => { f.tagState.reference.object.type = 'commit' },
    f => { f.tagState.object.object.sha = 'b'.repeat(40) },
    f => { f.tagState.object.object.type = 'tag' },
    f => { f.tagState.object.tag = 'v0.1.15' },
    f => { f.tagState.object.sha = 'b'.repeat(40) },
  ]) {
    const f = await fixture(t)
    f.environment.ASTRAL_RELEASE_SELECT_LATEST = 'true'
    alreadyTagged(f); change(f)
    await assert.rejects(resolveInputs(f.options), /conflicting tag|conflicts with/)
    assert.ok(f.requests.every(item => (item.options.method || 'GET') === 'GET'))
  }
})

test('latest selection still rejects an untrusted newest producer and never chooses the trusted hint', async t => {
  const f = await fixture(t, { runID: 503, runNumber: 3 })
  f.environment.ASTRAL_RELEASE_SELECT_LATEST = 'true'
  f.options.runID = 501
  f.run.head_repository.full_name = 'fork/astral'
  f.recent.push({ ...f.run, id: 501, run_number: 1, head_repository: { full_name: 'oines/astral' } })
  await assert.rejects(resolveInputs(f.options), /trusted main Prepare/)
  assert.equal(f.requests.length, 2)
})
