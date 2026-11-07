import { describe, expect, it, vi } from 'vitest'
import type { FileRef } from '../lib/types'
import { createFileService, fileService, type FileServiceAdapter } from './useFileService'
const sdk = vi.hoisted(() => ({ fileContent: vi.fn(), getFileReference: vi.fn(), listComputerDirectory: vi.fn(), referenceComputerFile: vi.fn() }))
vi.mock('@astral/sdk', () => sdk)
const file: FileRef = { id: 'file a', revision: 1, agent_id: 'bot', computer_id: 'source', path: '/work/source.md', display_name: 'source.md', media_type: 'text/plain', size: 1024, created_at: '', updated_at: '' }
function adapter(data: Blob, headers = new Headers()): FileServiceAdapter {
  return { resolveFile: vi.fn(), listDirectory: vi.fn(), referencePath: vi.fn(), content: vi.fn(async () => ({ data, headers })) }
}

describe('the shared file service boundary', () => {
  it('loads only a bounded preview through the authorized content route', async () => {
    sdk.fileContent.mockResolvedValueOnce({ data: new Blob(['hello']), response: { headers: new Headers({ 'Content-Type': 'text/plain' }) } })
    const preview = await fileService.preview(file, 'session b')
    expect(sdk.fileContent).toHaveBeenCalledWith(expect.objectContaining({ path: { id: 'file a' }, query: { session_id: 'session b' }, headers: { Range: 'bytes=0-262143' }, parseAs: 'blob' }))
    expect(preview.content).toBe('hello')
    expect(preview.url).toBe('/api/v1/files/file%20a/content?session_id=session%20b&download=false')
  })
  it('decodes the response filename and partial range while preserving valid empty text', async () => {
    const service = createFileService(adapter(new Blob(['']), new Headers({ 'Content-Type': 'text/markdown; charset=utf-8', 'Content-Disposition': "inline; filename*=UTF-8''%E6%8A%A5%E5%91%8A.md", 'Content-Range': 'bytes 0-262143/524288' })))
    expect(await service.preview(file, 'session')).toMatchObject({ name: '报告.md', mediaType: 'text/markdown', content: '', partial: true })
  })
  it('keeps binary and malformed UTF-8 files available for downloading without rendering them as text', async () => {
    for (const data of [new Blob(['a\u0000b']), new Blob([new Uint8Array([0xff])])]) {
      const preview = await createFileService(adapter(data)).preview(file, 'session')
      expect(preview.content).toBeUndefined()
      expect(preview.downloadURL).toContain('download=true')
    }
  })
  it.each([
    ['README', 'application/octet-stream', '文字 without an extension'],
    ['script.custom', 'application/octet-stream', 'run("unknown extension")'],
    ['config.json', 'application/json', '{"enabled":true}'],
    ['output.log', 'text/plain', 'first line\r\nsecond line\tvalue'],
    ['empty', 'application/octet-stream', ''],
  ])('previews UTF-8 text in %s without an extension or MIME whitelist', async (name, mediaType, content) => {
    const service = createFileService(adapter(new Blob([content]), new Headers({ 'Content-Type': mediaType })))
    expect((await service.preview({ ...file, path: `/work/${name}`, display_name: name }, 'session')).content).toBe(content)
  })
  it.each([
    ['UTF-8', [0xef, 0xbb, 0xbf, 0x48, 0x69, 0xe4, 0xb8, 0xad]],
    ['UTF-16LE', [0xff, 0xfe, 0x48, 0x00, 0x69, 0x00, 0x2d, 0x4e]],
    ['UTF-16BE', [0xfe, 0xff, 0x00, 0x48, 0x00, 0x69, 0x4e, 0x2d]],
  ])('recognizes a %s BOM before applying a conflicting server charset', async (_, bytes) => {
    const service = createFileService(adapter(new Blob([new Uint8Array(bytes)]), new Headers({ 'Content-Type': 'application/octet-stream; charset=windows-1252' })))
    expect((await service.preview(file, 'session')).content).toBe('Hi中')
  })
  it.each([
    ['text/plain; charset="UTF-16BE"', [0x4f, 0x60, 0x59, 0x7d], '你好'],
    ['text/plain; charset=utf-16le', [0x60, 0x4f, 0x7d, 0x59], '你好'],
    ['text/plain; charset=iso-8859-1', [0x63, 0x61, 0x66, 0xe9], 'café'],
    ['text/plain; Charset=GBK', [0xc4, 0xe3, 0xba, 0xc3], '你好'],
  ])('uses an explicitly declared charset in %s', async (contentType, bytes, content) => {
    const service = createFileService(adapter(new Blob([new Uint8Array(bytes)]), new Headers({ 'Content-Type': contentType })))
    expect((await service.preview(file, 'session')).content).toBe(content)
  })
  it('does not guess a legacy encoding or ignore an unsupported charset label', async () => {
    const bytes = new Blob([new Uint8Array([0xc4, 0xe3, 0xba, 0xc3])])
    for (const contentType of ['application/octet-stream', 'text/plain; charset=utf-7']) {
      const preview = await createFileService(adapter(bytes, new Headers({ 'Content-Type': contentType }))).preview(file, 'session')
      expect(preview.content).toBeUndefined()
      expect(preview.downloadURL).toContain('download=true')
    }
  })
  it('accepts a BOM-only empty text file but rejects decoded NULs and malformed complete UTF-16', async () => {
    const bomOnly = await createFileService(adapter(new Blob([new Uint8Array([0xff, 0xfe])]))).preview(file, 'session')
    expect(bomOnly.content).toBe('')
    for (const bytes of [[0xff, 0xfe, 0x41, 0x00, 0x00, 0x00], [0xfe, 0xff, 0x4f], [0xff, 0xfe, 0x00, 0xd8]]) {
      expect((await createFileService(adapter(new Blob([new Uint8Array(bytes)]))).preview(file, 'session')).content).toBeUndefined()
    }
  })
  it('does not decode media bytes and tolerates malformed filename headers', async () => {
    const data = new Blob(['not decoded'])
    const decode = vi.spyOn(data, 'arrayBuffer')
    const preview = await createFileService(adapter(data, new Headers({ 'Content-Type': 'image/png', 'Content-Disposition': "inline; filename*=UTF-8''%broken" }))).preview(file, 'session')
    expect(preview.name).toBe('source.md')
    expect(decode).not.toHaveBeenCalled()
  })
})

it('keeps the valid UTF-8 prefix when a bounded preview ends inside a character', async () => {
  const encoded = new TextEncoder().encode('你好')
  const service = createFileService(adapter(new Blob([encoded.subarray(0, 5)]), new Headers({ 'Content-Type': 'text/plain', 'Content-Range': 'bytes 0-4/6' })))
  expect(await service.preview(file, 'session')).toMatchObject({ content: '你', partial: true })
  expect((await createFileService(adapter(new Blob([encoded.subarray(0, 5)]))).preview(file, 'session')).content).toBeUndefined()
})

it.each([
  [[0xfe, 0xff, 0x4f, 0x60, 0x59], '你'],
  [[0xff, 0xfe, 0x3d, 0xd8, 0x00, 0xde, 0x3d, 0xd8], '😀'],
])('keeps the valid UTF-16 prefix at a bounded byte or surrogate boundary', async (bytes, content) => {
  const headers = new Headers({ 'Content-Type': 'application/octet-stream', 'Content-Range': `bytes 0-${bytes.length - 1}/${bytes.length + 2}` })
  expect(await createFileService(adapter(new Blob([new Uint8Array(bytes)]), headers)).preview(file, 'session')).toMatchObject({ content, partial: true })
})
