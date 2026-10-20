import { arch, platform } from 'node:os'
import { buildServer, embedWeb } from './build-lib.mjs'

await embedWeb()
const target = platform()
if (!['darwin', 'linux'].includes(target)) throw new Error('Astral supports macOS and Linux.')
const cpu = arch() === 'x64' ? 'amd64' : arch()
const output = await buildServer(target, cpu)
console.log(`Built ${output}`)
