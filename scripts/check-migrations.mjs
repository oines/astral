import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { isDirectInvocation } from './release-version.mjs'

const coreModule = 'github.com/oines/astral-core'
const coreMigrations = 'store/sqlite/migrations'
const migrationSource = /\/\d+_[^/]+\.(sql|go)$/

// The Go commands only parse metadata and fetch the explicitly pinned source.
// They do not build or execute dependency code, and never honor a local go.work.
export function resolvePinnedCoreModule(goMod, { command = execFileSync } = {}) {
  const temporary = mkdtempSync(path.join(tmpdir(), 'astral-migration-module-'))
  const manifest = path.join(temporary, 'go.mod')
  const options = { cwd: temporary, encoding: 'utf8', env: { ...process.env, GOWORK: 'off' }, maxBuffer: 4 << 20, timeout: 120_000 }
  try {
    writeFileSync(manifest, goMod)
    const metadata = JSON.parse(command('go', ['mod', 'edit', '-json', manifest], options))
    const requirements = (metadata.Require || []).filter(entry => entry.Path === coreModule)
    if (requirements.length !== 1 || !/^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+incompatible)?$/.test(requirements[0].Version || '')) {
      throw new Error(`go.mod must pin exactly one published ${coreModule} version.`)
    }
    if ((metadata.Replace || []).some(entry => entry.Old?.Path === coreModule)) {
      throw new Error(`Migration history requires the published ${coreModule} module, without a replace directive.`)
    }
    const version = requirements[0].Version
    const downloaded = JSON.parse(command('go', ['mod', 'download', '-json', `${coreModule}@${version}`], options))
    if (downloaded.Error || downloaded.Path !== coreModule || downloaded.Version !== version || typeof downloaded.Dir !== 'string' || !path.isAbsolute(downloaded.Dir)) {
      throw new Error(`Could not resolve the published ${coreModule}@${version} source.`)
    }
    return { version, directory: downloaded.Dir }
  } finally { rmSync(temporary, { recursive: true, force: true }) }
}

function moduleMigrationSources(directory, relative = coreMigrations) {
  let entries
  try { entries = readdirSync(path.join(directory, relative), { withFileTypes: true }) }
  catch (error) { if (error.code === 'ENOENT' && relative === coreMigrations) return []; throw error }
  return entries.flatMap(entry => {
    const file = `${relative}/${entry.name}`
    if (entry.isDirectory()) return moduleMigrationSources(directory, file)
    if (!migrationSource.test(file)) return []
    if (!entry.isFile()) throw new Error(`Published Core migration is not a regular file: ${file}`)
    return [file]
  }).sort()
}

// Migration history is part of the published data contract. New steps may be
// appended; a released SQL/Go source must never be edited, renamed or removed.
export function checkReleasedMigrations(cwd = process.cwd(), { resolveCoreModule = resolvePinnedCoreModule } = {}) {
  const git = args => execFileSync('git', args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 4 << 20 }).trim()
  const tags = git(['tag', '--list']).split('\n').filter(tag => /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(tag))
  tags.sort((a, b) => {
    const aa = a.slice(1).split('.').map(BigInt), bb = b.slice(1).split('.').map(BigInt)
    for (let i = 0; i < 3; i++) { if (aa[i] !== bb[i]) return aa[i] > bb[i] ? -1 : 1 }
    return 0
  })
  if (!tags.length) return { baseline: null, checked: 0 }
  const baseline = tags[0]
  const files = git(['ls-tree', '-r', '--name-only', baseline, '--', 'internal/persistence/migrations/']).split('\n').filter(file => migrationSource.test(file))
  for (const file of files) {
    const old = git(['rev-parse', `${baseline}:${file}`])
    let current
    try { current = git(['hash-object', '--', file]) } catch { throw new Error(`Published migration was removed: ${file}`) }
    if (old !== current) throw new Error(`Published migration changed: ${file}. Add a new migration instead.`)
  }
  const previous = resolveCoreModule(git(['show', `${baseline}:go.mod`]), { cwd })
  const current = resolveCoreModule(readFileSync(path.join(cwd, 'go.mod'), 'utf8'), { cwd })
  const coreFiles = moduleMigrationSources(previous.directory)
  for (const file of coreFiles) {
    let next
    try { next = readFileSync(path.join(current.directory, file)) }
    catch (error) {
      if (error.code === 'ENOENT' || error.code === 'EISDIR') throw new Error(`Published Core migration was removed: ${file}`, { cause: error })
      throw error
    }
    if (!readFileSync(path.join(previous.directory, file)).equals(next)) {
      throw new Error(`Published Core migration changed: ${file}. Add a new migration instead.`)
    }
  }
  return { baseline, checked: files.length + coreFiles.length, core: { baseline: previous.version, current: current.version, checked: coreFiles.length } }
}

if (await isDirectInvocation(import.meta.url)) {
  try {
    const result = checkReleasedMigrations()
    const coreChecked = result.core?.checked || 0
    console.log(`Migration history verified (${result.baseline || 'first release'}, Host ${result.checked - coreChecked}, Core ${coreChecked} released sources).`)
  }
  catch (error) { console.error(error.message); process.exitCode = 1 }
}
