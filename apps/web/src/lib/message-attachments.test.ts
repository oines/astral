import { describe, expect, it } from 'vitest'
import { messageAttachments } from './message-attachments'
import type { Content, Entry, FileRef } from './types'

function file(id: string, computer = 'computer', mediaType = 'image/png', name = `${id}.png`): FileRef {
  return {
    id, computer_id: computer, agent_id: 'agent', display_name: name,
    media_type: mediaType, path: `/uploads/${name}`, size: 256, revision: 1,
    created_at: '', updated_at: '', version: 'snapshot',
  }
}
function entry(content: Content[], files?: FileRef[]): Pick<Entry, 'message'> {
  return { message: { role: 'user', content, ...(files ? { metadata: { files } } : {}) } }
}

describe('message attachments', () => {
  it('keeps image endpoint indices in image order and removes only their matching original file rows', () => {
    const first = file('first')
    const second = file('second')
    const document = file('document', 'computer', 'text/plain', 'notes.txt')
    const unmatchedImage = file('unmatched')
    const originals = [document, second, unmatchedImage, first]
    const firstContent: Content = { type: 'image', url: 'astral-file://computer/first' }
    const secondContent: Content = { type: 'image', url: 'astral-file://computer/second' }
    const result = messageAttachments(entry([
      { type: 'text', text: 'Before the images' }, firstContent,
      { type: 'text', text: 'Between the images' }, secondContent,
    ], originals))

    expect(result.images).toEqual([
      { content: firstContent, index: 0, file: first },
      { content: secondContent, index: 1, file: second },
    ])
    expect(result.files).toEqual([document, unmatchedImage])
    expect(originals).toEqual([document, second, unmatchedImage, first])
    expect(result.images[0]?.file).toBe(first)
  })

  it.each([
    ['a different computer', 'astral-file://other-computer/picture'],
    ['an absent source URI', undefined],
    ['a remote URL', 'https://example.com/picture.png'],
    ['a data URL', 'data:image/png;base64,cHJldmlldw=='],
    ['a different file identity', 'astral-file://computer/other-picture'],
    ['a URI with additional query data', 'astral-file://computer/picture?other=1'],
  ])('retains the original file row for %s', (_name, url) => {
    const original = file('picture')
    const content: Content = { type: 'image', ...(url ? { url } : {}) }
    const result = messageAttachments(entry([content], [original]))
    expect(result.images).toEqual([{ content, index: 0, file: undefined }])
    expect(result.files).toEqual([original])
  })

  it('does not confuse identical names or a repeated file id on different computers', () => {
    const otherComputer = file('shared-id', 'other-computer', 'image/png', 'same-name.png')
    const sameName = file('different-id', 'computer', 'image/png', 'same-name.png')
    const matching = file('shared-id', 'computer', 'image/png', 'same-name.png')
    const result = messageAttachments(entry([
      { type: 'image', url: 'astral-file://computer/shared-id' },
    ], [otherComputer, sameName, matching]))

    expect(result.images[0]?.file).toBe(matching)
    expect(result.files).toEqual([otherComputer, sameName])
  })

  it('retains legacy image files without a computer identity', () => {
    const original = file('picture', '')
    const result = messageAttachments(entry([
      { type: 'image', url: 'astral-file:///picture' },
    ], [original]))
    expect(result.images[0]?.file).toBeUndefined()
    expect(result.files).toEqual([original])
  })

  it('supports image-only historical content without attachment metadata', () => {
    const image: Content = { type: 'image', media_type: 'image/png', metadata: { width: 300, height: 200 } }
    expect(messageAttachments(entry([image]))).toEqual({ images: [{ content: image, index: 0, file: undefined }], files: [] })
  })

  it('handles missing message and non-list attachment metadata without inventing attachments', () => {
    expect(messageAttachments({})).toEqual({ images: [], files: [] })
    for (const metadata of [undefined, null, 'legacy metadata', {}, { files: null }, { files: {} }]) {
      expect(messageAttachments({ message: { role: 'user', metadata } })).toEqual({ images: [], files: [] })
    }
  })
})
