import { spawn, type ChildProcess } from 'node:child_process'
import { mkdtemp, mkdir, readFile, rm } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

interface Ready { url: string; pid: number; token_file: string }
export class LocalService {
  child?: ChildProcess
  url = ''
  private output = ''
  private stopping = false
  private temporary = ''
  onUnexpectedExit?: (detail: string) => void

  async start(binary: string, root: string, distribution: string): Promise<string> {
    await mkdir(root, { recursive: true, mode: 0o700 })
    this.temporary = await mkdtemp(join(root, '.desktop-'))
    const readyFile = join(this.temporary, 'ready.json')
    const args = ['serve', '--listen', process.env.ASTRAL_SERVER_LISTEN || '127.0.0.1:4545', '--data', join(root, 'server'), '--computers-dir', join(root, 'computers'), '--distribution-dir', distribution, '--ready-file', readyFile, '--parent-pid', String(process.pid)]
    if (process.env.ASTRAL_PUBLIC_URL) args.push('--public-url', process.env.ASTRAL_PUBLIC_URL)
    if (process.env.ASTRAL_TLS_CERT) args.push('--tls-cert', process.env.ASTRAL_TLS_CERT)
    if (process.env.ASTRAL_TLS_KEY) args.push('--tls-key', process.env.ASTRAL_TLS_KEY)
    this.child = spawn(binary, args, { cwd: root, stdio: ['ignore', 'pipe', 'pipe'] })
    const capture = (chunk: Buffer) => { this.output = (this.output + chunk.toString()).slice(-6000) }
    this.child.stdout?.on('data', capture)
    this.child.stderr?.on('data', capture)
    let failure: Error | undefined
    this.child.once('error', error => { failure = error })
    this.child.once('exit', (code, signal) => {
      failure = new Error(this.output || `Local service exited (${signal || code}).`)
      if (this.url && !this.stopping) this.onUnexpectedExit?.(failure.message)
    })
    try {
      const deadline = Date.now() + 30_000
      while (Date.now() < deadline) {
        if (failure) throw failure
        const ready = await readFile(readyFile, 'utf8').then(raw => JSON.parse(raw) as Ready).catch(() => undefined)
        if (ready) {
          if (ready.pid !== this.child.pid || resolve(ready.token_file) !== join(resolve(root), 'server/owner.token')) throw new Error('Invalid local service startup response.')
          const url = new URL(ready.url)
          if (!['127.0.0.1', '[::1]', 'localhost'].includes(url.hostname)) throw new Error('The desktop service must listen on loopback.')
          this.url = url.origin
          return (await readFile(ready.token_file, 'utf8')).trim()
        }
        await delay(100)
      }
      throw new Error(`The local service did not become ready.\n${this.output}`)
    } catch (error) {
      await this.stop()
      throw error
    }
  }
  async stop() {
    this.stopping = true
    const child = this.child
    if (child?.pid && child.exitCode === null && child.signalCode === null) {
      const exited = new Promise<void>(resolve => child.once('exit', () => resolve()))
      child.kill('SIGTERM')
      const controller = new AbortController()
      await Promise.race([exited, delay(20_000, undefined, { signal: controller.signal }).catch(() => {})])
      controller.abort()
      if (child.exitCode === null && child.signalCode === null) {
        child.kill('SIGKILL')
        await exited
      }
    }
    if (this.temporary) await rm(this.temporary, { recursive: true, force: true })
  }
}
