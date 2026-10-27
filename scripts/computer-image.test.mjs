import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import test from 'node:test'
import { buildEnvironment } from './computer-image-build.mjs'
import { imageRepository, platforms, recipeHash, registryReader, validateLock, verifyEnvironmentLock } from './computer-image.mjs'

const recipeSHA = 'a'.repeat(64)
const sourceSHA = 'b'.repeat(40)
const digest = bytes => `sha256:${createHash('sha256').update(bytes).digest('hex')}`

function fixture({ architectures = ['amd64', 'arm64'], entrypoint = null, cmd = ['/bin/bash'], movedTag = false, badDigest = false, privateImage = false } = {}) {
  const bodies = new Map()
  const descriptors = architectures.map(architecture => {
    const config = Buffer.from(JSON.stringify({ os: 'linux', architecture, config: { Entrypoint: entrypoint, Cmd: cmd, Labels: {
      'io.astral.environment.recipe-sha256': recipeSHA, 'io.astral.environment.kind': 'agent-tools', 'org.opencontainers.image.revision': sourceSHA,
    } } }))
    const configDigest = digest(config)
    bodies.set(`blobs/${configDigest}`, config)
    const manifest = Buffer.from(JSON.stringify({ schemaVersion: 2, config: { digest: configDigest }, layers: [] }))
    const manifestDigest = digest(manifest)
    bodies.set(`manifests/${manifestDigest}`, manifest)
    return { digest: manifestDigest, size: manifest.length, platform: { os: 'linux', architecture } }
  })
  const index = Buffer.from(JSON.stringify({ schemaVersion: 2, manifests: descriptors }))
  const indexDigest = digest(index)
  const tag = `env-${recipeSHA.slice(0, 12)}-123-1`
  bodies.set(`manifests/${indexDigest}`, index)
  bodies.set(`manifests/${tag}`, movedTag ? Buffer.from(JSON.stringify({ schemaVersion: 2, manifests: descriptors, annotations: { changed: 'yes' } })) : index)
  const lock = { schema_version: 1, image: imageRepository, tag, digest: indexDigest, recipe_sha256: recipeSHA, platforms, source_sha: sourceSHA, build_run_url: 'https://github.com/oines/astral/actions/runs/123', created_at: '2026-10-27T00:00:00.000Z' }
  const calls = []
  const request = async (url, options) => {
    calls.push({ url, options })
    if (url.startsWith('https://ghcr.io/token?')) return new globalThis.Response(JSON.stringify({ token: 'fixture-pull-token' }), { status: privateImage ? 403 : 200 })
    assert.equal(options.headers.Authorization, 'Bearer fixture-pull-token')
    assert.equal(options.redirect, 'manual')
    const suffix = url.replace('https://ghcr.io/v2/oines/astral-computer/', '')
    const bytes = bodies.get(suffix)
    assert.ok(bytes, `unexpected registry request: ${suffix}`)
    return new globalThis.Response(bytes, { headers: { 'docker-content-digest': badDigest ? `sha256:${'0'.repeat(64)}` : digest(bytes) } })
  }
  return { lock, request, calls }
}

test('the recipe digest covers exact bytes and rejects local application inputs', () => {
  const bytes = Buffer.from('FROM ubuntu:24.04\nCOPY --from=uv /uv /usr/local/bin/uv\n')
  assert.equal(recipeHash(bytes), digest(bytes).slice(7))
  assert.notEqual(recipeHash(bytes), recipeHash(Buffer.concat([bytes, Buffer.from('\n')])))
  for (const command of ['COPY astral /usr/local/bin/astral', 'COPY --chmod=755 astral /usr/local/bin/astral', 'ADD https://example.test/astral /usr/local/bin/', 'ENTRYPOINT ["astral"]']) {
    assert.throws(() => recipeHash(Buffer.from(`FROM ubuntu\n${command}\n`)), /only COPY|ENTRYPOINT/)
  }
})

test('a complete public two-architecture environment is verified without pulling layers or sending credentials', async () => {
  const f = fixture()
  const result = await verifyEnvironmentLock(f.lock, recipeSHA, { request: f.request, environment: { GHCR_USERNAME: 'owner', GHCR_TOKEN: 'PRIVATE-TOKEN' }, authenticated: true })
  assert.equal(result.digest, f.lock.digest)
  assert.deepEqual(result.records.map(record => record.architecture), ['amd64', 'arm64'])
  assert.deepEqual(f.calls[0].options.headers, {})
  assert.equal(f.calls.length, 7)
  assert.ok(f.calls.every(call => !JSON.stringify(call).includes('PRIVATE-TOKEN')))
})

test('a private package fails the anonymous distribution gate even with publication credentials available', async () => {
  const f = fixture({ privateImage: true })
  await assert.rejects(verifyEnvironmentLock(f.lock, recipeSHA, { request: f.request, environment: { GHCR_USERNAME: 'owner', GHCR_TOKEN: 'PRIVATE-TOKEN' } }), /anonymous access failed/)
  assert.deepEqual(f.calls[0].options.headers, {})
  assert.equal(f.calls.length, 1)
})

test('missing, malformed and stale recipe locks are rejected before registry access', async () => {
  const f = fixture()
  for (const lock of [undefined, { ...f.lock, schema_version: 2 }, { ...f.lock, digest: 'latest' }, { ...f.lock, image: 'private.example/environment' }, { ...f.lock, platforms: ['linux/amd64'] }, { ...f.lock, recipe_sha256: 'c'.repeat(64) }, { ...f.lock, tag: 'latest' }, { ...f.lock, source_sha: 'private-branch' }, { ...f.lock, build_run_url: 'https://github.com/oines/astral/actions/runs/999' }, { ...f.lock, created_at: 'not-a-date' }]) {
    assert.throws(() => validateLock(lock, recipeSHA), /lock is missing, malformed/)
    await assert.rejects(verifyEnvironmentLock(lock, recipeSHA, { request: () => assert.fail('bad lock reached the registry') }), /lock is missing, malformed/)
  }
})

test('single-platform, extra-platform and daemon entrypoints or commands cannot pass the published check', async () => {
  for (const mutation of [
    { architectures: ['amd64'] }, { architectures: ['amd64', 'arm64', 'unknown'] },
    { entrypoint: ['/usr/local/bin/astral'] }, { cmd: ['/bin/bash', '-c', 'astral daemon'] },
  ]) {
    const f = fixture(mutation)
    await assert.rejects(verifyEnvironmentLock(f.lock, recipeSHA, { request: f.request }), /exactly native|daemon-free/)
  }
})

test('registry byte mismatches and a moved immutable tag are refused', async () => {
  for (const mutation of [{ badDigest: true }, { movedTag: true }]) {
    const f = fixture(mutation)
    await assert.rejects(verifyEnvironmentLock(f.lock, recipeSHA, { request: f.request }), /immutable digest|immutable environment tag/i)
  }
})

test('publication authentication is explicit and never embedded in errors', async () => {
  const secret = 'DO-NOT-PRINT-TOKEN'
  await assert.rejects(registryReader({ authenticated: true, environment: {}, request: () => assert.fail('missing credentials reached registry') }), /job-scoped/)
  await assert.rejects(registryReader({ authenticated: true, environment: { GHCR_USERNAME: 'owner', GHCR_TOKEN: secret }, request: async (_url, options) => {
    assert.equal(options.headers.Authorization, `Basic ${Buffer.from(`owner:${secret}`).toString('base64')}`)
    return new globalThis.Response('', { status: 403 })
  } }), error => !error.message.includes(secret) && /publication access failed/.test(error.message))
})

test('the build command refuses non-native execution before calling Docker', async () => {
  await assert.rejects(buildEnvironment({ architecture: 'not-native' }), /matching native Linux CI runner/)
})

test('GHCR config redirects only follow the HTTPS GitHub CDN without forwarding registry authentication', async () => {
  const bytes = Buffer.from('{"architecture":"amd64"}')
  const reference = digest(bytes)
  const calls = []
  const reader = await registryReader({ request: async (url, options) => {
    calls.push({ url, options })
    if (url.includes('/token?')) return new globalThis.Response(JSON.stringify({ token: 'PRIVATE-PULL-TOKEN' }))
    if (url.startsWith('https://ghcr.io/v2/')) return new globalThis.Response(null, { status: 307, headers: { Location: 'https://pkg-containers.githubusercontent.com/ghcr1/fixture?signature=private-cdn-token' } })
    assert.deepEqual(options.headers, { Accept: 'application/octet-stream' })
    assert.equal(options.redirect, 'manual')
    return new globalThis.Response(bytes)
  } })
  assert.deepEqual((await reader.config(reference)).value, { architecture: 'amd64' })
  assert.equal(calls.length, 3)
  assert.ok(!JSON.stringify(calls[2].options).includes('PRIVATE-PULL-TOKEN'))
})

test('untrusted CDN targets and manifest redirects fail without following them', async () => {
  for (const [kind, location] of [
    ['config', 'http://pkg-containers.githubusercontent.com/fixture'],
    ['config', 'https://untrusted.example/fixture'],
    ['config', 'https://name:password@pkg-containers.githubusercontent.com/fixture'],
    ['manifest', 'https://pkg-containers.githubusercontent.com/fixture'],
  ]) {
    let calls = 0
    const reader = await registryReader({ request: async url => {
      calls++
      if (url.includes('/token?')) return new globalThis.Response(JSON.stringify({ token: 'fixture' }))
      return new globalThis.Response(null, { status: 307, headers: { Location: location } })
    } })
    await assert.rejects(reader[kind](`sha256:${'c'.repeat(64)}`), /Untrusted environment registry redirect/)
    assert.equal(calls, 2)
  }
})
