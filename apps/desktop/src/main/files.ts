import { readFile, realpath, stat } from 'node:fs/promises'
import { basename, dirname, isAbsolute, join, resolve } from 'node:path'
import type { IpcMainInvokeEvent } from 'electron'
import type { DesktopFileTarget } from '../shared/files'

interface NativeFileDependencies {
  localDirectory: string
  serverURL: string
  alive: () => boolean
  request: (path: string, method?: 'GET' | 'POST') => Promise<unknown>
  shell: { showItemInFolder: (path: string) => void; openPath: (path: string) => Promise<string> }
}
interface LocalComputer { id: string; bootID: string }
function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid file source response.')
  return value as Record<string, unknown>
}
function identifier(value: unknown): value is string { return typeof value === 'string' && value.length > 0 && value.length <= 256 && !Array.from(value).some(character => character.charCodeAt(0) < 32 || character.charCodeAt(0) === 127) }
function localPath(value: unknown): string {
  if (typeof value !== 'string' || value.length > 32768 || value.includes('\u0000') || !isAbsolute(value)) throw new Error('A local absolute path is required.')
  return value
}
export function desktopFileTarget(value: unknown): DesktopFileTarget {
  const input = object(value)
  if (Object.keys(input).some(key => !['computerID', 'sessionID', 'fileID', 'path', 'directory'].includes(key)) || !identifier(input.computerID) || !identifier(input.sessionID)) throw new Error('Invalid file location request.')
  if (input.fileID !== undefined) {
    if (!identifier(input.fileID)) throw new Error('Invalid file reference.')
    return { computerID: input.computerID, sessionID: input.sessionID, fileID: input.fileID }
  }
  if (typeof input.directory !== 'boolean') throw new Error('Specify whether the location is a directory.')
  return { computerID: input.computerID, sessionID: input.sessionID, path: localPath(input.path), directory: input.directory }
}

/** Only the Daemon managed by this Desktop's child Server can name local paths. */
export class DesktopFiles {
  constructor(private readonly dependencies: NativeFileDependencies) {}

  private async localComputer(): Promise<LocalComputer> {
    const d = this.dependencies
    if (!d.alive()) throw new Error('The local Astral service is not running.')
    // Read only the fixed managed Daemon registration. Never return its secret
    // to the renderer or accept a registration directory from an IPC argument.
    const credential = object(JSON.parse(await readFile(join(d.localDirectory, 'credentials.json'), 'utf8')))
    if (!identifier(credential.computer_id) || typeof credential.server_url !== 'string' || new URL(credential.server_url).origin !== d.serverURL) throw new Error('The local Computer registration does not match this Desktop.')
    const computers = await d.request('/api/v1/computers')
    if (!Array.isArray(computers)) throw new Error('Invalid Computer response.')
    const computer = computers.map(object).find(value => value.id === credential.computer_id)
    if (!computer || computer.local !== true || computer.kind !== 'local' || computer.container || computer.revoked !== false || computer.online !== true || !identifier(computer.boot_id) || typeof computer.root !== 'string') throw new Error('The source is not this Desktop’s connected local Computer.')
    if (await realpath(computer.root) !== await realpath(d.localDirectory)) throw new Error('The local Computer directory does not match this Desktop.')
    return { id: credential.computer_id, bootID: computer.boot_id }
  }

  private async source(target: DesktopFileTarget, action: boolean): Promise<{ path: string; directory: boolean }> {
    const d = this.dependencies
    const query = new URLSearchParams({ session_id: target.sessionID })
    const computerPath = `/api/v1/computers/${encodeURIComponent(target.computerID)}/files`
    if (target.fileID) {
      const ref = object(await d.request(`/api/v1/files/${encodeURIComponent(target.fileID)}?${query}`))
      if (ref.computer_id !== target.computerID) throw new Error('The file belongs to another Computer.')
      const path = localPath(ref.path)
      // Historical metadata deliberately survives revoked Computer access.
      // A native location requires a fresh Hub-authorized source operation too.
      query.set('path', path)
      query.set('limit', '1')
      const directory = object(await d.request(`${computerPath}/directory?${query}`))
      if (directory.computer_id !== target.computerID) throw new Error('The directory belongs to another Computer.')
      return { path, directory: false }
    }
    if (target.directory || !action) {
      query.set('path', target.directory ? target.path! : dirname(target.path!))
      query.set('limit', '1')
      const directory = object(await d.request(`${computerPath}/directory?${query}`))
      if (directory.computer_id !== target.computerID) throw new Error('The directory belongs to another Computer.')
      const canonical = localPath(directory.path)
      // Capability checks do not create a FileRef merely to open a menu. The
      // action resolves a regular file through the authoritative reference API.
      return { path: target.directory ? canonical : join(canonical, basename(target.path!)), directory: !!target.directory }
    }
    query.set('path', target.path!)
    const result = object(await d.request(`${computerPath}/reference?${query}`, 'POST'))
    const ref = object(result.file)
    if (ref.computer_id !== target.computerID) throw new Error('The file belongs to another Computer.')
    return { path: localPath(ref.path), directory: false }
  }

  private async checked(value: unknown, action: boolean) {
    const target = desktopFileTarget(value)
    const identity = await this.localComputer()
    if (target.computerID !== identity.id) throw new Error('Remote and container files cannot be opened on this Desktop.')
    const source = await this.source(target, action)
    const path = await realpath(source.path)
    if (!isAbsolute(path) || resolve(path) !== path) throw new Error('Invalid local file path.')
    const info = await stat(path)
    if (source.directory ? !info.isDirectory() : !info.isFile()) throw new Error('The selected file or directory is no longer available.')
    const latest = await this.localComputer()
    if (latest.id !== identity.id || latest.bootID !== identity.bootID || !this.dependencies.alive()) throw new Error('The local Computer changed while checking this file.')
    return { path, directory: source.directory }
  }

  async canReveal(value: unknown): Promise<boolean> {
    try { await this.checked(value, false); return true } catch { return false }
  }

  async reveal(value: unknown): Promise<void> {
    const source = await this.checked(value, true)
    if (source.directory) {
      const error = await this.dependencies.shell.openPath(source.path)
      if (error) throw new Error(error)
    } else this.dependencies.shell.showItemInFolder(source.path)
  }
}

export function registerDesktopFileHandlers(
  ipc: { handle: (channel: string, listener: (event: IpcMainInvokeEvent, target: unknown) => unknown) => void },
  assertSender: (event: IpcMainInvokeEvent) => void,
  files: DesktopFiles,
) {
  ipc.handle('astral:files-can-reveal', (event, target) => { assertSender(event); return files.canReveal(target) })
  ipc.handle('astral:files-reveal', (event, target) => { assertSender(event); return files.reveal(target) })
}
