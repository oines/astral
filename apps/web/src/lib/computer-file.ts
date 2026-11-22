import type { FileRef } from './types'
import { fileURL } from './api'

export type ComputerFile = Pick<FileRef, 'computer_id' | 'path'>
export type PreviewFile = ComputerFile & Partial<Pick<FileRef, 'id' | 'display_name' | 'media_type' | 'version' | 'revision'>>

export function parseComputerFile(uri: string): ComputerFile | undefined {
  const match = /^astral-file:\/\/([A-Za-z0-9_-]+)\?path=([^&#]+)$/.exec(uri)
  if (!match) return
  try {
    const path = decodeURIComponent(match[2]!.replace(/\+/g, ' '))
    if (path && !path.includes('\u0000')) return { computer_id: match[1]!, path }
  } catch { /* Incomplete or malformed percent escape. */ }
}

export function computerFileURI(file: ComputerFile) {
  return `astral-file://${file.computer_id}?path=${encodeURIComponent(file.path)}`
}

export function previewFileURL(file: PreviewFile, sessionID: string, download = false) {
  if (file.id) return fileURL(file.id, sessionID, download)
  const path = `/api/v1/sessions/${encodeURIComponent(sessionID)}/computers/${encodeURIComponent(file.computer_id)}/files/content`
  return `${path}?path=${encodeURIComponent(file.path)}${download ? '&download=1' : ''}`
}
