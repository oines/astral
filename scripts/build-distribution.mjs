import { createHash } from 'node:crypto'
import { readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { buildServer, embedWeb, root, run } from './build-lib.mjs'

run('pnpm', ['openapi-generate'])
run('pnpm', ['sdk-generate'])
run('pnpm', ['--filter', '@astral/web', 'build'])
await embedWeb()
const sums = []
for (const system of ['darwin', 'linux']) {
  for (const arch of ['arm64', 'amd64']) {
    const output = await buildServer(system, arch)
    const hash = createHash('sha256').update(await readFile(output)).digest('hex')
    sums.push(`${hash}  ${path.basename(output)}`)
  }
}
await writeFile(path.join(root, 'dist/bin/checksums.txt'), `${sums.join('\n')}\n`)
console.log('Built macOS and Linux distributions in dist/bin.')
