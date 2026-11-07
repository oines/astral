export interface DesktopFileTarget {
  computerID: string
  sessionID: string
  /** Existing references take precedence over a renderer-supplied path. */
  fileID?: string
  path?: string
  /** Required with a path; directories open, regular files are selected. */
  directory?: boolean
}

export interface DesktopFileBridge {
  canReveal: (target: DesktopFileTarget) => Promise<boolean>
  reveal: (target: DesktopFileTarget) => Promise<void>
}
