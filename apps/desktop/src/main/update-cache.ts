import { constants } from 'node:fs'
import { createHash, randomUUID } from 'node:crypto'
import { lstat, mkdir, open, readFile, realpath, rename, unlink } from 'node:fs/promises'
import path from 'node:path'
import type { DesktopUpdateState } from '../shared/updates'

export interface UpdateCacheItem {
  token: string
  kind: 'update_cache'
  name: string
  bytes: number
  protected: boolean
  reason?: string
}
interface FileIdentity { name: string; fingerprint: string; bytes: number; removed?: boolean }
interface Candidate { item: UpdateCacheItem; version: string; root: string; files: FileIdentity[] }
interface CleanupRecord {
  schema: 1
  operation: string
  token: string
  version: string
  root: string
  files: FileIdentity[]
  removedBytes: number
  complete: boolean
}
interface CacheConfig {
  directory: string
  configuration: string
  journalDirectory: string
  platform: NodeJS.Platform
  architecture: string
  state: () => DesktopUpdateState
  recoveryBlocked: () => boolean
}

export class UpdateCacheConflict extends Error {}
const tokenPattern = /^desktop:[a-f0-9]{64}$/
const artifactPattern = /^Astral-((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))-(mac|win|linux)-(x64|arm64)\.(zip|exe|AppImage)$/
const digest = (value: string) => createHash('sha256').update(value).digest('hex')
const missing = (error: unknown) => (error as NodeJS.ErrnoException).code === 'ENOENT'

/** Layout verified against electron-updater 6.8.11's AppUpdater and
 * DownloadedUpdateHelper: configured cache root / pending / update-info.json.
 * Unknown files, Squirrel caches and differential baselines are never adopted. */
export class DesktopUpdateCache {
  constructor(private readonly config: CacheConfig) {}

  private artifact(name: string): string | undefined {
    const match = artifactPattern.exec(name)
    const platform = { darwin: ['mac', 'zip'], win32: ['win', 'exe'], linux: ['linux', 'AppImage'] }[this.config.platform as 'darwin' | 'win32' | 'linux']
    if (!match || !platform || match[2] !== platform[0] || match[3] !== this.config.architecture || match[4] !== platform[1]) return
    return match[1]
  }

  private reason(version: string): string | undefined {
    const state = this.config.state()
    if (this.config.recoveryBlocked() || state.operationState === 'failed' && state.operationVersion === version) return 'update_recovery'
    if (state.operationState === 'running' || ['checking', 'downloading', 'installing'].includes(state.phase)) return 'update_running'
    const current = state.currentVersion.split('.').map(value => /^\d+$/.test(value) ? BigInt(value) : -1n)
    const cached = version.split('.').map(BigInt)
    if (current.length !== 3 || current.some(value => value < 0n)) return 'update_recovery'
    for (let i = 0; i < 3; i++) {
      if (cached[i]! > current[i]!) return 'update_pending'
      if (cached[i]! < current[i]!) break
    }
  }

  private async root(requirePending = true): Promise<string | undefined> {
    // The bundled config is trusted main-process input, never a browser path.
    // Refuse an unfamiliar updater configuration rather than guessing its cache.
    const raw = await this.smallFile(this.config.configuration)
    const values = (key: string) => [...raw.matchAll(new RegExp(`^${key}:\\s*([^\\r\\n]+)\\s*$`, 'gm'))].map(value => value[1]!.trim())
    if (JSON.stringify(values('updaterCacheDirName')) !== '["astral-updater"]' || JSON.stringify(values('provider')) !== '["generic"]' ||
      JSON.stringify(values('url')) !== '["https://github.com/oines/astral/releases/latest/download/"]') return
    const root = path.resolve(this.config.directory)
    if (path.basename(root) !== 'astral-updater') return
    try {
      if (await realpath(root) !== root) return
      try { if (await realpath(path.join(root, 'pending')) !== path.join(root, 'pending')) return }
      catch (error) { if (requirePending || !missing(error)) throw error }
    } catch (error) { if (missing(error)) return; throw error }
    return root
  }

  private async smallFile(file: string): Promise<string> {
    const handle = await open(file, constants.O_RDONLY | constants.O_NOFOLLOW)
    try {
      const info = await handle.stat()
      if (!info.isFile() || info.size > 64 << 10) throw new Error('Update cache metadata is not a bounded regular file.')
      return await handle.readFile('utf8')
    } finally { await handle.close() }
  }

  private async identity(root: string, name: string): Promise<FileIdentity> {
    const info = await lstat(path.join(root, 'pending', name), { bigint: true })
    if (!info.isFile() || info.nlink !== 1n || info.size > BigInt(Number.MAX_SAFE_INTEGER)) throw new UpdateCacheConflict('The update cache file is no longer eligible.')
    return { name, bytes: Number(info.size), fingerprint: digest([info.dev, info.ino, info.mode, info.size, info.mtimeNs, info.ctimeNs].join(':')) }
  }

  private async candidate(): Promise<Candidate | undefined> {
    let root: string | undefined
    try { root = await this.root() } catch (error) { if (missing(error)) return; throw error }
    if (!root) return
    let raw: string
    try { raw = await this.smallFile(path.join(root, 'pending', 'update-info.json')) }
    catch (error) { if (missing(error)) return; throw error }
    let metadata: { fileName?: unknown; sha512?: unknown }
    try { metadata = JSON.parse(raw) } catch { return }
    if (!metadata || typeof metadata.fileName !== 'string' || typeof metadata.sha512 !== 'string' ||
      Buffer.from(metadata.sha512, 'base64').length !== 64 || Buffer.from(metadata.sha512, 'base64').toString('base64') !== metadata.sha512) return
    const version = this.artifact(metadata.fileName)
    if (!version) return
    let files: FileIdentity[]
    try { files = [await this.identity(root, metadata.fileName), await this.identity(root, 'update-info.json')] }
    catch (error) { if (missing(error) || error instanceof UpdateCacheConflict) return; throw error }
    const token = `desktop:${digest(JSON.stringify({ root, metadata: digest(raw), files }))}`
    const reason = this.reason(version)
    return { root, version, files, item: { token, kind: 'update_cache', name: metadata.fileName, bytes: files.reduce((sum, file) => sum + file.bytes, 0), protected: !!reason, ...(reason ? { reason } : {}) } }
  }

  async inventory(): Promise<UpdateCacheItem[]> {
    const candidate = await this.candidate()
    return candidate ? [candidate.item] : []
  }

  private validRecord(value: unknown, operation: string, token: string, root: string): value is CleanupRecord {
    const record = value as Partial<CleanupRecord> | null
    return !!record && record.schema === 1 && record.operation === operation && record.token === token && record.root === root &&
      typeof record.version === 'string' && typeof record.complete === 'boolean' && Number.isSafeInteger(record.removedBytes) && record.removedBytes! >= 0 &&
      Array.isArray(record.files) && record.files.length === 2 && this.artifact(record.files[0]?.name || '') === record.version && record.files[1]?.name === 'update-info.json' &&
      record.files.every(file => typeof file.fingerprint === 'string' && /^[a-f0-9]{64}$/.test(file.fingerprint) && Number.isSafeInteger(file.bytes) && file.bytes >= 0 &&
        (file.removed === undefined || typeof file.removed === 'boolean'))
  }

  private async save(file: string, record: CleanupRecord) {
    const directory = path.dirname(file)
    await mkdir(directory, { recursive: true, mode: 0o700 })
    if (await realpath(directory) !== path.resolve(directory)) throw new Error('The update cleanup journal directory is not trusted.')
    // Persist the newly created journal directory's entry in userData before
    // the first durable intent can authorize removing an installer.
    await this.syncDirectory(path.dirname(directory))
    const temporary = `${file}.${randomUUID()}.tmp`
    const handle = await open(temporary, 'wx', 0o600)
    try { await handle.writeFile(`${JSON.stringify(record)}\n`); await handle.sync() }
    finally { await handle.close() }
    await rename(temporary, file)
    await this.syncDirectory(directory)
  }

  private async syncDirectory(directory: string) {
    let handle: Awaited<ReturnType<typeof open>> | undefined
    try { handle = await open(directory, constants.O_RDONLY); await handle.sync() }
    catch (error) {
      // Windows does not expose directory FlushFileBuffers through Node on
      // every filesystem. Replays therefore also reconcile original identities
      // even for completed receipts; missing intent cannot authorize deletion.
      if (process.platform !== 'win32' || !['EINVAL', 'EISDIR', 'EPERM', 'EACCES', 'ENOTSUP'].includes((error as NodeJS.ErrnoException).code || '')) throw error
    } finally { await handle?.close() }
  }

  /** Called under DesktopUpdateControl's update/cleanup admission barrier. */
  async clean(operation: string, token: string): Promise<number> {
    if (!operation.trim() || operation.length > 128 || !tokenPattern.test(token)) throw new UpdateCacheConflict('Invalid update cache cleanup identity.')
    const journal = path.join(this.config.journalDirectory, `${digest(`${operation}\0${token}`)}.json`)
    const expectedRoot = path.resolve(this.config.directory)
    let record: CleanupRecord
    try {
      const value: unknown = JSON.parse(await readFile(journal, 'utf8'))
      if (!this.validRecord(value, operation, token, expectedRoot)) throw new UpdateCacheConflict('The update cache cleanup record is invalid.')
      record = value
    } catch (error) {
      if (!missing(error)) throw error
      const candidate = await this.candidate()
      if (!candidate || candidate.item.token !== token || candidate.item.protected) throw new UpdateCacheConflict('The update cache changed or is needed by an update. Refresh the inventory.')
      record = { schema: 1, operation, token, root: candidate.root, version: candidate.version, files: candidate.files, removedBytes: 0, complete: false }
      await this.save(journal, record)
    }
    const root = await this.root(false)
    if (!root || root !== record.root) {
      if (record.complete) return record.removedBytes
      throw new UpdateCacheConflict('The update cache is no longer available.')
    }
    for (const file of record.files) {
      let identity: FileIdentity
      try { identity = await this.identity(root, file.name) }
      catch (error) {
        if (!missing(error)) throw error
        if (file.removed) continue
        // An interrupted process may have unlinked this file before saving its
        // progress. Reconcile absence without claiming additional freed bytes.
        await this.syncDirectory(path.join(root, 'pending'))
        file.removed = true
        await this.save(journal, record)
        continue
      }
      if (identity.fingerprint !== file.fingerprint) {
        // The recorded file was already removed; a new download with the same
        // name belongs to a different operation and must survive this replay.
        if (file.removed) continue
        throw new UpdateCacheConflict('The selected update cache file changed. Refresh the inventory.')
      }
      if (this.reason(record.version)) throw new UpdateCacheConflict('The update cache is required by an active or pending update.')
      await unlink(path.join(root, 'pending', file.name))
      await this.syncDirectory(path.join(root, 'pending'))
      if (!file.removed) record.removedBytes += file.bytes
      file.removed = true
      await this.save(journal, record)
    }
    record.complete = true
    await this.save(journal, record)
    return record.removedBytes
  }
}
