/* eslint-disable vue/one-component-per-file -- Shared controls are lightweight client-renderer fixtures. */
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { createRenderer, h, nextTick, shallowRef, ssrContextKey, type RenderFunction } from 'vue'
import { compileScript, compileTemplate, parse } from 'vue/compiler-sfc'
import type { FileRef } from '../../lib/types'
import type { FileService } from '../../composables/useFileService'
import FileExplorer from './FileExplorer.vue'

const fixture = vi.hoisted(() => ({ list: vi.fn<FileService['listDirectory']>(), reference: vi.fn<FileService['referencePath']>(), download: vi.fn(), opened: vi.fn(), transitions: [] as Array<{ readonly css: boolean }> }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('../../composables/useFileService', async importOriginal => ({ ...await importOriginal<object>(), fileService: { listDirectory: fixture.list, referencePath: fixture.reference } }))
vi.mock('../../lib/file-download', () => ({ downloadFileReference: fixture.download }))
vi.mock('../../composables/useFileReveal', async () => {
  const { ref } = await import('vue')
  return { useFileReveal: () => ({ available: ref(false), error: ref(''), labelKey: ref('reveal'), reveal: vi.fn() }) }
})
vi.mock('@astral/ui', async () => {
  const { defineComponent, h, ref } = await import('vue')
  return {
    useDelayedPending: () => ref(false),
    Button: defineComponent({ props: { busy: Boolean, disabled: Boolean }, setup: (props, { attrs, slots }) => () => h('button', { ...attrs, disabled: props.busy || props.disabled }, slots.default?.()) }),
    Menu: defineComponent({ props: { open: Boolean }, setup: (props, { slots }) => () => h('div', [...slots.trigger?.() || [], ...props.open ? slots.default?.() || [] : []]) }),
    MenuItem: defineComponent({ emits: ['select'], setup: (_props, { emit, slots }) => () => h('button', { 'data-menu-item': true, onClick: () => emit('select') }, slots.default?.()) }),
  }
})

interface HostNode { tag: string; text: string; props: Record<string, unknown>; children: HostNode[]; parent?: HostNode }
const node = (tag: string, text = ''): HostNode => ({ tag, text, props: {}, children: [] })
function remove(value: HostNode) { if (value.parent) value.parent.children.splice(value.parent.children.indexOf(value), 1); value.parent = undefined }
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
const file: FileRef = { id: 'reference', revision: 1, created_at: '', updated_at: '', agent_id: 'bot', computer_id: 'office', path: '/work/report.md', display_name: 'report.md', media_type: 'text/markdown', size: 1_000_000 }
const all = (root: HostNode): HostNode[] => [root, ...root.children.flatMap(all)]
const text = (root: HostNode): string => root.text + root.children.map(text).join('')
const RowTransitions = Vue.defineComponent({
  inheritAttrs: false,
  props: { css: Boolean, name: String },
  setup(props, { slots }) {
    fixture.transitions.push(props)
    return () => h(Vue.Fragment, null, slots.default?.())
  },
})
function clientRender(): RenderFunction {
  const filename = 'FileExplorer.vue'
  const { descriptor } = parse(readFileSync(new URL('./FileExplorer.vue', import.meta.url), 'utf8'))
  const script = compileScript(descriptor, { id: filename })
  const result = compileTemplate({ filename, id: filename, source: descriptor.template!.content, compilerOptions: { mode: 'function', bindingMetadata: script.bindings } })
  if (result.errors.length) throw new Error(result.errors.map(String).join('\n'))
  return new Function('Vue', result.code)({ ...Vue, TransitionGroup: RowTransitions }) as RenderFunction
}
let unmount: (() => void) | undefined
async function mount() {
  const source = shallowRef({ computer_id: 'office', path: '/work/report.md' })
  const root = node('root')
  const Child = Object.assign({}, FileExplorer, { render: clientRender() })
  const app = renderer.createApp({ setup: () => () => h(Child, { source: source.value, sessionID: 'source-session', onOpenFile: fixture.opened }) })
  app.provide(ssrContextKey, { modules: new Set() })
  app.mount(root)
  unmount = () => app.unmount()
  await vi.waitFor(() => expect(all(root).some(value => value.props.role === 'treeitem')).toBe(true))
  async function context(path: string) {
    const row = all(root).find(value => value.props['data-path'] === path)!
    ;(row.props.onContextmenu as (event: unknown) => void)({ preventDefault: vi.fn(), currentTarget: null })
    await nextTick()
  }
  function download() { return all(root).find(value => value.props['data-menu-item'] && text(value) === 'download') }
  return { root, source, context, download }
}
beforeEach(() => {
  vi.stubGlobal('HTMLElement', class {})
  fixture.list.mockReset().mockImplementation(async source => ({ computer_id: source.computer_id, path: '/work', parent: '/', entries: [{ name: 'report.md', path: '/work/report.md', is_dir: false, size: file.size, modified_at: '' }, { name: 'folder', path: '/work/folder', is_dir: true, size: 0, modified_at: '' }] }))
  fixture.reference.mockReset().mockResolvedValue(file)
  fixture.download.mockReset()
  fixture.opened.mockReset()
  fixture.transitions = []
})
afterEach(() => { unmount?.(); unmount = undefined; vi.unstubAllGlobals() })

describe('file explorer context-menu downloads', () => {
  it('obtains a source-bound reference and downloads without opening a preview tab', async () => {
    const view = await mount()
    await view.context('/work/report.md')
    const labels = all(view.root).filter(value => value.props['data-menu-item']).map(text)
    expect(labels).toEqual(['download', 'filesCopyPath'])
    ;(view.download()!.props.onClick as () => void)()
    await vi.waitFor(() => expect(fixture.download).toHaveBeenCalledExactlyOnceWith(file, 'source-session'))
    expect(fixture.reference).toHaveBeenCalledWith(expect.objectContaining({ computer_id: 'office' }), 'source-session', '/work/report.md', expect.any(AbortSignal))
    expect(fixture.opened).not.toHaveBeenCalled()
  })

  it('keeps Copy path but omits Download for a directory', async () => {
    const view = await mount()
    await view.context('/work/folder')
    expect(view.download()).toBeUndefined()
    expect(all(view.root).filter(value => value.props['data-menu-item']).map(text)).toEqual(['filesCopyPath'])
    expect(fixture.reference).not.toHaveBeenCalled()
  })

  it.each([
    { button: 2, ctrlKey: false, prevented: true },
    { button: 1, ctrlKey: false, prevented: true },
    { button: 0, ctrlKey: true, prevented: true },
    { button: 0, ctrlKey: false, prevented: false },
  ])('guards the opening gesture release before menu-item selection: %j', async ({ button, ctrlKey, prevented }) => {
    const view = await mount()
    await view.context('/work/report.md')
    const menu = all(view.root).find(value => String(value.props.class).includes('file-explorer-menu'))!
    const event = new Event('pointerup', { cancelable: true })
    Object.defineProperties(event, { button: { value: button }, ctrlKey: { value: ctrlKey } })
    ;(menu.props.onPointerupCapture as (event: Event) => void)(event)
    expect(event.defaultPrevented).toBe(prevented)
    expect(view.download()).toBeDefined()
    expect(fixture.reference).not.toHaveBeenCalled()
    expect(fixture.download).not.toHaveBeenCalled()
  })

  it('prevents the DropdownMenu focus timer from targeting its hidden positioning anchor', async () => {
    const view = await mount()
    await view.context('/work/report.md')
    const menu = all(view.root).find(value => String(value.props.class).includes('file-explorer-menu'))!
    const event = new Event('closeAutoFocus', { cancelable: true })
    ;(menu.props.onCloseAutoFocus as (event: Event) => void)(event)
    expect(event.defaultPrevented).toBe(true)
  })

  it.each(['source change', 'unmount'])('cancels a pending download reference on %s and ignores its late response', async action => {
    let finish!: (value: FileRef) => void
    fixture.reference.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
    const view = await mount()
    await view.context('/work/report.md')
    ;(view.download()!.props.onClick as () => void)()
    const signal = fixture.reference.mock.calls[0]![3]!
    if (action === 'source change') { view.source.value = { computer_id: 'other', path: '/other/file' }; await nextTick() }
    else { unmount?.(); unmount = undefined }
    expect(signal.aborted).toBe(true)
    finish(file)
    await Promise.resolve(); await nextTick()
    expect(fixture.download).not.toHaveBeenCalled()
    expect(fixture.opened).not.toHaveBeenCalled()
  })

  it('shows reference failures locally without downloading or changing the preview', async () => {
    fixture.reference.mockRejectedValueOnce(new Error('Computer is offline'))
    const view = await mount()
    await view.context('/work/report.md')
    ;(view.download()!.props.onClick as () => void)()
    await vi.waitFor(() => expect(text(view.root)).toContain('Computer is offline'))
    expect(fixture.download).not.toHaveBeenCalled()
    expect(fixture.opened).not.toHaveBeenCalled()
  })
})

describe('file explorer folder disclosure', () => {
  beforeEach(() => {
    fixture.list.mockImplementation(async (source, _session, path) => ({
      computer_id: source.computer_id,
      path: path === '/work/folder' ? '/work/folder' : '/work',
      parent: path === '/work/folder' ? '/work' : '/',
      entries: path === '/work/folder'
        ? [{ name: 'note.txt', path: '/work/folder/note.txt', is_dir: false, size: 5, modified_at: '' }]
        : [{ name: 'report.md', path: '/work/report.md', is_dir: false, size: file.size, modified_at: '' }, { name: 'folder', path: '/work/folder', is_dir: true, size: 0, modified_at: '' }],
    }))
  })

  it('keeps the same disclosure arrow and tree host when folder rows enter and leave', async () => {
    const view = await mount()
    const host = all(view.root).find(value => value.props.role === 'tree')!
    const folder = all(host).find(value => value.props['data-path'] === '/work/folder')!
    const chevron = all(folder).find(value => String(value.props.class).includes('file-explorer-chevron'))!
    expect(chevron).toBeDefined()
    expect(fixture.transitions[0]?.css).toBe(false)
    expect(folder.props['aria-expanded']).toBe(false)
    await (folder.props.onClick as () => Promise<void>)()
    await nextTick()
    expect(fixture.transitions[0]?.css).toBe(true)
    expect(folder.props['aria-expanded']).toBe(true)
    expect(all(folder).find(value => String(value.props.class).includes('file-explorer-chevron'))).toBe(chevron)
    const child = all(host).find(value => value.props['data-path'] === '/work/folder/note.txt')!
    expect(child).toBeDefined()
    expect(child.parent?.parent).toBe(host)
    await (folder.props.onClick as () => Promise<void>)()
    await nextTick()
    expect(folder.props['aria-expanded']).toBe(false)
    expect(all(folder).find(value => String(value.props.class).includes('file-explorer-chevron'))).toBe(chevron)
    expect(all(host).some(value => value.props['data-path'] === '/work/folder/note.txt')).toBe(false)
    expect(all(view.root).find(value => value.props.role === 'tree')).toBe(host)
    expect(fixture.opened).not.toHaveBeenCalled()
  })

  it('preserves keyed rows and disables disclosure motion for refresh and source navigation', async () => {
    const view = await mount()
    const folder = all(view.root).find(value => value.props['data-path'] === '/work/folder')!
    await (folder.props.onClick as () => Promise<void>)()
    await nextTick()
    const child = all(view.root).find(value => value.props['data-path'] === '/work/folder/note.txt')!
    const refresh = all(view.root).find(value => value.props['aria-label'] === 'filesRefresh')!
    await (refresh.props.onClick as () => Promise<void>)()
    await nextTick()
    expect(fixture.transitions[0]?.css).toBe(false)
    expect(all(view.root).find(value => value.props['data-path'] === '/work/folder/note.txt')).toBe(child)
    expect(all(view.root).find(value => value.props['data-path'] === '/work/folder')).toBe(folder)
    await (folder.props.onClick as () => Promise<void>)()
    await nextTick()
    expect(fixture.transitions[0]?.css).toBe(true)
    view.source.value = { computer_id: 'other', path: '/other/file' }
    await nextTick()
    expect(fixture.transitions[0]?.css).toBe(false)
    expect(fixture.opened).not.toHaveBeenCalled()
  })
})
