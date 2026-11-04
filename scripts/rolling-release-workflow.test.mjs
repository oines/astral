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

// Evaluate the actual ordered workflow rules for representative changed paths.
// These patterns use only literals, * and **, without platform-specific paths.
function preparesRelease(changedPaths) {
  return changedPaths.some(file => {
    let included = false
    for (const pattern of prepare.on.push.paths) {
      const excluded = pattern.startsWith('!')
      if (path.posix.matchesGlob(file, excluded ? pattern.slice(1) : pattern)) included = !excluded
    }
    return included
  })
}

test('documentation, website, workflow and test-only changes do not prepare a release', () => {
  assert.equal(prepare.on.push['paths-ignore'], undefined)
  assert.ok(Object.hasOwn(prepare.on, 'workflow_dispatch'), 'Explicit recovery remains available')
  for (const file of [
    'README.md', 'README.zh-CN.md', 'CONTRIBUTING.md', 'SECURITY.md', 'AGENTS.md', 'SPEC.md',
    'docs/releasing.md', 'docs/new-guide.md',
    'apps/website/src/content/docs/docs/start/get-astral.md', 'apps/website/src/pages/index.astro',
    'apps/website/astro.config.mjs', 'apps/website/package.json', 'wrangler.jsonc',
    '.github/workflows/ci.yml', '.github/workflows/prepare-release.yml', '.github/workflows/release.yml',
    '.github/actions/release-source/action.yml', '.github/actions/native-release-checks/action.yml',
    'scripts/check-desktop-update.mjs', 'scripts/desktop-version-acceptance.mjs',
    'scripts/release-draft.mjs', 'scripts/mac-credentials.mjs', 'scripts/build-server.mjs',
    'scripts/rolling-release-inputs.mjs', 'scripts/rolling-release-workflow.test.mjs',
    'internal/harness/subagent_legacy_test.go', 'internal/testkit/shell/shell.go',
    'internal/harness/testdata/history.json', 'apps/web/src/example.test.ts',
    'apps/desktop/src/main/updates.test.ts', 'packages/ui/src/Button.test.ts',
    'packages/ui/tests/fixture.ts', 'packages/sdk/README.md', 'apps/web/AGENTS.md',
    'scripts/build-lib.test.mjs', 'vitest.config.ts', 'eslint.config.mjs',
  ]) assert.equal(preparesRelease([file]), false, file)
})

test('production code, packaged Markdown and build inputs still prepare a release', () => {
  for (const file of [
    'cmd/astral/main.go', 'internal/harness/subagents.go',
    'internal/harness/prompts/im-compaction.md', 'internal/persistence/migrations/00006.sql',
    'internal/platform/installers/install.sh', 'internal/platform/installers/install.ps1',
    'internal/platform/computer-image/image-lock.json', 'internal/platform/computer-image/Dockerfile',
    'internal/platform/web/README.txt', 'internal/timezone/system_windows_LICENSE.txt',
    'apps/web/src/App.vue', 'apps/web/public/favicon.svg', 'apps/web/index.html', 'apps/web/vite.config.ts',
    'apps/desktop/src/main/index.ts', 'apps/desktop/resources/entitlements.mac.plist',
    'apps/desktop/electron-builder.release.cjs', 'apps/desktop/electron-builder.yml',
    'apps/desktop/electron.vite.config.ts', 'apps/desktop/package.json',
    'packages/ui/src/Button.vue', 'packages/sdk/src/client.gen.ts', 'packages/config/tsconfig.json',
    'packages/icons/src/assets/NOTICE.md', 'packages/icons/src/assets/LICENSE-Memoh.txt',
    'packages/icons/assets/astral-mark.svg', 'spec/openapi/astral.json',
    'go.mod', 'go.sum', 'package.json', 'pnpm-lock.yaml', 'pnpm-workspace.yaml',
    'tsconfig.json', 'openapi-ts.config.ts', '.npmrc', '.gitattributes', 'LICENSE', 'THIRD_PARTY.md',
    'scripts/build-distribution.mjs', 'scripts/build-lib.mjs', 'scripts/build-icons.mjs',
    'scripts/build-notices.mjs', 'scripts/install-server.sh', 'scripts/release-version.mjs',
    'scripts/release-assets.mjs', 'scripts/desktop-update-feeds.mjs',
    'scripts/mac-signing.mjs', 'scripts/mac-notarize.mjs',
  ]) assert.equal(preparesRelease([file]), true, file)
})

test('a mixed change or product deletion still prepares a release', () => {
  assert.equal(preparesRelease(['README.md', 'docs/releasing.md', 'internal/harness/subagents.go']), true)
  assert.equal(preparesRelease(['apps/website/package.json', 'pnpm-lock.yaml']), true, 'The shared lock remains a product build input')
  // Deleted paths match just like added/modified paths; a move out of a product
  // directory must retain the removed product path in the changed-file set.
  assert.equal(preparesRelease(['internal/harness/removed.go']), true)
  assert.equal(preparesRelease(['docs/removed.md']), false)
  assert.equal(preparesRelease(['internal/harness/removed.go', 'docs/archived-example.go']), true)
  assert.equal(preparesRelease(['docs/example.go', 'internal/harness/new.go']), true)
  assert.equal(preparesRelease(['docs/old.md', 'docs/new.md']), false)
})

// These are architectural acceptance conditions: the expensive release bytes
// have one producer and a checked, immutable path to publication.
test('Main preparation builds immediately; exact-source checks overlap protected packaging', () => {
  assert.deepEqual(prepare.on.push.branches, ['main'])
  assert.equal(prepare.concurrency['cancel-in-progress'], true)
  assert.equal(prepare.jobs.checks, undefined)
  assert.equal(release.jobs.checks.uses, './.github/workflows/ci.yml')
  assert.equal(release.jobs.checks.needs, 'validate')
  assert.equal(release.jobs.checks.if, "needs.validate.outputs.eligible == 'true'")
  assert.equal(release.jobs.checks.with.source_sha, '${{ needs.validate.outputs.source_sha }}')
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
  for (const name of ['mac', 'desktop', 'native', 'containers', 'mac-binaries-notary']) {
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
  for (const dependency of ['validate', 'checks', 'mac-programs', 'mac-binaries-notary', 'mac', 'desktop', 'native', 'containers']) assert.ok(needs(release.jobs.seal).has(dependency))
  assert.deepEqual([...needs(release.jobs.publish)].sort(), ['seal', 'validate'])
  assert.equal(release.jobs.seal.if, undefined, 'Default success gating must remain effective')
  assert.equal(release.jobs.publish.if, undefined)
  assert.match(script(release.jobs.seal), /sealed-release-inputs\.mjs seal sealed-release sealed-metadata/)
  const attest = release.jobs.seal.steps.find(step => step.uses?.startsWith('actions/attest@'))
  assert.deepEqual(attest.with['subject-path'].trim().split('\n'), ['sealed-metadata/SHA256SUMS', 'sealed-metadata/package-inputs.json'])
  assert.equal(attest.with['predicate-path'], 'sealed-metadata/release-manifest.json')
  assert.equal(release.jobs.seal.permissions['id-token'], 'write')
  assert.equal(release.jobs.seal.env.RELEASE_SOURCE_SHA, '${{ needs.validate.outputs.source_sha }}')
  assert.equal(release.jobs.publish.env.ASTRAL_RELEASE_BUNDLE_SHA256, '${{ needs.seal.outputs.bundle_sha256 }}')
  assert.equal(release.jobs.publish.env.ASTRAL_RELEASE_INPUTS_SHA256, '${{ needs.seal.outputs.inputs_sha256 }}')
  const publishing = script(release.jobs.publish)
  const download = release.jobs.publish.steps.find(step => step.env?.ARTIFACT_ID === '${{ needs.seal.outputs.artifact_id }}')
  assert.match(download?.run || '', /--artifact-id "\$ARTIFACT_ID"/)
  assert.ok(publishing.indexOf('gh attestation verify') < publishing.indexOf('sealed-release-inputs.mjs restore'))
  assert.ok(publishing.indexOf('sealed-release-inputs.mjs restore') < publishing.indexOf('rolling-release-tag.mjs'))
  assert.match(publishing, /SHA256SUMS package-inputs\.json/)
  assert.ok(publishing.indexOf('rolling-release-tag.mjs') < publishing.indexOf('release-draft.mjs --public'))
  assert.doesNotMatch(publishing, /release-assets|electron-builder|mac-notarize|sign-binaries|build:/)
})

test('source validation runs in parallel without making packages while release retains native lifecycle checks', () => {
  assert.equal(ci.on.push, undefined)
  assert.equal(ci.on.workflow_call.inputs.source_sha.type, 'string')
  assert.equal(ci.on.workflow_call.inputs.source_sha.default, '')
  assert.equal(ci.jobs.native.needs, undefined)
  assert.doesNotMatch(Object.values(ci.jobs).map(script).join('\n'), /build:distribution|package:mac|package:windows|package:linux/)
  assert.match(script(ci.jobs.check), /openapi-generate[\s\S]*sdk-generate[\s\S]*git diff --exit-code/)
  assert.deepEqual(ci.jobs.native.strategy.matrix.shard, [0, 1, 2, 3])
  assert.match(script(ci.jobs.native), /test-go-shard\.mjs/)
  for (const check of ['pnpm lint', 'pnpm typecheck', 'pnpm test']) {
    assert.ok(script(ci.jobs.check).includes(check))
    assert.ok(!script(ci.jobs.native).includes(check))
  }
  const runtime = native.runs.steps.map(step => step.run || '').join('\n')
  for (const check of ['TestDistributionInstallerRegistersActualDaemon', 'pnpm test:desktop-service', 'pnpm test:daemon-service', 'scripts/check-daemon-update.mjs', '--root-user']) assert.ok(runtime.includes(check), check)
  assert.ok(release.jobs.native.steps.some(step => step.uses === './.github/actions/native-release-checks'))
  for (const name of ['mac', 'desktop']) assert.ok(!release.jobs[name].steps.some(step => step.uses === './.github/actions/native-release-checks'))
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
