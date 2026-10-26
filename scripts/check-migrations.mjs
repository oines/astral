import { execFileSync } from 'node:child_process'
import { isDirectInvocation } from './release-version.mjs'

// Migration history is part of the published data contract. New steps may be
// appended; a released SQL/Go source must never be edited, renamed or removed.
export function checkReleasedMigrations(cwd = process.cwd()) {
  const git = args => execFileSync('git', args, { cwd, encoding: 'utf8', maxBuffer: 4 << 20 }).trim()
  const tags = git(['tag', '--list']).split('\n').filter(tag => /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(tag))
  tags.sort((a, b) => {
    const aa = a.slice(1).split('.').map(BigInt), bb = b.slice(1).split('.').map(BigInt)
    for (let i = 0; i < 3; i++) { if (aa[i] !== bb[i]) return aa[i] > bb[i] ? -1 : 1 }
    return 0
  })
  if (!tags.length) return { baseline: null, checked: 0 }
  const baseline = tags[0]
  const files = git(['ls-tree', '-r', '--name-only', baseline, '--', 'internal/persistence/migrations/']).split('\n').filter(file => /\/\d+_[^/]+\.(sql|go)$/.test(file))
  for (const file of files) {
    const old = git(['rev-parse', `${baseline}:${file}`])
    let current
    try { current = git(['hash-object', '--', file]) } catch { throw new Error(`Published migration was removed: ${file}`) }
    if (old !== current) throw new Error(`Published migration changed: ${file}. Add a new migration instead.`)
  }
  return { baseline, checked: files.length }
}

if (await isDirectInvocation(import.meta.url)) {
  try { const result = checkReleasedMigrations(); console.log(`Migration history verified (${result.baseline || 'first release'}, ${result.checked} released sources).`) }
  catch (error) { console.error(error.message); process.exitCode = 1 }
}
