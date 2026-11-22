import { lstat, readdir, realpath, unlink } from 'node:fs/promises'
import path from 'node:path'
import type { DesktopUpdateState } from '../shared/updates'

interface CacheConfig {
  directory: string
  state: () => DesktopUpdateState
}

export class UpdateCacheConflict extends Error {}
const artifactPattern = /^Astral-((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))-(?:mac-(?:x64|arm64)\.zip|win-(?:x64|arm64)\.exe|linux-(?:x86_64|arm64)\.AppImage)$/
const missing = (error: unknown) => (error as NodeJS.ErrnoException).code === 'ENOENT'

export class DesktopUpdateCache {
  constructor(private readonly config: CacheConfig) {}

  private updating(state: DesktopUpdateState) {
    return state.operationState === 'running' || ['checking', 'downloading', 'installing'].includes(state.phase)
  }

  private async packages() {
    const result: { file: string; bytes: number; cleanable: boolean }[] = []
    const state = this.config.state()
    if (this.updating(state) || !/^\d+\.\d+\.\d+$/.test(state.currentVersion)) return result
    if (path.basename(this.config.directory) !== 'astral-updater') return result
    const current = state.currentVersion.split('.').map(BigInt)
    const directory = path.resolve(this.config.directory, 'pending')
    try {
      if (await realpath(directory) !== directory) return result
      for (const entry of await readdir(directory, { withFileTypes: true })) {
        const match = artifactPattern.exec(entry.name)
        if (!entry.isFile() || !match) continue
        const version = match[1]!.split('.').map(BigInt)
        const diff = version.findIndex((part, i) => part !== current[i])
        const cleanable = diff === -1 || version[diff]! < current[diff]!
        const file = path.join(directory, entry.name)
        try {
          const info = await lstat(file)
          if (info.isFile()) result.push({ file, bytes: info.size, cleanable })
        } catch (error) { if (!missing(error)) throw error }
      }
    } catch (error) { if (!missing(error)) throw error }
    return result
  }

  async inventory() {
    const files = await this.packages()
    return {
      total_bytes: files.reduce((total, file) => total + file.bytes, 0),
      cleanable_bytes: files.reduce((total, file) => total + (file.cleanable ? file.bytes : 0), 0),
    }
  }

  async clean() {
    let removed = 0
    for (const item of await this.packages()) {
      if (!item.cleanable) continue
      try {
        if (await realpath(path.dirname(item.file)) !== path.dirname(item.file)) continue
        const info = await lstat(item.file)
        if (this.updating(this.config.state())) break
        if (!info.isFile()) continue
        await unlink(item.file)
        removed += info.size
      } catch (error) {
        if (!missing(error)) return { removed_bytes: removed, error: 'Could not remove the old Desktop update package.' }
      }
    }
    return { removed_bytes: removed }
  }
}
