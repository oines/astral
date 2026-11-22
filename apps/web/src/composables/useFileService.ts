import { fileContent, getFileReference, listComputerDirectory, sessionComputerFileContent } from '@astral/sdk'
import { request } from '../lib/api'
import { previewFileURL, type PreviewFile } from '../lib/computer-file'
import type { FileRef } from '../lib/types'

export interface DirectoryEntry {
  name: string
  path: string
  is_dir: boolean
  size: number
  modified_at: string
}
export interface DirectoryPage {
  computer_id: string
  path: string
  parent: string
  entries: DirectoryEntry[]
  next_cursor?: string
}
export interface DirectoryOptions { cursor?: string; signal?: AbortSignal }
export interface FilePreviewResource {
  name: string
  mediaType: string
  content?: string
  partial: boolean
  url: string
  downloadURL: string
}
export interface FileServiceAdapter {
  resolveFile(id: string, sessionID: string, signal?: AbortSignal): Promise<FileRef>
  listDirectory(computerID: string, sessionID: string, path: string | undefined, options: DirectoryOptions): Promise<DirectoryPage>
  content(file: PreviewFile, sessionID: string, signal?: AbortSignal): Promise<{ data: Blob; headers: Headers }>
}

export function filePathName(path: string) { return path.split(/[\\/]/).filter(Boolean).at(-1) || path }

function responseName(disposition: string, fallback: string) {
  const encoded = /filename\*=UTF-8''([^;]+)/i.exec(disposition)?.[1]
  if (encoded) {
    try { return decodeURIComponent(encoded) } catch { /* Retain a usable name for malformed server headers. */ }
  }
  const plain = /filename=(?:"([^"]+)"|([^;]+))/i.exec(disposition)
  return plain?.[1] || plain?.[2]?.trim() || fallback
}

function decodeText(buffer: ArrayBuffer, contentType: string, partial: boolean): string | undefined {
  const bytes = new Uint8Array(buffer)
  const charset = /(?:^|;)\s*charset\s*=\s*(?:"([^"]*)"|([^;\s]+))/i.exec(contentType)
  // A BOM identifies the bytes themselves, including when an HTTP server uses
  // a generic or conflicting charset. Other encodings require an explicit label.
  const encoding = bytes[0] === 0xff && bytes[1] === 0xfe ? 'utf-16le'
    : bytes[0] === 0xfe && bytes[1] === 0xff ? 'utf-16be'
      : bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf ? 'utf-8'
        : charset?.[1]?.trim() || charset?.[2] || 'utf-8'
  try {
    // A bounded range can end inside a character; retain only its valid prefix.
    const text = new TextDecoder(encoding, { fatal: true }).decode(bytes, { stream: partial })
    if (!text.includes('\u0000')) return text
  } catch { /* Malformed bytes and unsupported charsets retain the download action. */ }
}

// SDK details stay at this boundary. Both file surfaces use the source
// Computer explicitly; browsing never changes the Session's execution binding.
const adapter: FileServiceAdapter = {
  resolveFile: (id, sessionID, signal) => request(getFileReference({ path: { id }, query: { session_id: sessionID }, signal })),
  listDirectory: (id, sessionID, path, options) => request(listComputerDirectory({ path: { id }, query: { session_id: sessionID, path, cursor: options.cursor, limit: 200 }, signal: options.signal })),
  content: async (file, sessionID, signal) => {
    const options = { headers: { Range: 'bytes=0-262143' }, parseAs: 'blob' as const, signal, throwOnError: true as const }
    const result = file.id
      ? await fileContent({ ...options, path: { id: file.id }, query: { session_id: sessionID } })
      : await sessionComputerFileContent({ ...options, path: { id: sessionID, computer: file.computer_id }, query: { path: file.path } })
    if (!(result.data instanceof Blob)) throw new Error('The server returned no file content.')
    return { data: result.data, headers: result.response.headers }
  },
}

export function createFileService(api: FileServiceAdapter) {
  return {
    resolveFile: api.resolveFile,
    listDirectory(source: Pick<FileRef, 'computer_id'>, sessionID: string, path?: string, options: DirectoryOptions = {}) {
      return api.listDirectory(source.computer_id, sessionID, path, options)
    },
    async preview(file: PreviewFile, sessionID: string, signal?: AbortSignal): Promise<FilePreviewResource> {
      const { data, headers } = await api.content(file, sessionID, signal)
      const contentType = headers.get('Content-Type') || file.media_type || ''
      const mediaType = contentType.split(';')[0]!.trim().toLowerCase()
      const name = responseName(headers.get('Content-Disposition') || '', file.display_name || filePathName(file.path))
      const range = /bytes (\d+)-(\d+)\/(\d+)/.exec(headers.get('Content-Range') || '')
      const partial = Boolean(range && Number(range[2]) + 1 < Number(range[3]))
      let content: string | undefined
      if (!/^(image|audio|video)\//.test(mediaType) && mediaType !== 'application/pdf') {
        content = decodeText(await data.arrayBuffer(), contentType, partial)
      }
      return { name, mediaType, content, partial, url: previewFileURL(file, sessionID), downloadURL: previewFileURL(file, sessionID, true) }
    },
  }
}
export type FileService = ReturnType<typeof createFileService>
export const fileService = createFileService(adapter)
export const resolveFile = fileService.resolveFile
