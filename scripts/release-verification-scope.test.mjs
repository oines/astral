import assert from 'node:assert/strict'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { parseNameStatus, releaseVerificationScope, scopeForChanges, writeScopeOutputs } from './release-verification-scope.mjs'

const baseSHA = 'a'.repeat(40), sourceSHA = 'b'.repeat(40)
const scope = paths => scopeForChanges({ baseSHA, sourceSHA, changes: paths.map(name => ({ status: 'M', paths: [name] })) })

test('ordinary Host work can omit only the prepublication old-App fixture', () => {
  for (const name of [
    'internal/harness/inputs.go', 'internal/channels/weixin.go', 'internal/modelbridge/requests.go',
    'internal/messages/archive.go', 'internal/gateway/session.go', 'internal/schedule/service.go',
    'internal/search/request.go', 'internal/domain/session.go', 'internal/auth/settings.go',
    'docs/releasing.md', 'README.md', 'apps/website/src/content/docs/docs/start.md',
    'scripts/mac-notarize.test.mjs', 'apps/desktop/src/main/updates.test.ts', 'internal/platform/installations_test.go',
  ]) assert.equal(scope([name]).full_desktop_update, false, name)
})

test('Desktop, installation, migrations, dependencies, release orchestration and unknown inputs require the full fixture', () => {
  for (const name of [
    'apps/desktop/src/main/updates.ts', 'apps/desktop/src/main/index.ts', 'apps/desktop/src/preload/index.ts',
    'apps/desktop/electron-builder.release.cjs', 'apps/desktop/electron.vite.config.ts',
    'apps/web/src/components/settings/DesktopUpdates.vue', 'packages/ui/src/Button.vue',
    'internal/platform/daemon_update.go', 'internal/cli/serve.go', 'internal/datamigrate/upgrade.go',
    'internal/persistence/migrations/00007_next.sql', 'internal/persistence/migrate.go', 'internal/persistence/store.go',
    'internal/daemonservice/update.go', 'internal/serverupdate/update.go', 'internal/releasefeed/releases.go',
    'internal/githubrelease/releases.go', 'internal/httpapi/updates.go', 'internal/server/server.go',
    'go.mod', 'go.sum', 'go.work', 'pnpm-lock.yaml', 'pnpm-workspace.yaml', 'package.json',
    'apps/desktop/package.json', 'apps/website/package.json', 'packages/sdk/package.json',
    'internal/platform/computer-image/image-lock.json', 'internal/harness/runtime-manifest.json',
    'internal/harness/Cargo.lock', 'internal/harness/migrations/00001.sql', 'internal/harness/migrate.go', 'internal/harness/install.sh',
    'scripts/release-draft.mjs', 'scripts/check-mac-release.mjs', 'scripts/install-server.sh',
    '.github/workflows/release.yml', '.github/workflows/ci.yml', '.github/actions/release-source/action.yml',
    'cmd/astral/main.go', 'spec/openapi/astral.json', 'tsconfig.json', 'openapi-ts.config.ts', 'LICENSE',
    'new-root/agent.go', 'new-root/agent.test.ts', 'future.config.json', 'internal/new-module/agent.go',
  ]) assert.equal(scope([name]).full_desktop_update, true, name)
  assert.equal(scope(['internal/harness/inputs.go', 'apps/desktop/src/main/updates.ts']).full_desktop_update, true)
})

test('Git NUL records preserve deletes, copy/rename endpoints and whitespace without treating a tab as a field separator', () => {
  const value = parseNameStatus('D\0apps/desktop/src/main/updates.ts\0R100\0internal/platform/install old.go\0internal/harness/install new.go\0C075\0internal/harness/old.go\0internal/harness/copy.go\0')
  assert.deepEqual(value, [
    { status: 'D', paths: ['apps/desktop/src/main/updates.ts'] },
    { status: 'R100', paths: ['internal/platform/install old.go', 'internal/harness/install new.go'] },
    { status: 'C075', paths: ['internal/harness/old.go', 'internal/harness/copy.go'] },
  ])
  const result = scopeForChanges({ baseSHA, sourceSHA, changes: value })
  assert.equal(result.full_desktop_update, true)
  assert.ok(result.reasons.some(reason => reason.path === 'internal/platform/install old.go'))
})

test('missing provenance, unknown Git records, invalid paths and malformed UTF-8 cannot select lighter checks', () => {
  const never = () => assert.fail('Invalid provenance must not invoke Git.')
  for (const [base, source, reason] of [[undefined, sourceSHA, 'no_previous_public_source'], ['', sourceSHA, 'no_previous_public_source'], ['main', sourceSHA, 'invalid_base_sha'], [baseSHA, 'HEAD', 'invalid_source_sha']]) {
    const result = releaseVerificationScope({ baseSHA: base, sourceSHA: source, execute: never })
    assert.equal(result.full_desktop_update, true)
    assert.equal(result.reasons[0].code, reason)
  }
  for (const raw of [
    undefined, null, {},
    'M\0internal/harness/inputs.go', 'R100\0internal/harness/old.go\0', 'Z\0internal/harness/file.go\0',
    'R101\0a\0b\0', 'M\0../internal/harness/file.go\0', 'M\0/internal/harness/file.go\0',
    'M\0internal/harness//file.go\0', 'M\0internal/harness/file\t.go\0', 'M\0internal/harness/file\n.go\0',
    Buffer.from([77, 0, 105, 110, 116, 101, 114, 110, 97, 108, 47, 0xff, 0]),
  ]) {
    const result = releaseVerificationScope({ baseSHA, sourceSHA, execute: () => raw })
    assert.equal(result.full_desktop_update, true)
    assert.equal(result.reasons[0].code, 'git_diff_parse_failed')
  }
  assert.equal(releaseVerificationScope({ baseSHA, sourceSHA, execute: () => { throw new Error('missing Git object') } }).reasons[0].code, 'git_diff_failed')
})

async function repository(t) {
  const root = await mkdtemp(path.join(tmpdir(), 'astral-release-scope-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const git = args => execFileSync('git', args, { cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 15_000 }).trim()
  git(['init', '--quiet'])
  git(['config', 'user.name', 'Synthetic release test'])
  git(['config', 'user.email', 'release-test@example.invalid'])
  git(['config', 'commit.gpgsign', 'false'])
  const write = async (name, value) => { await mkdir(path.dirname(path.join(root, name)), { recursive: true }); await writeFile(path.join(root, name), value) }
  const commit = message => { git(['add', '-A']); git(['commit', '--quiet', '-m', message]); return git(['rev-parse', 'HEAD']) }
  return { root, git, write, commit }
}

test('the exact last-public-to-selected-source diff includes earlier commits, rename sources and deletions', async t => {
  const f = await repository(t)
  await f.write('internal/harness/inputs.go', 'ordinary host input\n')
  await f.write('apps/desktop/src/main/updates.ts', 'critical update bytes\n'.repeat(10))
  await f.write('internal/platform/old.go', 'installation behavior\n'.repeat(10))
  const base = f.commit('last public source')
  await f.write('internal/harness/inputs.go', 'normal host recovery fix\n')
  const host = f.commit('ordinary Host fix')
  assert.equal(releaseVerificationScope({ baseSHA: base, sourceSHA: host, cwd: f.root }).full_desktop_update, false)
  await mkdir(path.join(f.root, 'internal/harness'), { recursive: true })
  f.git(['mv', 'internal/platform/old.go', 'internal/harness/moved.go'])
  const renamed = f.commit('move former installation implementation')
  const renameScope = releaseVerificationScope({ baseSHA: host, sourceSHA: renamed, cwd: f.root })
  assert.equal(renameScope.full_desktop_update, true)
  assert.ok(renameScope.changed_paths.includes('internal/platform/old.go'))
  f.git(['rm', 'apps/desktop/src/main/updates.ts'])
  f.commit('remove update implementation')
  await f.write('internal/harness/inputs.go', 'latest unrelated host change\n')
  const source = f.commit('last commit alone looks harmless')
  const result = releaseVerificationScope({ baseSHA: base, sourceSHA: source, cwd: f.root })
  assert.equal(result.base_sha, base)
  assert.equal(result.source_sha, source)
  assert.equal(result.full_desktop_update, true)
  assert.ok(result.changed_paths.includes('apps/desktop/src/main/updates.ts'))
  assert.ok(result.changed_paths.includes('internal/platform/old.go'))
  assert.equal(releaseVerificationScope({ baseSHA: source, sourceSHA: source, cwd: f.root }).full_desktop_update, false)
})

test('the diff uses exactly the supplied endpoints and GitHub output explicitly records the selected coverage', async t => {
  let args
  const result = releaseVerificationScope({ baseSHA, sourceSHA, execute: (_program, value) => { args = value; return Buffer.from('M\0internal/harness/inputs.go\0') } })
  assert.deepEqual(args.slice(-3), [baseSHA, sourceSHA, '--'])
  assert.ok(args.includes('--no-ext-diff') && args.includes('--no-textconv'))
  const f = await repository(t)
  const output = path.join(f.root, 'outputs')
  await writeScopeOutputs(result, output)
  const lines = (await readFile(output, 'utf8')).trim().split('\n')
  assert.equal(lines[0], 'full_desktop_update=false')
  assert.deepEqual(JSON.parse(lines[1].slice('verification_scope_json='.length)), result)
  const cli = fileURLToPath(new URL('./release-verification-scope.mjs', import.meta.url))
  const command = spawnSync(process.execPath, [cli, '--base', '', '--source', sourceSHA], { encoding: 'utf8', env: { ...process.env, GITHUB_OUTPUT: output }, timeout: 10_000 })
  assert.equal(command.status, 0)
  assert.equal(JSON.parse(command.stdout).full_desktop_update, true)
  assert.match(await readFile(output, 'utf8'), /full_desktop_update=true/)
})
