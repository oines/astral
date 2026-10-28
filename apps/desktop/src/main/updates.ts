import type { DesktopUpdateState } from '../shared/updates'

export const updateFeed = 'https://github.com/oines/astral/releases/latest/download/'
export const releasesPage = 'https://github.com/oines/astral/releases/latest'

/** GenericProvider appends its own platform suffix to this channel. */
export function updateChannel(architecture: string): string {
  if (!['x64', 'arm64'].includes(architecture)) throw new Error('Unsupported update architecture.')
  return `latest-${architecture}`
}

interface UpdateCheckResult {
  isUpdateAvailable: boolean
  updateInfo: { version: string }
}
interface UpdateEngine {
  autoDownload: boolean
  autoInstallOnAppQuit: boolean
  allowPrerelease: boolean
  allowDowngrade: boolean
  disableDifferentialDownload: boolean
  on(event: 'download-progress', listener: (progress: { percent: number }) => void): unknown
  on(event: 'error', listener: (error: Error) => void): unknown
  checkForUpdates(): Promise<UpdateCheckResult | null>
  downloadUpdate(): Promise<unknown>
  quitAndInstall(isSilent?: boolean, isForceRunAfter?: boolean): void
}

/** Owns update state; no URL, file path, or installer argument comes from Renderer. */
export class DesktopUpdates {
  private current: DesktopUpdateState
  private checking?: Promise<DesktopUpdateState>
  private updating?: Promise<void>
  private serverStopped = false
  private installErrorReported = false
  onState?: (state: DesktopUpdateState) => void
  onInstallError?: (error: Error) => void

  constructor(private readonly engine: UpdateEngine, version: string, private readonly stopServer: () => Promise<void>, unavailable?: DesktopUpdateState['unavailable']) {
    this.current = { phase: unavailable ? 'unavailable' : 'idle', currentVersion: version, ...(unavailable ? { unavailable } : {}) }
    engine.autoDownload = false
    // Neither metadata checks nor ordinary quits may install an update.
    engine.autoInstallOnAppQuit = false
    engine.allowPrerelease = false
    engine.allowDowngrade = false
    // Each release is independently downloadable, including upgrades which skip versions.
    engine.disableDifferentialDownload = true
    engine.on('download-progress', progress => {
      if (this.current.phase === 'downloading' && Number.isFinite(progress.percent)) {
        this.set({ percent: Math.max(0, Math.min(100, progress.percent)) })
      }
    })
    engine.on('error', error => {
      // Checks/downloads reject their own promises. Installer errors can arrive later.
      if (this.serverStopped) this.installFailed(error)
    })
  }

  state(): DesktopUpdateState { return { ...this.current } }

  private set(patch: Partial<DesktopUpdateState>) {
    this.current = { ...this.current, ...patch }
    this.onState?.(this.state())
  }

  private async latest(): Promise<UpdateCheckResult> {
    this.set({ phase: 'checking', version: undefined, percent: undefined, error: undefined, retry: undefined })
    const result = await this.engine.checkForUpdates()
    if (!result) throw new Error('The update service is unavailable.')
    this.set({ phase: result.isUpdateAvailable ? 'available' : 'current', version: result.isUpdateAvailable ? result.updateInfo.version : undefined })
    return result
  }

  private failed(value: unknown, retry: 'check' | 'update'): Error {
    const error = value instanceof Error ? value : new Error(String(value))
    this.set({ phase: 'error', error: error.message, retry, percent: undefined })
    return error
  }

  private installFailed(error: Error) {
    if (this.installErrorReported) return
    this.installErrorReported = true
    this.failed(error, 'update')
    this.onInstallError?.(error)
  }

  async check(automatic = false): Promise<DesktopUpdateState> {
    if (this.checking) return this.checking
    if (this.serverStopped || this.updating || ['unavailable', 'installing'].includes(this.current.phase)) return this.state()
    // A periodic check must not hide the failure of an update the user requested.
    if (automatic && this.current.phase === 'error' && this.current.retry === 'update') return this.state()
    this.checking = this.latest().then(() => this.state()).catch(error => {
      this.failed(error, 'check')
      return this.state()
    }).finally(() => { this.checking = undefined })
    return this.checking
  }

  async update(): Promise<void> {
    if (this.updating) return this.updating
    if (this.current.phase === 'unavailable') throw new Error('In-app updates are unavailable for this installation.')
    if (this.serverStopped) throw new Error('Astral is restarting after the update attempt.')
    if (this.current.phase === 'installing') return
    const checking = this.checking
    this.updating = Promise.resolve().then(async () => {
      try {
        // Finish any earlier lookup, then resolve Latest again for this explicit action.
        if (checking) await checking
        const result = await this.latest()
        if (!result.isUpdateAvailable) return
        this.set({ phase: 'downloading', percent: 0 })
        await this.engine.downloadUpdate()
        this.set({ phase: 'installing', percent: 100 })
        await this.stopServer()
        // The managed Server and its pipes have exited; main can reopen on failure.
        this.serverStopped = true
        this.engine.quitAndInstall(false, true)
        if (this.installErrorReported) throw new Error(this.current.error)
      } catch (value) {
        const error = value instanceof Error ? value : new Error(String(value))
        if (this.serverStopped) this.installFailed(error)
        else this.failed(error, 'update')
        throw error
      }
    }).finally(() => { this.updating = undefined })
    return this.updating
  }
}
