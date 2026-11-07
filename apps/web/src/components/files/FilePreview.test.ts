/* eslint-disable vue/one-component-per-file -- Preview renderers and shared buttons are isolated test stubs. */
import { readFileSync } from 'node:fs'
import * as Vue from 'vue'
import { compileScript, compileTemplate, parse } from 'vue/compiler-sfc'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, defineAsyncComponent, defineComponent, h, nextTick, ref, shallowRef, ssrContextKey, type RenderFunction, type Component, type Ref, type ShallowRef } from 'vue'
import type { FileRef } from '../../lib/types'
import type { FilePreviewResource } from '../../composables/useFileService'
import FilePreview from './FilePreview.vue'

const fixture = vi.hoisted(() => ({ renderer: undefined as unknown as Component, refreshRemount: false, createComponent: undefined as undefined | (() => Component), preview: undefined as unknown as ShallowRef<FilePreviewResource | undefined>, busy: undefined as unknown as Ref<boolean>, reload: vi.fn(), writeClipboard: vi.fn<(text: string) => Promise<boolean>>() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('./renderers/DownloadFilePreview.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return { default: defineComponent({ props: { message: { type: String, default: '' } }, setup: props => () => h('p', props.message || 'previewUnavailable') }) }
})
vi.mock('./preview-registry', () => ({ selectFilePreview: () => ({ id: 'custom-renderer', labelKey: 'customRendererLabel', component: fixture.renderer, createComponent: fixture.createComponent, refreshRemount: fixture.refreshRemount }) }))
vi.mock('../../composables/useFilePreview', () => ({ useFilePreview: () => ({ preview: fixture.preview, busy: fixture.busy, error: ref(''), reload: fixture.reload }) }))
vi.mock('../../composables/useFileReveal', () => ({ useFileReveal: () => ({ available: ref(false), busy: ref(false), error: ref(''), labelKey: ref('reveal'), reveal: vi.fn() }) }))
vi.mock('../../lib/browser', async importOriginal => ({ ...await importOriginal<object>(), copyToClipboard: fixture.writeClipboard }))
vi.mock('@astral/ui', async () => {
  const { defineComponent, h, ref } = await import('vue')
  return {
    useDelayedPending: () => ref(false),
    Button: defineComponent({ props: { busy: Boolean, disabled: Boolean }, setup: (props, { attrs, slots }) => () => h('button', { ...attrs, disabled: props.busy || props.disabled }, slots.default?.()) }),
  }
})
interface HostNode { tag: string; text: string; props: Record<string, unknown>; children: HostNode[]; parent?: HostNode }
const node = (tag: string, text = ''): HostNode => ({ tag, text, props: {}, children: [] })
function remove(value: HostNode) {
  const siblings = value.parent?.children
  if (siblings) siblings.splice(siblings.indexOf(value), 1)
  value.parent = undefined
}
function insert(value: HostNode, parent: HostNode, anchor?: HostNode | null) {
  remove(value)
  const index = anchor ? parent.children.indexOf(anchor) : -1
  parent.children.splice(index < 0 ? parent.children.length : index, 0, value)
  value.parent = parent
}
const renderer = createRenderer<HostNode, HostNode>({
  createElement: tag => node(tag), createText: text => node('#text', text), createComment: text => node('#comment', text),
  insert, remove, setText: (value, text) => { value.text = text }, setElementText: (value, text) => { value.text = text; value.children = [] },
  parentNode: value => value.parent || null, nextSibling: value => value.parent?.children[value.parent.children.indexOf(value) + 1] || null,
  patchProp: (value, key, _previous, next) => { value.props[key] = next },
  insertStaticContent: (content, parent, anchor) => { const value = node('#static', content); insert(value, parent, anchor); return [value, value] },
})
const file: FileRef = { id: 'file', revision: 1, agent_id: 'bot', computer_id: 'computer', path: '/work/a.md', display_name: 'a.md', media_type: 'text/plain', size: 5, created_at: '', updated_at: '' }
const resource = (): FilePreviewResource => ({ name: 'a.md', mediaType: 'text/plain', content: 'preview content', partial: false, url: '/file', downloadURL: '/file?download=true' })
const all = (value: HostNode): HostNode[] => [value, ...value.children.flatMap(all)]
const text = (value: HostNode): string => value.text + value.children.map(text).join('')
const unmount: (() => void)[] = []
function clientRender(): RenderFunction {
  const filename = 'FilePreview.vue'
  const { descriptor } = parse(readFileSync(new URL('./FilePreview.vue', import.meta.url), 'utf8'))
  const script = compileScript(descriptor, { id: filename })
  const template = compileTemplate({ filename, id: filename, source: descriptor.template!.content, compilerOptions: { mode: 'function', bindingMetadata: script.bindings } })
  if (template.errors.length) throw new Error(template.errors.map(String).join('\n'))
  return new Function('Vue', template.code)(Vue) as RenderFunction
}
function mount() {
  const root = node('root')
  const source = shallowRef(file)
  const Child = Object.assign({}, FilePreview, { render: clientRender() })
  const app = renderer.createApp({ setup: () => () => h(Child, { file: source.value, sessionID: 'session', computerName: 'Work computer' }) })
  app.provide(ssrContextKey, { modules: new Set() })
  const unhandled = vi.fn()
  app.config.errorHandler = unhandled
  app.mount(root)
  unmount.push(() => app.unmount())
  return { root, unhandled, source }
}
beforeEach(() => {
  fixture.createComponent = undefined
  fixture.refreshRemount = false
  fixture.renderer = defineComponent({ inheritAttrs: false, setup: () => () => h('div', { class: 'healthy-renderer' }, 'preview ready') })
  fixture.preview = shallowRef(resource())
  fixture.busy = ref(false)
  fixture.writeClipboard.mockReset().mockResolvedValue(true)
  fixture.reload.mockReset().mockImplementation(async () => { fixture.busy.value = true; await Promise.resolve(); fixture.preview.value = resource(); fixture.busy.value = false; return fixture.preview.value })
})
afterEach(() => { for (const stop of unmount.splice(0)) stop() })

describe('file preview renderer isolation', () => {
  it('contains a rendering error and keeps download/retry available without failing the workspace', async () => {
    fixture.renderer = defineComponent({ setup: () => () => { throw new Error('Renderer parse failure') } })
    const { root, unhandled } = mount()
    await vi.waitFor(() => expect(text(root)).toContain('filesPreviewFailed'))
    expect(unhandled).not.toHaveBeenCalled()
    expect(all(root).some(value => value.tag === 'a' && String(value.props.href).includes('download=true'))).toBe(true)
    const retry = all(root).find(value => value.tag === 'button' && text(value) === 'retry')
    expect(retry).toBeDefined()
    fixture.renderer = defineComponent({ inheritAttrs: false, setup: () => () => h('div', 'preview recovered') })
    await (retry!.props.onClick as () => Promise<void>)()
    await nextTick()
    expect(fixture.reload).toHaveBeenCalledOnce()
    expect(text(root)).toContain('preview recovered')
    expect(text(root)).not.toContain('filesPreviewFailed')
  })
  it('contains an asynchronous renderer loading failure', async () => {
    fixture.renderer = defineAsyncComponent(async () => { throw new Error('Unable to load renderer') })
    const { root, unhandled } = mount()
    await vi.waitFor(() => expect(text(root)).toContain('filesPreviewFailed'))
    expect(unhandled).not.toHaveBeenCalled()
    expect(text(root)).toContain('filesPreviewDownload')
  })
  it('creates a fresh asynchronous renderer when retrying a failed loader', async () => {
    const ready = defineComponent({ inheritAttrs: false, setup: () => () => h('div', 'loaded after retry') })
    const load = vi.fn().mockRejectedValueOnce(new Error('Temporary chunk failure')).mockResolvedValueOnce(ready)
    fixture.createComponent = () => defineAsyncComponent(load)
    const { root, unhandled } = mount()
    await vi.waitFor(() => expect(text(root)).toContain('filesPreviewFailed'))
    const retry = all(root).find(value => value.tag === 'button' && text(value) === 'retry')
    await (retry!.props.onClick as () => Promise<void>)()
    await vi.waitFor(() => expect(text(root)).toContain('loaded after retry'))
    expect(load).toHaveBeenCalledTimes(2)
    expect(unhandled).not.toHaveBeenCalled()
  })
  it.each([false, true])('remounts on refresh only when the registry declares it: %s', async refreshRemount => {
    const mounted = vi.fn()
    fixture.refreshRemount = refreshRemount
    fixture.renderer = defineComponent({ inheritAttrs: false, setup: () => { mounted(); return () => h('div', 'existing preview') } })
    const { root } = mount()
    await nextTick()
    const refresh = all(root).find(value => value.tag === 'button' && value.props['aria-label'] === 'filesRefreshPreview')
    await (refresh!.props.onClick as () => Promise<void>)()
    await nextTick()
    expect(mounted).toHaveBeenCalledTimes(refreshRemount ? 2 : 1)
  })
  it('refreshes live file content through its own toolbar action', async () => {
    const { root } = mount()
    const refresh = all(root).find(value => value.tag === 'button' && value.props['aria-label'] === 'filesRefreshPreview')
    expect(refresh).toBeDefined()
    await (refresh!.props.onClick as () => Promise<void>)()
    expect(fixture.reload).toHaveBeenCalledOnce()
  })
})

function copyButton(root: HostNode) {
  return all(root).find(value => value.tag === 'button' && ['filesCopyContent', 'filesCopyPreview'].includes(String(value.props['aria-label'])))
}
function hasCopiedIcon(root: HostNode) {
  const button = copyButton(root)
  return !!button && all(button).some(value => String(value.props.class).includes('lucide-check'))
}

describe('copying file preview content', () => {
  it.each(['text/plain', 'text/markdown', 'application/typescript'])('copies the original loaded text for %s rather than rendered markup', async mediaType => {
    const content = '# Original heading\n\n<b>literal text</b>\n```ts\nconst answer = 42\n```\n'
    fixture.preview.value = { ...resource(), mediaType, content }
    const { root } = mount()
    const button = copyButton(root)
    expect(button?.props['aria-label']).toBe('filesCopyContent')
    expect(button?.props.title).toBe('filesCopyContent')
    await (button!.props.onClick as () => Promise<void>)()
    await nextTick()
    expect(fixture.writeClipboard).toHaveBeenCalledExactlyOnceWith(content)
    expect(hasCopiedIcon(root)).toBe(true)
  })

  it('labels a partial preview explicitly and copies only its loaded portion', async () => {
    fixture.preview.value = { ...resource(), content: 'only the loaded beginning', partial: true }
    const { root } = mount()
    const button = copyButton(root)
    expect(button?.props['aria-label']).toBe('filesCopyPreview')
    expect(button?.props.title).toBe('filesCopyPreview')
    await (button!.props.onClick as () => Promise<void>)()
    expect(fixture.writeClipboard).toHaveBeenCalledExactlyOnceWith('only the loaded beginning')
  })

  it.each(['image/png', 'application/pdf', 'application/octet-stream'])('omits copy for %s when there is no text content', mediaType => {
    fixture.preview.value = { ...resource(), mediaType, content: undefined }
    const { root } = mount()
    expect(copyButton(root)).toBeUndefined()
    expect(fixture.writeClipboard).not.toHaveBeenCalled()
  })

  it('copies an empty text file without treating it as a binary preview', async () => {
    fixture.preview.value = { ...resource(), content: '' }
    const { root } = mount()
    await (copyButton(root)!.props.onClick as () => Promise<void>)()
    expect(fixture.writeClipboard).toHaveBeenCalledExactlyOnceWith('')
  })

  it('clears the copied feedback on refresh and when another file is displayed', async () => {
    const { root, source } = mount()
    await (copyButton(root)!.props.onClick as () => Promise<void>)()
    await nextTick()
    expect(hasCopiedIcon(root)).toBe(true)
    const refresh = all(root).find(value => value.tag === 'button' && value.props['aria-label'] === 'filesRefreshPreview')!
    await (refresh.props.onClick as () => Promise<void>)()
    await nextTick()
    expect(hasCopiedIcon(root)).toBe(false)
    await (copyButton(root)!.props.onClick as () => Promise<void>)()
    await nextTick()
    expect(hasCopiedIcon(root)).toBe(true)
    source.value = { ...file, id: 'another-file', path: '/work/another.md' }
    await nextTick()
    expect(hasCopiedIcon(root)).toBe(false)
  })

  it('ignores a late clipboard completion for the previous file', async () => {
    let finish!: (success: boolean) => void
    fixture.writeClipboard.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
    const { root, source } = mount()
    const copying = (copyButton(root)!.props.onClick as () => Promise<void>)()
    source.value = { ...file, id: 'another-file', path: '/work/another.md' }
    await nextTick()
    finish(true)
    await copying
    await nextTick()
    expect(hasCopiedIcon(root)).toBe(false)
  })

  it('shows clipboard errors locally and clears them after a successful retry', async () => {
    fixture.writeClipboard.mockRejectedValueOnce(new Error('Clipboard unavailable'))
    const { root, unhandled } = mount()
    await (copyButton(root)!.props.onClick as () => Promise<void>)()
    await nextTick()
    expect(text(root)).toContain('Clipboard unavailable')
    expect(hasCopiedIcon(root)).toBe(false)
    expect(unhandled).not.toHaveBeenCalled()
    await (copyButton(root)!.props.onClick as () => Promise<void>)()
    await nextTick()
    expect(text(root)).not.toContain('Clipboard unavailable')
    expect(hasCopiedIcon(root)).toBe(true)
  })
})
