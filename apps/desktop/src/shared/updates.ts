export type UpdatePhase = 'idle' | 'checking' | 'current' | 'available' | 'downloading' | 'installing' | 'error' | 'unavailable'
export interface DesktopUpdateState {
  phase: UpdatePhase
  currentVersion: string
  version?: string
  percent?: number
  error?: string
  retry?: 'check' | 'update'
  unavailable?: 'development' | 'package' | 'windows-zip' | 'signature'
}
export interface DesktopUpdateBridge {
  state: () => Promise<DesktopUpdateState>
  check: () => Promise<DesktopUpdateState>
  update: () => Promise<void>
  onState: (callback: (state: DesktopUpdateState) => void) => () => void
}
