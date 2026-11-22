import { afterEach, expect, it, vi } from 'vitest'
import { downloadComputerFile } from './file-download'

afterEach(() => vi.unstubAllGlobals())

it('downloads through the full authenticated content URL without buffering file bytes', () => {
  const link = { href: '', download: '', click: vi.fn(), remove: vi.fn() }
  const append = vi.fn()
  const createElement = vi.fn(() => link)
  vi.stubGlobal('document', { createElement, body: { append } })
  downloadComputerFile({ computer_id: 'Office', path: '/work/report.md' }, 'source session', 'report.md')
  expect(createElement).toHaveBeenCalledExactlyOnceWith('a')
  expect(link.href).toBe('/api/v1/sessions/source%20session/computers/Office/files/content?path=%2Fwork%2Freport.md&download=1')
  expect(link.download).toBe('report.md')
  expect(append).toHaveBeenCalledWith(link)
  expect(link.click).toHaveBeenCalledOnce()
  expect(link.remove).toHaveBeenCalledOnce()
})

it('removes the temporary download link even when navigation fails', () => {
  const link = { href: '', download: '', click: vi.fn(() => { throw new Error('Navigation blocked') }), remove: vi.fn() }
  vi.stubGlobal('document', { createElement: () => link, body: { append: vi.fn() } })
  expect(() => downloadComputerFile({ computer_id: 'Office', path: '/work/report.md' }, 'session', 'report.md')).toThrow('Navigation blocked')
  expect(link.remove).toHaveBeenCalledOnce()
})
