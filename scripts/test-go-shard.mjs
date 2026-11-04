import { createHash } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { isDirectInvocation, repositoryRoot } from './release-version.mjs'

export function testNames(output) {
  return [...new Set(output.split(/\r?\n/).map(line => line.trim())
    .filter(line => /^(Test|Fuzz|Example)[^\s]*$/.test(line)))].sort()
}

export function shardTests(names, index, count) {
  if (!Number.isInteger(count) || count < 1 || count > 32 || !Number.isInteger(index) || index < 0 || index >= count) throw new Error('Invalid Go test shard.')
  return [...new Set(names)].sort().filter(name => createHash('sha256').update(name).digest().readUInt32BE(0) % count === index)
}

export function testPattern(names) {
  // A duplicate top-level name in different packages belongs to the same
  // shard, so one exact expression covers every package without omissions.
  if (!names.length) return '^$'
  return `^(${names.map(name => name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')})$`
}

export function runShard(index, count, { execute = spawnSync, log = console.log } = {}) {
  shardTests([], index, count)
  const options = { cwd: repositoryRoot, env: { ...process.env, GOWORK: 'off' } }
  // Discover through Go itself, retaining native build constraints, examples
  // and fuzz seed tests. -list does not execute tests; -race warms the same
  // compiled packages used by the actual run below.
  const listed = execute('go', ['test', '-race', '-list', '^(Test|Example|Fuzz)', './...'], { ...options, encoding: 'utf8', maxBuffer: 16 << 20 })
  if (listed.error) throw listed.error
  if (listed.status !== 0) throw new Error(`Go test discovery failed: ${listed.stderr || listed.stdout}`)
  const all = testNames(listed.stdout)
  if (!all.length) throw new Error('Go test discovery returned no tests.')
  const selected = shardTests(all, index, count)
  const pattern = testPattern(selected)
  if (pattern.length > 28_000) throw new Error('Go test shard exceeds the native command-line limit; increase the shard count.')
  log(`Go race shard ${index + 1}/${count}: ${selected.length}/${all.length} top-level test names (including examples and fuzz seeds).`)
  if (!selected.length) return
  const result = execute('go', ['test', '-race', '-timeout=20m', '-run', pattern, './...'], { ...options, stdio: 'inherit' })
  if (result.error) throw result.error
  if (result.status !== 0) throw new Error(`Go race shard ${index + 1}/${count} failed (exit ${result.status}).`)
}

if (await isDirectInvocation(import.meta.url)) {
  const [index, count, ...extra] = process.argv.slice(2)
  try {
    if (extra.length || !/^\d+$/.test(index || '') || !/^[1-9]\d*$/.test(count || '')) throw new Error('Usage: test-go-shard.mjs INDEX COUNT')
    runShard(Number(index), Number(count))
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
