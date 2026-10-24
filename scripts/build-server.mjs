import { arch, platform } from 'node:os'
import { buildServer, embedWeb } from './build-lib.mjs'

await embedWeb()
const target = platform() === 'win32' ? 'windows' : platform()
if (!['darwin', 'linux', 'windows'].includes(target)) throw new Error('Astral supports macOS, Linux, and Windows.')
const cpu = arch() === 'x64' ? 'amd64' : arch()
const output = await buildServer(target, cpu)
console.log(`Built ${output}`)
