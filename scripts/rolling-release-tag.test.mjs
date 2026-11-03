import assert from 'node:assert/strict'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { createReleaseTag, releaseTagMessage } from './rolling-release-tag.mjs'

function fixture({ existing = false, conflict = false } = {}) {
  const manifest = { tag: 'v0.1.14', version: '0.1.14', source_sha: 'a'.repeat(40), packaging_run_url: 'https://github.com/oines/astral/actions/runs/900', build_provenance: { build_run_url: 'https://github.com/oines/astral/actions/runs/800' } }
  const sealSHA256 = 'd'.repeat(64)
  const object = { tag: manifest.tag, sha: 'e'.repeat(40), object: { type: 'commit', sha: manifest.source_sha }, message: releaseTagMessage(manifest, sealSHA256) }
  const reference = { ref: `refs/tags/${manifest.tag}`, object: { type: 'tag', sha: object.sha } }
  const calls = []
  const environment = { GH_TOKEN: 'fixture-only', ASTRAL_RELEASE_BUNDLE_SHA256: sealSHA256 }
  const options = { environment,
    verify: async (directory, args) => {
      assert.equal(directory, 'sealed')
      assert.equal(args.requireSeal, true)
      assert.equal(args.environment, environment)
      return { manifest, sealSHA256, repository: 'oines/astral' }
    },
    fetch: async (url, args) => {
      calls.push({ url, method: args.method, body: args.body && JSON.parse(args.body) })
      assert.equal(args.headers.Authorization, 'Bearer fixture-only')
      assert.equal(args.redirect, 'error')
      if (url.endsWith('/git/ref/tags/v0.1.14')) return new globalThis.Response(JSON.stringify(reference), { status: existing ? 200 : 404 })
      if (url.endsWith('/git/tags/' + reference.object.sha)) return new globalThis.Response(JSON.stringify(object))
      if (url.endsWith('/git/tags')) return new globalThis.Response(JSON.stringify(object), { status: 201 })
      if (url.endsWith('/git/refs')) {
        if (conflict) { existing = true; return new globalThis.Response('{}', { status: 422 }) }
        return new globalThis.Response(JSON.stringify(reference), { status: 201 })
      }
      assert.fail('Unexpected tag API call')
    },
  }
  return { manifest, object, reference, calls, options }
}

test('creates an annotated exact-source tag after verifying the immutable release bundle', async () => {
  const f = fixture()
  const result = await createReleaseTag('sealed', f.options)
  assert.equal(result.created, true)
  assert.deepEqual(f.calls.map(call => call.method), ['GET', 'POST', 'POST'])
  const tag = f.calls[1].body
  assert.equal(tag.object, f.manifest.source_sha)
  assert.equal(tag.type, 'commit')
  assert.match(tag.message, /Source: a{40}\nRelease index SHA256: d{64}\nBuild: https:\/\/github.com\/oines\/astral\/actions\/runs\/800\nPackaging:/)
  assert.equal(tag.tagger.name, 'Astral Release Automation')
  assert.deepEqual(f.calls[2].body, { ref: 'refs/tags/v0.1.14', sha: f.object.sha })
})

test('identical annotated tags are idempotent and never moved', async () => {
  const f = fixture({ existing: true })
  const result = await createReleaseTag('sealed', f.options)
  assert.equal(result.created, false)
  assert.ok(f.calls.every(call => call.method === 'GET'))
})

test('a concurrent identical tag creation is verified without forcing its ref', async () => {
  const f = fixture({ conflict: true })
  const result = await createReleaseTag('sealed', f.options)
  assert.equal(result.created, false)
  assert.deepEqual(f.calls.map(call => call.method), ['GET', 'POST', 'POST', 'GET', 'GET'])
  assert.ok(f.calls.every(call => !['PATCH', 'DELETE'].includes(call.method)))
})

test('existing lightweight tags or different source, message and object SHA are never changed', async () => {
  for (const change of [
    f => { f.reference.object.type = 'commit' },
    f => { f.object.object.sha = 'b'.repeat(40) },
    f => { f.object.object.type = 'tag' },
    f => { f.object.message += 'Changed provenance' },
    f => { f.object.sha = 'c'.repeat(40) },
  ]) {
    const f = fixture({ existing: true }); change(f)
    await assert.rejects(createReleaseTag('sealed', f.options), /will not be moved|differs from its annotated/)
    assert.ok(f.calls.every(call => call.method === 'GET'))
  }
})

test('an invalid or missing producer seal prevents all tag API requests', async () => {
  for (const change of [
    f => { delete f.options.environment.ASTRAL_RELEASE_BUNDLE_SHA256 },
    f => { f.options.environment.ASTRAL_RELEASE_BUNDLE_SHA256 = 'c'.repeat(64) },
    f => { delete f.manifest.build_provenance },
  ]) {
    const f = fixture(); change(f)
    await assert.rejects(createReleaseTag('sealed', f.options), /verified rolling release bundle/)
    assert.equal(f.calls.length, 0)
  }
})

test('the real bundle verifier rejects an unsealed directory before a tag mutation', async t => {
  const directory = await mkdtemp(path.join(tmpdir(), 'astral-tag-unsealed-'))
  t.after(() => rm(directory, { recursive: true, force: true }))
  let calls = 0
  await assert.rejects(createReleaseTag(directory, { environment: { GH_TOKEN: 'fixture-only', GITHUB_REPOSITORY: 'oines/astral' }, fetch: () => { calls++; assert.fail('No tag API request is allowed') } }), /ENOENT/)
  assert.equal(calls, 0)
})
