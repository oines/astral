import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import test from 'node:test'
import { parse } from 'yaml'

const root = fileURLToPath(new URL('../', import.meta.url))
const read = name => readFile(path.join(root, name), 'utf8')
const prepare = parse(await read('.github/workflows/prepare-release.yml'))
const release = parse(await read('.github/workflows/release.yml'))
const ci = parse(await read('.github/workflows/ci.yml'))
const native = parse(await read('.github/actions/native-release-checks/action.yml'))
const script = job => (job.steps || []).map(step => step.run || '').join('\n')
const needs = job => new Set(Array.isArray(job.needs) ? job.needs : [job.needs])

// These are architectural acceptance conditions: the expensive release bytes
// have one producer and a checked, immutable path to publication.
test('Main preparation starts checks and the one distribution build independently', () => {
  assert.deepEqual(prepare.on.push.branches, ['main'])
  assert.equal(prepare.concurrency['cancel-in-progress'], true)
  assert.equal(prepare.jobs.checks.uses, './.github/workflows/ci.yml')
  assert.equal(prepare.jobs.checks.with, undefined)
  assert.equal(prepare.jobs.checks.needs, undefined)
  assert.equal(prepare.jobs.build.needs, undefined)
  assert.equal(script(prepare.jobs.build).match(/pnpm build:distribution/g)?.length, 1)
  assert.ok(script(prepare.jobs.build).indexOf('freeze --output') < script(prepare.jobs.build).indexOf('pnpm build:distribution'))
  assert.ok(script(prepare.jobs.build).indexOf('apply --metadata') < script(prepare.jobs.build).indexOf('pnpm build:distribution'))
  assert.ok(prepare.jobs.build.steps.filter(step => step.uses?.startsWith('actions/upload-artifact@')).every(step => step.with.name.includes('github.run_attempt')))
})

test('protected release coalesces waiting requests and cannot be canceled by a newer merge', () => {
  assert.deepEqual(release.on.workflow_run.workflows, ['Prepare Release'])
  assert.deepEqual(release.on.workflow_run.types, ['completed'])
  assert.equal(release.on.push, undefined, 'Creating the source tag must not rebuild the release')
  assert.equal(release.concurrency['cancel-in-progress'], false)
  assert.equal(release.concurrency.queue ?? 'single', 'single')
  assert.match(script(release.jobs.validate), /rolling-release-inputs\.mjs resolve/)
  assert.doesNotMatch(release.jobs.validate.if, /conclusion/ , 'Completed events are wake-up hints; the selected latest producer is checked by the resolver')
  assert.equal(release.jobs.validate.outputs.build_run_id, '${{ steps.resolve.outputs.build_run_id }}')
  const resolver = release.jobs.validate.steps.find(step => step.id === 'resolve')
  assert.equal(resolver.env.ASTRAL_RELEASE_SELECT_LATEST, "${{ github.event_name == 'workflow_run' && 'true' || 'false' }}")
  assert.equal(release.jobs['mac-programs'].if, "needs.validate.outputs.eligible == 'true'")
  assert.doesNotMatch(Object.values(release.jobs).map(script).join('\n'), /build:distribution|check-release-candidate|release-version\.mjs set/)
})

test('all Desktop platforms use the same signed programs without waiting for Apple DMG notarization', () => {
  for (const name of ['mac', 'desktop', 'containers', 'mac-binaries-notary']) {
    assert.ok(needs(release.jobs[name]).has('mac-programs'))
    assert.ok(!needs(release.jobs[name]).has('mac-binaries-notary'))
    const download = release.jobs[name].steps.find(step => step.env?.ARTIFACT_ID === '${{ needs.mac-programs.outputs.artifact_id }}')
    assert.match(download?.run || '', /--artifact-id "\$ARTIFACT_ID"/)
  }
  assert.deepEqual(release.jobs.mac.strategy.matrix.include, [
    { os: 'macos-15-intel', arch: 'x64' }, { os: 'macos-15', arch: 'arm64' },
  ])
  assert.match(script(release.jobs.mac), /process\.arch !== process\.env\.NATIVE_ARCH/)
  assert.equal(script(release.jobs['mac-binaries-notary']).match(/mac-notarize\.mjs binaries/g)?.length, 1)
})

test('no seal or publisher can bypass any native or container acceptance result', () => {
  for (const dependency of ['validate', 'mac-programs', 'mac-binaries-notary', 'mac', 'desktop', 'containers']) assert.ok(needs(release.jobs.seal).has(dependency))
  assert.deepEqual([...needs(release.jobs.publish)].sort(), ['seal', 'validate'])
  assert.equal(release.jobs.seal.if, undefined, 'Default success gating must remain effective')
  assert.equal(release.jobs.publish.if, undefined)
  assert.match(script(release.jobs.seal), /release-draft\.mjs --verify sealed-release/)
  const attest = release.jobs.seal.steps.find(step => step.uses?.startsWith('actions/attest@'))
  assert.equal(attest.with['subject-path'], 'sealed-release/SHA256SUMS')
  assert.equal(attest.with['predicate-path'], 'sealed-release/release-manifest.json')
  assert.equal(release.jobs.seal.permissions['id-token'], 'write')
  assert.equal(release.jobs.seal.env.RELEASE_SOURCE_SHA, '${{ needs.validate.outputs.source_sha }}')
  assert.equal(release.jobs.publish.env.ASTRAL_RELEASE_BUNDLE_SHA256, '${{ needs.seal.outputs.bundle_sha256 }}')
  const publishing = script(release.jobs.publish)
  const download = release.jobs.publish.steps.find(step => step.env?.ARTIFACT_ID === '${{ needs.seal.outputs.artifact_id }}')
  assert.match(download?.run || '', /--artifact-id "\$ARTIFACT_ID"/)
  assert.ok(publishing.indexOf('gh attestation verify') < publishing.indexOf('rolling-release-tag.mjs'))
  assert.ok(publishing.indexOf('rolling-release-tag.mjs') < publishing.indexOf('release-draft.mjs --public'))
  assert.doesNotMatch(publishing, /release-assets|electron-builder|mac-notarize|sign-binaries|build:/)
})

test('source validation runs in parallel without making packages while release retains native lifecycle checks', () => {
  assert.equal(ci.on.push, undefined)
  assert.deepEqual(ci.on.workflow_call, {})
  assert.equal(ci.jobs.native.needs, undefined)
  assert.doesNotMatch(Object.values(ci.jobs).map(script).join('\n'), /build:distribution|package:mac|package:windows|package:linux/)
  assert.match(script(ci.jobs.check), /openapi-generate[\s\S]*sdk-generate[\s\S]*git diff --exit-code/)
  assert.match(script(ci.jobs.native), /go test -race -timeout=20m \.\/\.\.\./)
  const runtime = native.runs.steps.map(step => step.run || '').join('\n')
  for (const check of ['TestDistributionInstallerRegistersActualDaemon', 'pnpm test:desktop-service', 'pnpm test:daemon-service', 'scripts/check-daemon-update.mjs', '--root-user']) assert.ok(runtime.includes(check), check)
  for (const name of ['mac', 'desktop']) assert.ok(release.jobs[name].steps.some(step => step.uses === './.github/actions/native-release-checks'))
  assert.match(script(release.jobs.containers), /go test -tags=integration -count=1 -timeout=20m/)
  assert.match(script(release.jobs.desktop), /scripts\/check-headless-install\.mjs/)
})

test('public installation and both native Mac updates remain inside the protected release run', () => {
  assert.deepEqual(release.jobs['published-desktop-update'].strategy.matrix.include.map(item => `${item.platform}-${item.arch}`).sort(), ['linux-x64', 'mac-arm64', 'mac-x64', 'win-x64'])
  for (const name of ['published-desktop-update', 'published-install']) assert.ok(needs(release.jobs[name]).has('publish'))
  assert.match(script(release.jobs['published-desktop-update']), /--published-from/)
  assert.match(script(release.jobs['published-install']), /Install|install-server\.sh/)
})

test('new release Actions are pinned and only credentialed Mac jobs access the signing environment', () => {
  for (const workflow of [prepare, release]) for (const [name, job] of Object.entries(workflow.jobs)) {
    if (job.environment === 'mac-signing') assert.ok(['mac-programs', 'mac-binaries-notary', 'mac'].includes(name))
    for (const step of job.steps || []) if (step.uses && !step.uses.startsWith('./')) assert.match(step.uses, /@[a-f0-9]{40}$/, step.uses)
  }
  assert.notEqual(release.jobs.seal.environment, 'mac-signing')
  assert.notEqual(release.jobs.publish.environment, 'mac-signing')
})

test('metadata artifacts stay visible and every metadata consumer can read Actions artifacts', () => {
  for (const step of prepare.jobs.build.steps.filter(step => step.uses?.startsWith('actions/upload-artifact@'))) {
    assert.ok(!step.with.path.split('/').some(part => part.startsWith('.')) || step.with['include-hidden-files'] === true)
  }
  for (const job of Object.values(release.jobs)) if (job.steps?.some(step => step.uses === './.github/actions/release-source')) {
    assert.ok((job.permissions ?? release.permissions).actions === 'read')
  }
})
