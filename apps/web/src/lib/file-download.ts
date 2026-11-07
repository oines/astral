import type { FileRef } from './types'
import { fileURL } from './api'

// Navigate directly to the authorized full-content response. The browser owns
// the transfer; no preview range or whole-file buffer is involved.
export function downloadFileReference(file: Pick<FileRef, 'id' | 'display_name'>, sessionID: string) {
  const link = document.createElement('a')
  link.href = fileURL(file.id, sessionID, true)
  link.download = file.display_name
  document.body.append(link)
  try { link.click() }
  finally { link.remove() }
}
