import { describe, expect, it, vi } from 'vitest'
import { selectFilePreview } from './preview-registry'
import { codeLanguage } from './preview-language'
import type { FilePreviewResource } from '../../composables/useFileService'
vi.mock('./renderers/MarkdownFilePreview.vue', () => ({ default: {} }))
vi.mock('./renderers/ImageFilePreview.vue', () => ({ default: {} }))
vi.mock('./renderers/PdfFilePreview.vue', () => ({ default: {} }))
vi.mock('./renderers/MediaFilePreview.vue', () => ({ default: {} }))
vi.mock('./renderers/DownloadFilePreview.vue', () => ({ default: {} }))
const resource = (patch: Partial<FilePreviewResource>): FilePreviewResource => ({ name: 'file', mediaType: '', partial: false, url: '/file', downloadURL: '/file?download', ...patch })

describe('the file preview registry', () => {
  it.each([
    [{ name: 'REPORT.MD', content: '# Report', mediaType: 'text/plain' }, 'markdown'],
    [{ name: 'report', content: '# Report', mediaType: 'text/markdown' }, 'markdown'],
    [{ name: 'script.py', content: '' }, 'code'],
    [{ name: 'README', content: 'Plain text without an extension' }, 'code'],
    [{ name: 'program.custom', content: 'print("custom source")', mediaType: 'application/octet-stream' }, 'code'],
    [{ name: 'config.json', content: '{"enabled":true}', mediaType: 'application/json' }, 'code'],
    [{ name: 'output.log', content: 'first line\nsecond line', mediaType: 'text/plain' }, 'code'],
    [{ name: 'empty', content: '', mediaType: 'application/octet-stream' }, 'code'],
    [{ name: 'image.md', mediaType: 'image/png', content: 'ignored' }, 'image'],
    [{ mediaType: 'application/pdf' }, 'pdf'],
    [{ mediaType: 'audio/mpeg' }, 'audio'],
    [{ mediaType: 'video/mp4' }, 'video'],
    [{ name: 'binary.md' }, 'download'],
  ] as const)('selects the supported renderer for %j', (input, expected) => {
    expect(selectFilePreview(resource(input)).id).toBe(expected)
  })
  it('allows an independent renderer to be registered ahead of the fallback', () => {
    const component = {}
    expect(selectFilePreview(resource({ mediaType: 'text/plain' }), [{ id: 'code', labelKey: 'filesText', component, matches: file => file.mediaType === 'text/plain' }]).component).toBe(component)
  })
  it('uses only supported code grammars and leaves other text as plaintext', () => {
    expect(codeLanguage('SCRIPT.PY')).toBe('python')
    expect(codeLanguage('module.tsx')).toBe('javascript')
    expect(codeLanguage('build.sh')).toBe('shell')
    expect(codeLanguage('file.unknown')).toBe('plaintext')
  })
})
