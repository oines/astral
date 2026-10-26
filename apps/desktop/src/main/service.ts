import { spawn, type ChildProcess } from 'node:child_process'
import { mkdtemp, mkdir, readFile, rm } from 'node:fs/promises'
import { join } from 'node:path'
import { isIP } from 'node:net'
import { setTimeout as delay } from 'node:timers/promises'
import { randomBytes } from 'node:crypto'

interface Ready { url: string; pid: number }
interface StartupMaintenance { pid: number; phase: string }
export function maintenanceDeadline(startedAt: number, childPID: number | undefined, marker: StartupMaintenance | undefined): number | undefined {
  return childPID !== undefined && marker?.pid === childPID && marker.phase === 'preparing-data' ? startedAt + 20 * 60_000 : undefined
}
export class LocalService {
  child?: ChildProcess
  url = ''
  private output = ''
  private stopping = false
  private temporary = ''
  private closed?: Promise<void>
  onUnexpectedExit?: (detail: string) => void

  async start(binary: string, root: string, distribution: string, networkArguments: string[] = []): Promise<string> {
    await mkdir(root, { recursive: true, mode: 0o700 })
    this.temporary = await mkdtemp(join(root, '.desktop-'))
    const readyFile = join(this.temporary, 'ready.json')
    // Environment variables remain environment inputs to the Go resolver.
    // Only arguments explicitly supplied by the user have CLI precedence.
    const args = ['serve', ...networkArguments, '--data', join(root, 'server'), '--computers-dir', join(root, 'computers'), '--distribution-dir', distribution, '--ready-file', readyFile, '--parent-pid', String(process.pid), '--managed-stdin']
    this.child = spawn(binary, args, { cwd: root, stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true })
    const credential = randomBytes(32).toString('hex')
    this.child.stdin!.write(`${JSON.stringify({ desktop_token: credential })}\n`)
    this.closed = new Promise(resolve => this.child!.once('close', () => resolve()))
    // A startup failure can close the pipe before stop() requests shutdown.
    this.child.stdin?.on('error', () => {})
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
      const startedAt = Date.now()
      let deadline = startedAt + 30_000
      while (Date.now() < deadline) {
        if (failure) throw failure
        const maintenance = await readFile(join(root, 'server', 'startup-maintenance.json'), 'utf8').then(raw => JSON.parse(raw) as StartupMaintenance).catch(() => undefined)
        // Only a marker belonging to this exact child can extend the startup
        // budget. Old markers and ordinary startup retain the normal timeout.
        deadline = maintenanceDeadline(startedAt, this.child.pid, maintenance) ?? deadline
        const ready = await readFile(readyFile, 'utf8').then(raw => JSON.parse(raw) as Ready).catch(() => undefined)
        if (ready) {
          if (ready.pid !== this.child.pid) throw new Error('Invalid local service startup response.')
          const url = new URL(ready.url)
          const loopback = ['[::1]', 'localhost'].includes(url.hostname) || (isIP(url.hostname) === 4 && url.hostname.startsWith('127.'))
          if (url.protocol !== 'http:' || !loopback) throw new Error('The desktop service must provide a local HTTP address.')
          this.url = url.origin
          return credential
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
      // Closing this private pipe is the Server's graceful shutdown request.
      // POSIX signals become forceful termination on Windows.
      child.stdin?.end()
      const controller = new AbortController()
      await Promise.race([exited, delay(20_000, undefined, { signal: controller.signal }).catch(() => {})])
      controller.abort()
      if (child.exitCode === null && child.signalCode === null) {
        child.kill('SIGKILL')
        await exited
      }
    }
    if (this.closed) {
      let timeout: ReturnType<typeof setTimeout> | undefined
      try {
        await Promise.race([this.closed, new Promise<never>((_, reject) => {
          timeout = setTimeout(() => reject(new Error('Local service pipes did not close after Server exit.')), 5000)
        })])
      } finally {
        clearTimeout(timeout)
      }
    }
    if (this.temporary) await rm(this.temporary, { recursive: true, force: true })
  }
}
