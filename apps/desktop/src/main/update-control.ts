import { randomBytes, randomUUID, timingSafeEqual } from 'node:crypto'
import { createServer, type Server } from 'node:http'
import { mkdir, readFile, rename, writeFile } from 'node:fs/promises'
import path from 'node:path'
import type { DesktopUpdateState } from '../shared/updates'
import type { DesktopUpdates } from './updates'

interface Operation {
  id: string
  from: string
  target?: string
  state: 'running' | 'complete' | 'failed'
  error?: string
}
const versionPattern = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/

/** Both native IPC and the private Owner API relay use this one operation. */
export class DesktopUpdateControl {
  private operation?: Operation
  private active?: Promise<void>
  private writes = Promise.resolve()
  private recoveryError?: string
  onState?: (state: DesktopUpdateState) => void
  onInstallError?: (error: Error) => void

  constructor(private readonly updates: DesktopUpdates, private readonly journal: string) {
    updates.onState = state => {
      if (this.operation?.state === 'running' && state.version && ['downloading', 'installing'].includes(state.phase)) this.operation.target = state.version
      this.onState?.(this.state())
    }
    updates.onInstallError = error => {
      const reopen = () => this.onInstallError?.(error)
      void this.fail(error).then(reopen, reopen)
    }
  }

  async initialize() {
    let record: unknown
    try { record = JSON.parse(await readFile(this.journal, 'utf8')) }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') this.recoveryError = 'The previous update operation could not be read. Check the installed version before updating again.'
      return
    }
    const value = record as Partial<Operation> | null
    if (!value || typeof value.id !== 'string' || !/^[a-f0-9-]{36}$/.test(value.id) || typeof value.from !== 'string' || !versionPattern.test(value.from) ||
      value.target !== undefined && (typeof value.target !== 'string' || !versionPattern.test(value.target)) ||
      !['running', 'complete', 'failed'].includes(value.state || '') || value.error !== undefined && typeof value.error !== 'string') {
      this.recoveryError = 'The previous update operation is invalid. Check the installed version before updating again.'
      return
    }
    this.operation = value as Operation
    if (value.state === 'running') {
      const installed = this.updates.state().currentVersion
      this.operation.state = value.target === installed ? 'complete' : 'failed'
      if (this.operation.state === 'failed') this.operation.error = 'The previous update did not finish with its expected application version.'
      await this.save()
    }
  }

  state(): DesktopUpdateState {
    const state = this.updates.state()
    const operation = this.operation
    if (this.recoveryError && state.phase === 'idle') return { ...state, phase: 'error', error: this.recoveryError, retry: 'check' }
    const details = operation ? { operationId: operation.id, operationVersion: operation.target, operationState: operation.state } : {}
    if (operation?.state === 'failed' && state.phase === 'idle') return { ...state, ...details, phase: 'error', error: operation.error, retry: 'update' }
    if (this.active && operation?.state === 'running' && ['idle', 'available', 'current'].includes(state.phase)) return { ...state, ...details, phase: 'checking' }
    return { ...state, ...details }
  }

  async check(automatic = false) {
    if (automatic && this.operation?.state === 'failed') return this.state()
    await this.updates.check(automatic)
    return this.state()
  }

  /** The HTTP request returns before metadata/download/shutdown begins. */
  apply(): DesktopUpdateState {
    if (!this.active && !['unavailable', 'installing'].includes(this.updates.state().phase)) this.begin()
    return this.state()
  }

  async update(): Promise<void> {
    if (!this.active) {
      if (this.updates.state().phase === 'unavailable') throw new Error('In-app updates are unavailable for this installation.')
      if (this.updates.state().phase === 'installing') return
      this.begin()
    }
    await this.active
  }

  /** Must be durable before the Go Server or Electron process is stopped. */
  async beforeInstall() {
    if (!this.operation?.target) throw new Error('The update operation has no verified target version.')
    await this.save()
  }

  private begin() {
    this.recoveryError = undefined
    this.operation = { id: randomUUID(), from: this.updates.state().currentVersion, state: 'running' }
    this.active = Promise.resolve().then(async () => {
      try {
        await this.save()
        await this.updates.update()
        if (this.updates.state().phase === 'current') {
          this.operation!.target = this.updates.state().currentVersion
          this.operation!.state = 'complete'
          await this.save()
          this.onState?.(this.state())
        }
      } catch (error) {
        await this.fail(error)
        throw error
      }
    }).finally(() => { this.active = undefined })
    // A queued HTTP apply does not leave its detached operation unhandled.
    void this.active.catch(() => {})
    this.onState?.(this.state())
  }

  private async fail(value: unknown) {
    if (!this.operation) return
    this.operation.state = 'failed'
    this.operation.error = value instanceof Error ? value.message : String(value)
    await this.save()
    this.onState?.(this.state())
  }

  private save() {
    const bytes = `${JSON.stringify(this.operation)}\n`
    this.writes = this.writes.catch(() => {}).then(async () => {
      await mkdir(path.dirname(this.journal), { recursive: true, mode: 0o700 })
      const temporary = `${this.journal}.${randomUUID()}.tmp`
      await writeFile(temporary, bytes, { mode: 0o600, flag: 'wx' })
      await rename(temporary, this.journal)
    })
    return this.writes
  }
}

export function privateUpdateState(state: DesktopUpdateState) {
  return { phase: state.phase, current_version: state.currentVersion, version: state.version, percent: state.percent,
    error: state.error, retry: state.retry, unavailable: state.unavailable, manager: 'desktop',
    operation_id: state.operationId, operation_version: state.operationVersion, operation_state: state.operationState }
}

/** Local managed-stdin credentials are never exposed to Renderer or the browser. */
export async function startDesktopUpdateBridge(control: Pick<DesktopUpdateControl, 'state' | 'check' | 'apply'>): Promise<{ url: string; token: string; close: () => Promise<void> }> {
  const token = randomBytes(32).toString('hex')
  let expectedHost = ''
  const server: Server = createServer(async (request, response) => {
    response.setHeader('Cache-Control', 'no-store')
    response.setHeader('Content-Type', 'application/json')
    const reject = (status: number) => { response.writeHead(status); response.end('{}') }
    const authorization = Buffer.from(request.headers.authorization || '')
    const expected = Buffer.from(`Bearer ${token}`)
    if (!['127.0.0.1', '::ffff:127.0.0.1'].includes(request.socket.remoteAddress || '') || request.headers.host !== expectedHost ||
      request.headers.origin !== undefined || authorization.length !== expected.length || !timingSafeEqual(authorization, expected)) return reject(403)
    if (request.headers['transfer-encoding'] !== undefined || Number(request.headers['content-length'] || 0) !== 0) return reject(400)
    try {
      let state: DesktopUpdateState
      if (request.method === 'GET' && request.url === '/state') state = control.state()
      else if (request.method === 'POST' && request.url === '/check') state = await control.check()
      else if (request.method === 'POST' && request.url === '/apply') state = control.apply()
      else return reject(404)
      response.end(JSON.stringify(privateUpdateState(state)))
    } catch { response.writeHead(503); response.end('{"error":"Desktop update control could not complete the request."}') }
  })
  server.headersTimeout = 5000
  server.requestTimeout = 10_000
  server.keepAliveTimeout = 1000
  await new Promise<void>((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve) })
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('The private update bridge did not obtain a loopback port.')
  expectedHost = `127.0.0.1:${address.port}`
  let closing: Promise<void> | undefined
  return { url: `http://${expectedHost}`, token, close: () => {
    closing ??= new Promise<void>((resolve, reject) => { server.close(error => error ? reject(error) : resolve()); server.closeAllConnections() })
    return closing
  } }
}
