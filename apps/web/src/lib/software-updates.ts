import type { DesktopUpdateState } from '../../../desktop/src/shared/updates'

export type SoftwareUpdateState = Omit<DesktopUpdateState, 'unavailable'> & { unavailable?: string; manager?: string }
export interface UpdateTransport {
  state: () => Promise<SoftwareUpdateState>
  check: () => Promise<SoftwareUpdateState>
  apply: () => Promise<SoftwareUpdateState>
}
interface Attempt { id?: string; previousId?: string; target?: string; from: string; started: number }
interface Persistence { getItem: (key: string) => string | null; setItem: (key: string, value: string) => void; removeItem: (key: string) => void }
const storageKey = 'astral:server-update-operation'
export const updateConfirmationBudget = 80 * 60_000
const active = (state?: SoftwareUpdateState) => state?.operationState === 'running' || ['checking', 'downloading', 'installing'].includes(state?.phase || '')
export class UpdateRequestError extends Error {
  constructor(message: string, readonly status: number) { super(message) }
}
export async function updateResponse<T>(pending: Promise<{ data?: T; error?: unknown; response?: Response }>): Promise<T> {
  const value = await pending
  if (value.error || value.response && !value.response.ok) {
    const error = value.error
    const message = error && typeof error === 'object' && 'message' in error && typeof error.message === 'string' ? error.message : 'The update request could not be completed.'
    throw new UpdateRequestError(message, value.response?.status || 0)
  }
  if (value.data === undefined) throw new Error('The update service returned no response.')
  return value.data
}

/** A lost connection is not an update result. Only the same durable operation
 * and its exact running target version may confirm completion after restart. */
export class SoftwareUpdates {
  state?: SoftwareUpdateState
  error = ''
  waiting = false
  busy = false
  onChange?: () => void
  onUpdated?: () => void
  private attempt?: Attempt
  private timer?: ReturnType<typeof setTimeout>
  private stopped = false
  private generation = 0
  private stickyError = false
  private stickyRetry: 'check' | 'update' = 'check'

  constructor(private readonly transport: UpdateTransport, private readonly storage?: Persistence, private readonly now = Date.now) {
    try {
      const pending = JSON.parse(storage?.getItem(storageKey) || 'null') as Attempt | null
      if (pending && typeof pending.from === 'string' && Number.isFinite(pending.started) && pending.started <= now() && now() - pending.started < updateConfirmationBudget &&
        [pending.id, pending.previousId, pending.target].every(value => value === undefined || typeof value === 'string')) this.attempt = pending
    } catch { /* Browser storage is optional; the running page still tracks its operation. */ }
  }

  async start() { this.stopped = false; await this.refresh() }
  stop() { this.stopped = true; clearTimeout(this.timer); this.generation++ }
  private notify() { this.onChange?.() }
  private unconfirmed(message: string, retry: 'check' | 'update' = 'check') {
    this.error = message
    this.stickyError = true
    this.stickyRetry = retry
    this.state = { ...(this.state || { currentVersion: '' }), phase: 'error', retry, error: message }
  }
  private persist() {
    try {
      if (this.attempt) this.storage?.setItem(storageKey, JSON.stringify(this.attempt))
      else this.storage?.removeItem(storageKey)
    } catch { /* Disabled session storage must not interrupt an update. */ }
  }
  private accept(value: SoftwareUpdateState) {
    this.state = value
    this.waiting = false
    let reload = false
    const pending = this.attempt
    if (pending) {
      if (!pending.id && value.operationId && value.operationId !== pending.previousId) pending.id = value.operationId
      if (pending.id && value.operationId === pending.id) {
        if (value.operationVersion) pending.target = value.operationVersion
        if (value.operationState === 'complete' && pending.target && value.currentVersion === pending.target) {
          this.attempt = undefined
          this.waiting = false
          reload = value.currentVersion !== pending.from
        } else if (value.operationState === 'complete') {
          this.attempt = undefined
          this.unconfirmed('The running version does not match the completed update target. Check the installed version before updating again.')
        } else if (value.operationState === 'failed') {
          this.attempt = undefined
          this.waiting = false
          this.state = { ...value, phase: 'error', retry: 'update', error: value.error || 'The update did not complete with its expected version.' }
        }
      }
      if (this.attempt && pending.id && value.operationId && value.operationId !== pending.id) {
        this.attempt = undefined
        this.unconfirmed('A different update operation is now reported. The requested update result could not be confirmed.')
      }
      if (this.attempt && this.now() - pending.started >= updateConfirmationBudget) {
        this.attempt = undefined
        this.waiting = false
        this.unconfirmed('The update result could not be confirmed. Reconnect and check the installed version before trying again.')
      }
      this.persist()
    } else if (value.operationState === 'running') {
      this.attempt = { id: value.operationId, target: value.operationVersion, from: value.currentVersion, started: this.now() }
      this.persist()
    }
    this.busy = Boolean(this.attempt) || active(this.state)
    this.notify()
    if (reload) this.onUpdated?.()
  }
  private schedule() {
    clearTimeout(this.timer)
    if (!this.stopped) this.timer = setTimeout(() => { void this.refresh() }, this.attempt || active(this.state) ? 1000 : 15_000)
  }
  async refresh() {
    const generation = ++this.generation
    try {
      const value = await this.transport.state()
      if (this.stopped || generation !== this.generation) return
      if (!this.stickyError) this.error = ''
      this.accept(value)
      if (this.stickyError) { this.unconfirmed(this.error, this.stickyRetry); this.notify() }
    } catch (error) {
      if (this.stopped || generation !== this.generation) return
      if (this.attempt) {
        this.waiting = true
        this.busy = true
        if (this.now() - this.attempt.started >= updateConfirmationBudget) {
          this.attempt = undefined
          this.busy = false
          this.waiting = false
          this.unconfirmed('The update result could not be confirmed. Reconnect and check the installed version before trying again.')
          this.persist()
        }
      } else this.error = error instanceof Error ? error.message : String(error)
      this.notify()
    } finally { if (generation === this.generation) this.schedule() }
  }
  async act(intent: 'check' | 'update') {
    if (this.busy) return
    clearTimeout(this.timer)
    this.generation++
    this.busy = true
    this.error = ''
    this.stickyError = false
    if (intent === 'update') {
      this.attempt = { previousId: this.state?.operationId, from: this.state?.currentVersion || '', started: this.now() }
      this.persist()
    }
    this.notify()
    try { this.accept(await (intent === 'update' ? this.transport.apply() : this.transport.check())) }
    catch (error) {
      if (intent === 'update' && error instanceof UpdateRequestError && error.status >= 400 && error.status < 500) {
        this.attempt = undefined
        this.waiting = false
        this.persist()
        this.unconfirmed(error.message, 'update')
      } else if (intent === 'update') this.waiting = true
      else {
        this.unconfirmed(error instanceof Error ? error.message : String(error))
      }
      this.notify()
    } finally {
      this.busy = Boolean(this.attempt) || active(this.state)
      this.notify()
      this.schedule()
    }
  }
}
