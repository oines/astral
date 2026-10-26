import type { DesktopUpdateState } from '../shared/updates'

export const updateFeed = 'https://github.com/oines/astral/releases/latest/download/'
export const releasesPage = 'https://github.com/oines/astral/releases/latest'

/** GenericProvider appends its own platform suffix to this channel. */
export function updateChannel(architecture: string): string {
  if (!['x64', 'arm64'].includes(architecture)) throw new Error('Unsupported update architecture.')
  return `latest-${architecture}`
}

interface UpdateInfo { version: string }
interface UpdateEngine {
  autoDownload: boolean
  autoInstallOnAppQuit: boolean
  allowPrerelease: boolean
  allowDowngrade: boolean
  disableDifferentialDownload: boolean
  on(event: 'checking-for-update', listener: () => void): unknown
  on(event: 'update-available' | 'update-not-available' | 'update-downloaded', listener: (info: UpdateInfo) => void): unknown
  on(event: 'download-progress', listener: (progress: { percent: number }) => void): unknown
  on(event: 'error', listener: (error: Error) => void): unknown
  checkForUpdates(): Promise<{ downloadPromise?: Promise<unknown> | null } | null>
  quitAndInstall(isSilent?: boolean, isForceRunAfter?: boolean): void
}

/** Owns update state; no URL, file path, or installer argument comes from Renderer. */
export class DesktopUpdates {
  private current: DesktopUpdateState
  private checking?: Promise<DesktopUpdateState>
  private installing?: Promise<void>
  onState?: (state: DesktopUpdateState) => void
  onInstallError?: (error: Error) => void

  constructor(private readonly engine: UpdateEngine, version: string, private readonly stopServer: () => Promise<void>, unavailable?: DesktopUpdateState['unavailable']) {
    this.current = { phase: unavailable ? 'unavailable' : 'idle', currentVersion: version, ...(unavailable ? { unavailable } : {}) }
    engine.autoDownload = true
    // Never install during an ordinary quit: only our explicit stop-then-install path is safe.
    engine.autoInstallOnAppQuit = false
    engine.allowPrerelease = false
    engine.allowDowngrade = false
    // Each release is independently downloadable, including upgrades which skip versions.
    engine.disableDifferentialDownload = true
    engine.on('checking-for-update', () => this.set({ phase: 'checking', error: undefined }))
    engine.on('update-available', info => this.set({ phase: 'downloading', version: info.version, percent: 0, error: undefined }))
    engine.on('update-not-available', () => this.set({ phase: 'current', version: undefined, percent: undefined, error: undefined }))
    engine.on('download-progress', progress => this.set({ phase: 'downloading', percent: Math.max(0, Math.min(100, progress.percent)) }))
    engine.on('update-downloaded', info => this.set({ phase: 'ready', version: info.version, percent: 100, error: undefined }))
    engine.on('error', error => {
      const installing = this.current.phase === 'installing'
      this.set({ phase: 'error', error: error.message })
      if (installing) this.onInstallError?.(error)
    })
  }

  state(): DesktopUpdateState { return { ...this.current } }

  private set(patch: Partial<DesktopUpdateState>) {
    this.current = { ...this.current, ...patch }
    this.onState?.(this.state())
  }

  async check(): Promise<DesktopUpdateState> {
    if (this.checking) return this.checking
    if (['unavailable', 'downloading', 'ready', 'installing'].includes(this.current.phase)) return this.state()
    this.set({ phase: 'checking', error: undefined })
    this.checking = this.engine.checkForUpdates().then(result => {
      // Downloads fail independently of the check and emit an error event.
      void result?.downloadPromise?.catch(() => undefined)
      return this.state()
    }).catch(error => {
      this.set({ phase: 'error', error: error instanceof Error ? error.message : String(error) })
      return this.state()
    }).finally(() => { this.checking = undefined })
    return this.checking
  }

  async install(): Promise<void> {
    if (this.installing) return this.installing
    if (this.current.phase !== 'ready') throw new Error('An update has not finished downloading.')
    this.set({ phase: 'installing', error: undefined })
    this.installing = (async () => {
      try {
        await this.stopServer()
      } catch (error) {
        this.set({ phase: 'ready', error: error instanceof Error ? error.message : String(error) })
        throw error
      }
      // stopServer resolves only after the managed Server and its pipes have exited.
      try {
        this.engine.quitAndInstall(false, true)
      } catch (value) {
        const error = value instanceof Error ? value : new Error(String(value))
        this.set({ phase: 'error', error: error.message })
        this.onInstallError?.(error)
        throw error
      }
    })().finally(() => { this.installing = undefined })
    return this.installing
  }
}
