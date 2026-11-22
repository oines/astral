import { createPinia } from 'pinia'
import { computed, createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { expect, it, vi } from 'vitest'
import { computerFileURI, parseComputerFile, previewFileURL } from '../lib/computer-file'
import { markdownSource } from '../lib/markdown'
import MarkdownImage from './MarkdownImage.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('markstream-vue', async () => {
  const { h } = await import('vue')
  return { ImageNode: (props: { node: { src: string } }) => h('img', { src: props.node.src }) }
})

function image(src: string, assistantReply: boolean, loading = false) {
  const app = createSSRApp({ render: () => h(MarkdownImage, { node: { type: 'image', src, alt: 'Photo', title: null, raw: '', loading } }) })
  app.use(createPinia())
  app.provide(markdownSource, computed(() => ({ sessionID: 'sid', assistantReply })))
  return renderToString(app)
}

it('decodes Windows file paths exactly once and preserves case-sensitive Computer IDs', async () => {
  const file = { computer_id: 'PC_MiX', path: 'C:\\报告\\a+b %2F #.png' }
  const uri = computerFileURI(file)
  expect(parseComputerFile(uri)).toEqual(file)
  expect(parseComputerFile('astral-file://PC_MiX?path=a+b')).toEqual({ computer_id: 'PC_MiX', path: 'a b' })
  expect(await image(uri, true)).toContain(previewFileURL(file, 'sid'))
  expect(await image(uri, true, true)).not.toContain('<img')
  for (const invalid of [uri + '#other', uri + '&path=other', uri + '%zz', 'astral-file://PC_MiX?path=']) {
    expect(parseComputerFile(invalid)).toBeUndefined()
  }
})

it('only reads computer paths in assistant replies and still recognizes historical upload IDs', async () => {
  const file = { computer_id: 'PC_MiX', path: '/old/report.png' }
  for (const src of [computerFileURI(file), previewFileURL(file, 'sid')]) {
    expect(await image(src, false)).not.toContain('<img')
  }
  expect(await image('astral-file://PC_MiX/file_old', false)).not.toContain('<img')
  expect(await image('astral-file://PC_MiX/file_old', true)).toContain('/api/v1/files/file_old/content?session_id=sid')
  expect(await image('https://example.org/photo.png', false)).toContain('https://example.org/photo.png')
})
