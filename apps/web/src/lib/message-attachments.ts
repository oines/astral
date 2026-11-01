import type { Content, Entry, FileRef } from './types'
import { object } from './api'

export interface MessageImage {
  content: Content
  index: number
  file?: FileRef
}

export function messageAttachments(entry: Pick<Entry, 'message'>) {
  const raw = object(entry.message?.metadata).files
  const files = Array.isArray(raw) ? raw as FileRef[] : []
  const images: MessageImage[] = (entry.message?.content ?? []).filter(part => part.type === 'image').map((content, index) => ({
    content,
    index,
    // Frozen previews retain this original URI. Match both identities so an
    // unrelated file is never hidden just because its MIME type is an image.
    file: files.find(file => file.computer_id && content.url === `astral-file://${file.computer_id}/${file.id}`),
  }))
  const represented = new Set(images.flatMap(image => image.file ? [image.file] : []))
  return { images, files: files.filter(file => !represented.has(file)) }
}
