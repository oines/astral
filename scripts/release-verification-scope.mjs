import { execFileSync } from 'node:child_process'
import { appendFile } from 'node:fs/promises'
import { isDirectInvocation, repositoryRoot } from './release-version.mjs'

const sha = value => typeof value === 'string' && /^[a-f0-9]{40}$/.test(value)
const knownRoots = new Set(['.github', '.husky', 'apps', 'cmd', 'docs', 'internal', 'packages', 'scripts', 'spec'])
const nonProductFiles = new Set(['README.md', 'README.zh-CN.md', 'AGENTS.md', 'SPEC.md', 'CONTRIBUTING.md', 'SECURITY.md', '.gitignore', '.golangci.yml', 'eslint.config.mjs', 'vitest.config.ts', 'wrangler.jsonc'])
// These modules implement ordinary Host behavior. Installation, supervision,
// persistent storage and transport are deliberately outside this allowlist.
const hostModules = new Set(['auth', 'channels', 'domain', 'filestream', 'gateway', 'harness', 'httpcontract', 'messages', 'modelbridge', 'presenter', 'recall', 'schedule', 'search', 'timezone'])

export function parseNameStatus(raw) {
  if (typeof raw !== 'string' && !Buffer.isBuffer(raw)) throw new Error('Git name-status output must be text or bytes.')
  const value = typeof raw === 'string' ? raw : new globalThis.TextDecoder('utf-8', { fatal: true }).decode(raw)
  if (value === '') return []
  if (!value.endsWith('\0')) throw new Error('Git name-status output is not NUL-terminated.')
  const fields = value.slice(0, -1).split('\0')
  const result = []
  for (let index = 0; index < fields.length;) {
    const status = fields[index++]
    if (!/^(?:[ADMT]|[RC]\d{1,3})$/.test(status) || /^[RC]/.test(status) && Number(status.slice(1)) > 100) throw new Error('Unknown Git name-status record.')
    const count = /^[RC]/.test(status) ? 2 : 1
    const paths = fields.slice(index, index + count)
    if (paths.length !== count || paths.some(name => !name || name.startsWith('/') || name.includes('\\') || [...name].some(character => character.codePointAt(0) < 32 || character.codePointAt(0) === 127) || name.split('/').some(part => !part || part === '.' || part === '..'))) throw new Error('Unsafe or incomplete Git name-status path.')
    result.push({ status, paths })
    index += count
  }
  return result
}

function affectedArea(name) {
  const [root, module] = name.split('/')
  if (!knownRoots.has(root) && !nonProductFiles.has(name) && name.includes('/')) return 'unknown_path'
  // A lock or manifest can change installed/runtime code even in an otherwise
  // unrelated directory. Check it before allowing documentation or fixtures.
  if (/(?:^|\/)(?:package\.json|go\.(?:mod|sum|work)|pnpm-workspace\.yaml|[^/]*\.lock|[^/]*(?:lock|manifest)[^/]*\.(?:json|jsonc|yaml|yml))$/.test(name)) return 'dependencies_or_manifest'
  if (knownRoots.has(root) && (/(?:_test\.go|\.test\.[cm]?[jt]sx?)$/.test(name) || /(?:^|\/)(?:testdata|tests)\//.test(name))) return undefined
  if (/(?:^|\/)(?:migrations?|datamigrate)(?:\/|$)/.test(name) || /(?:^|\/)(?:migrate|installer|install)(?:[-_.]|$)/.test(name)) return 'installation_or_migration'
  if (nonProductFiles.has(name) || root === 'docs' || root === '.husky' || name.startsWith('apps/website/') || name === '.github/workflows/website.yml') return undefined
  if (root === 'scripts' || root === '.github') return 'release_or_build'
  if (root === 'apps' || root === 'packages') return 'desktop_or_frontend'
  if (root === 'cmd' || root === 'spec') return 'entrypoint_or_contract'
  if (root === 'internal') {
    if (hostModules.has(module)) return undefined
    return 'installation_storage_or_unknown_module'
  }
  // New root files, build configuration and unrecognized modules all retain
  // the expensive fixture until their boundaries have been reviewed.
  return 'unknown_or_build_input'
}

export function scopeForChanges({ baseSHA, sourceSHA, changes }) {
  const changedPaths = [...new Set(changes.flatMap(change => change.paths))].sort()
  const reasons = changedPaths.flatMap(name => {
    const area = affectedArea(name)
    return area ? [{ code: 'affected_path', path: name, area }] : []
  })
  return {
    base_sha: baseSHA, source_sha: sourceSHA, full_desktop_update: reasons.length > 0,
    reasons: reasons.length ? reasons : [{ code: 'host_only_or_non_product_changes' }], changed_paths: changedPaths,
  }
}

export function releaseVerificationScope({ baseSHA, sourceSHA, cwd = repositoryRoot, execute = execFileSync }) {
  const full = code => ({ base_sha: sha(baseSHA) ? baseSHA : null, source_sha: sha(sourceSHA) ? sourceSHA : null, full_desktop_update: true, reasons: [{ code }], changed_paths: [] })
  if (!sha(sourceSHA)) return full('invalid_source_sha')
  if (baseSHA === undefined || baseSHA === null || baseSHA === '') return full('no_previous_public_source')
  if (!sha(baseSHA)) return full('invalid_base_sha')
  let raw
  try {
    // Two exact endpoints cover all merged commits since the last public App,
    // including deleted files and both sides of renames/copies. No refs or
    // remote state are selected by this helper, and no diff drivers are run.
    raw = execute('git', ['diff', '--no-ext-diff', '--no-textconv', '--name-status', '-z', '--find-renames', '--find-copies', baseSHA, sourceSHA, '--'], { cwd, maxBuffer: 16 << 20, timeout: 30_000, stdio: ['ignore', 'pipe', 'pipe'] })
  } catch { return full('git_diff_failed') }
  try { return scopeForChanges({ baseSHA, sourceSHA, changes: parseNameStatus(raw) }) }
  catch { return full('git_diff_parse_failed') }
}

export async function writeScopeOutputs(result, output) {
  if (!output) return
  await appendFile(output, `full_desktop_update=${result.full_desktop_update}\nverification_scope_json=${JSON.stringify(result)}\n`)
}

async function main() {
  const args = process.argv.slice(2)
  const values = new Map()
  for (let index = 0; index < args.length; index += 2) {
    if (!['--base', '--source'].includes(args[index]) || args[index + 1] === undefined || values.has(args[index])) throw new Error('Usage: release-verification-scope.mjs --base PREVIOUS_SOURCE_SHA --source SOURCE_SHA')
    values.set(args[index], args[index + 1])
  }
  const result = releaseVerificationScope({ baseSHA: values.get('--base'), sourceSHA: values.get('--source') })
  await writeScopeOutputs(result, process.env.GITHUB_OUTPUT)
  console.log(JSON.stringify(result))
}

if (await isDirectInvocation(import.meta.url)) main().catch(error => { console.error(error.message); process.exitCode = 1 })
