export type UpdatePhase = 'idle' | 'checking' | 'current' | 'downloading' | 'ready' | 'installing' | 'error' | 'unavailable'
export interface DesktopUpdateState {
  phase: UpdatePhase
  currentVersion: string
  version?: string
  percent?: number
  error?: string
  unavailable?: 'development' | 'package' | 'windows-zip' | 'signature'
}
export interface DesktopUpdateBridge {
  state: () => Promise<DesktopUpdateState>
  check: () => Promise<DesktopUpdateState>
  install: () => Promise<void>
  onState: (callback: (state: DesktopUpdateState) => void) => () => void
}
