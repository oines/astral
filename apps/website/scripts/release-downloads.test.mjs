import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { setImmediate as nextTurn } from 'node:timers/promises'
import vm from 'node:vm'
import { desktopArtifactName } from '../../../scripts/desktop-update-feeds.mjs'

const source = await readFile(new URL('../public/scripts/release-downloads.js', import.meta.url), 'utf8')
const repository = 'https://github.com/oines/astral'
const targets = [
  ['mac-arm64-dmg', 'mac', 'arm64', 'dmg'],
  ['mac-x64-dmg', 'mac', 'x64', 'dmg'],
  ['win-x64-exe', 'win', 'x64', 'exe'],
  ['win-arm64-exe', 'win', 'arm64', 'exe'],
  ['linux-x64-appimage', 'linux', 'x64', 'AppImage'],
  ['linux-arm64-appimage', 'linux', 'arm64', 'AppImage'],
  ['linux-x64-deb', 'linux', 'x64', 'deb'],
  ['linux-arm64-deb', 'linux', 'arm64', 'deb'],
]
const urlFor = (version, [, platform, arch, extension]) =>
  `${repository}/releases/download/v${version}/${desktopArtifactName(version, platform, arch, extension)}`

function release(version = '0.1.1') {
  return {
    tag_name: `v${version}`, draft: false, prerelease: false,
    published_at: '2026-10-27T12:00:00Z',
    assets: targets.map(target => ({
      name: desktopArtifactName(version, ...target.slice(1)), state: 'uploaded', size: 1024,
      browser_download_url: urlFor(version, target),
    })),
  }
}

function fixture() {
  const label = { dataset: { fallbackVersion: '0.1.0', latestPrefix: 'Latest release: ' }, isConnected: true, textContent: 'Available version: v0.1.0' }
  const notes = { href: `${repository}/releases/tag/v0.1.0` }
  const links = new Map(targets.map(target => [target[0], [{ href: urlFor('0.1.0', target) }]]))
  const events = new Map()
  const document = {
    querySelector: selector => ({ '[data-astral-release-status]': label, '[data-astral-release-notes]': notes })[selector],
    querySelectorAll: selector => links.get(/^\[data-astral-download="([^"]+)"\]$/.exec(selector)?.[1]) || [],
    addEventListener: (name, callback) => { events.set(name, callback) },
  }
  return { label, notes, links, document, events }
}

const settle = () => nextTurn()
function assertFallback(page) {
  assert.equal(page.label.textContent, 'Available version: v0.1.0')
  assert.equal(page.notes.href, `${repository}/releases/tag/v0.1.0`)
  for (const target of targets) assert.equal(page.links.get(target[0])[0].href, urlFor('0.1.0', target))
}

async function run(page, response, status = 200) {
  const requests = []
  vm.runInNewContext(source, {
    document: page.document, AbortSignal,
    fetch: async (...args) => {
      requests.push(args)
      if (response instanceof Error) throw response
      return { ok: status === 200, json: async () => response }
    },
  })
  await settle()
  return requests
}

test('both languages retain eight usable versioned downloads without JavaScript', async () => {
  let fallbackVersion
  for (const language of ['docs', 'zh-cn/docs']) {
    const page = await readFile(new URL(`../src/content/docs/${language}/start/get-astral.md`, import.meta.url), 'utf8')
    const version = /data-fallback-version="([^"]+)"/.exec(page)?.[1]
    assert.match(version || '', /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/)
    fallbackVersion ??= version
    assert.equal(version, fallbackVersion, 'both languages must identify the same published fallback')
    const links = [...page.matchAll(/data-astral-download="([^"]+)" href="([^"]+)"/g)]
    assert.equal(links.length, 8)
    for (const target of targets) assert.deepEqual(links.filter(match => match[1] === target[0]).map(match => match[2]), [urlFor(version, target)])
    assert.deepEqual([...page.matchAll(/data-astral-release-notes href="([^"]+)"/g)].map(match => match[1]), [`${repository}/releases/tag/v${version}`])
    assert.deepEqual([...page.matchAll(/\]\((https:\/\/github\.com\/oines\/astral\/releases\/download\/[^)]+\/mac-signing-report\.json)\)/g)].map(match => match[1]), [`${repository}/releases/download/v${version}/mac-signing-report.json`])
    assert.match(page, /schema-3-core-11/)
    assert.match(page, /Host 3 \/ Core 11/)
    assert.match(page, /releases\/latest\/download\/install-server\.sh/)
  }
})

test('a complete newer stable release updates all links and the matching version together', async () => {
  const page = fixture()
  const requests = await run(page, release())
  assert.equal(requests.length, 1)
  assert.equal(requests[0][0], 'https://api.github.com/repos/oines/astral/releases/latest')
  assert.equal(requests[0][1].credentials, 'omit')
  assert.equal(requests[0][1].redirect, 'error')
  assert.equal(page.label.textContent, 'Latest release: v0.1.1')
  assert.equal(page.notes.href, `${repository}/releases/tag/v0.1.1`)
  for (const target of targets) assert.equal(page.links.get(target[0])[0].href, urlFor('0.1.1', target))
  page.events.get('astro:page-load')()
  await settle()
  assert.equal(requests.length, 1, 'the same page is checked once')
})

test('Latest never replaces the fallback with an incomplete or untrusted release', async t => {
  const cases = [
    ['missing one package', value => { value.assets.pop() }],
    ['draft', value => { value.draft = true }],
    ['prerelease', value => { value.prerelease = true }],
    ['missing publication date', value => { delete value.published_at }],
    ['invalid publication date', value => { value.published_at = 'invalid' }],
    ['non-semver tag', value => { value.tag_name = 'nightly' }],
    ['leading-zero tag', value => { value.tag_name = 'v0.01.1' }],
    ['oversized version', value => { value.tag_name = 'v9007199254740992.0.0' }],
    ['duplicate package', value => { value.assets.push(value.assets[0]) }],
    ['unfinished package', value => { value.assets[0].state = 'new' }],
    ['empty package', value => { value.assets[0].size = 0 }],
    ['wrong repository', value => { value.assets[0].browser_download_url = value.assets[0].browser_download_url.replace('/oines/', '/other/') }],
    ['mutable package URL', value => { value.assets[0].browser_download_url = value.assets[0].browser_download_url.replace('/download/v0.1.1/', '/latest/download/') }],
  ]
  for (const [name, change] of cases) await t.test(name, async () => {
    const page = fixture()
    const value = release()
    change(value)
    await run(page, value)
    assertFallback(page)
  })
})

test('older release, API failure and timeout preserve the explicitly labelled fallback', async () => {
  for (const [value, status] of [[release('0.0.9'), 200], [release(), 403], [release(), 404], [new Error('Request timed out'), 200]]) {
    const page = fixture()
    await run(page, value, status)
    assertFallback(page)
  }
})

test('a validated fallback version can accurately be labelled Latest', async () => {
  const page = fixture()
  await run(page, release('0.1.0'))
  assert.equal(page.label.textContent, 'Latest release: v0.1.0')
  for (const target of targets) assert.equal(page.links.get(target[0])[0].href, urlFor('0.1.0', target))
})

test('navigation during the request cannot mutate detached download links', async () => {
  const page = fixture()
  let respond
  const response = new Promise(resolve => { respond = resolve })
  vm.runInNewContext(source, {
    document: page.document, AbortSignal,
    fetch: () => response,
  })
  page.label.isConnected = false
  respond({ ok: true, json: async () => release() })
  await settle()
  assertFallback(page)
})

test('unexpected duplicate page links stop the refresh before requesting a release', async () => {
  const page = fixture()
  page.links.get(targets[0][0]).push({ href: 'unexpected' })
  const requests = await run(page, release())
  assert.equal(requests.length, 0)
  assertFallback(page)
})
