import { afterEach, expect, it, vi } from 'vitest'
import { downloadFileReference } from './file-download'

afterEach(() => vi.unstubAllGlobals())

it('downloads through the full authenticated content URL without buffering file bytes', () => {
  const link = { href: '', download: '', click: vi.fn(), remove: vi.fn() }
  const append = vi.fn()
  const createElement = vi.fn(() => link)
  vi.stubGlobal('document', { createElement, body: { append } })
  downloadFileReference({ id: 'file/id', display_name: 'report.md' }, 'source session')
  expect(createElement).toHaveBeenCalledExactlyOnceWith('a')
  expect(link.href).toBe('/api/v1/files/file%2Fid/content?session_id=source%20session&download=true')
  expect(link.download).toBe('report.md')
  expect(append).toHaveBeenCalledWith(link)
  expect(link.click).toHaveBeenCalledOnce()
  expect(link.remove).toHaveBeenCalledOnce()
})

it('removes the temporary download link even when navigation fails', () => {
  const link = { href: '', download: '', click: vi.fn(() => { throw new Error('Navigation blocked') }), remove: vi.fn() }
  vi.stubGlobal('document', { createElement: () => link, body: { append: vi.fn() } })
  expect(() => downloadFileReference({ id: 'file', display_name: 'report.md' }, 'session')).toThrow('Navigation blocked')
  expect(link.remove).toHaveBeenCalledOnce()
})
