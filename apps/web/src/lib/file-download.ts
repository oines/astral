import { previewFileURL, type ComputerFile } from './computer-file'

// Navigate directly to the authorized full-content response. The browser owns
// the transfer; no preview range or whole-file buffer is involved.
export function downloadComputerFile(file: ComputerFile, sessionID: string, name: string) {
  const link = document.createElement('a')
  link.href = previewFileURL(file, sessionID, true)
  link.download = name
  document.body.append(link)
  try { link.click() }
  finally { link.remove() }
}
