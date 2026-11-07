/* eslint-disable vue/one-component-per-file -- Cached siblings and the preview resource are isolated renderer fixtures. */
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { computed, createRenderer, defineComponent, h, KeepAlive, nextTick, onScopeDispose, provide, ref, ssrContextKey, type PropType, type RenderFunction } from 'vue'
import { compileScript, compileTemplate, parse } from 'vue/compiler-sfc'
import type { FileRef } from '../../lib/types'
import type { SurfaceTab } from '../../stores/surface'
import SurfaceFile from './SurfaceFile.vue'
import { workspaceTabKeys } from './workspace-context'

const fixture = vi.hoisted(() => ({
  resolveFile: vi.fn<(id: string, sessionID: string, signal?: AbortSignal) => Promise<FileRef>>(),
  resources: [] as Array<{ fileID: string; released: boolean }>,
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('../../composables/useFileService', () => ({ resolveFile: fixture.resolveFile }))
vi.mock('@astral/ui', async () => {
  const { computed, defineComponent, h } = await import('vue')
  return {
    useDelayedPending: (value: () => boolean) => computed(value),
    Button: defineComponent({ setup: (_props, { slots }) => () => h('button', slots.default?.()) }),
  }
})
vi.mock('./FilePreview.vue', async () => {
  const { defineComponent, h, onScopeDispose } = await import('vue')
  return { default: defineComponent({
    props: { file: { type: Object as PropType<FileRef>, required: true }, sessionID: { type: String, required: true }, computerName: { type: String, required: true } },
    setup(props) {
      const resource = { fileID: props.file.id, released: false }
      fixture.resources.push(resource)
      onScopeDispose(() => { resource.released = true })
      return () => h('article', { 'data-preview-file': props.file.id }, 'preview resource')
    },
  }) }
})

interface HostNode { type: string; text: string; children: HostNode[]; parent: HostNode | null }
function node(type: string, text = ''): HostNode { return { type, text, children: [], parent: null } }
function insert(child: HostNode, parent: HostNode, anchor: HostNode | null = null) {
  if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1)
  child.parent = parent
  const index = anchor ? parent.children.indexOf(anchor) : -1
  parent.children.splice(index < 0 ? parent.children.length : index, 0, child)
}
const renderer = createRenderer<HostNode, HostNode>({
  createElement: type => node(type), createText: text => node('#text', text), createComment: text => node('#comment', text),
  setText: (target, text) => { target.text = text },
  setElementText: (target, text) => { target.text = text; target.children = [] },
  patchProp: () => {}, parentNode: target => target.parent,
  nextSibling: target => target.parent?.children[target.parent.children.indexOf(target) + 1] ?? null,
  insert,
  remove: target => {
    if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1)
    target.parent = null
  },
  insertStaticContent: (html, parent, anchor) => {
    const target = node('#static', html)
    insert(target, parent, anchor)
    return [target, target]
  },
})

function clientRender(): RenderFunction {
  const filename = 'SurfaceFile.vue'
  const { descriptor } = parse(readFileSync(new URL('./SurfaceFile.vue', import.meta.url), 'utf8'))
  const script = compileScript(descriptor, { id: filename })
  const template = compileTemplate({ filename, id: filename, source: descriptor.template!.content, compilerOptions: { mode: 'function', bindingMetadata: script.bindings } })
  if (template.errors.length) throw new Error(template.errors.map(String).join('\n'))
  return new Function('Vue', template.code)(Vue) as RenderFunction
}

const file: FileRef = { id: 'file', revision: 1, created_at: '', updated_at: '', agent_id: 'bot', computer_id: 'office', path: '/reports/result.pdf', display_name: 'result.pdf', media_type: 'application/pdf', size: 1_000_000 }
const tabKey = 'file:session:file'
let unmount: (() => void) | undefined
function mount(provided = false) {
  const active = ref(true)
  const keys = ref(new Set([tabKey]))
  const resolved = vi.fn()
  const retainedTabDisposed = vi.fn()
  const tab: Extract<SurfaceTab, { kind: 'file' }> = { key: tabKey, kind: 'file', id: file.id, sessionID: 'session', ...(provided ? { ref: file } : {}) }
  const Child = Object.assign({}, SurfaceFile, { render: clientRender() })
  const CachedFile = defineComponent({
    setup() {
      onScopeDispose(retainedTabDisposed)
      return () => h(Child, { tab, computerName: 'Office computer', onResolved: resolved })
    },
  })
  const Other = defineComponent({ setup: () => () => h('section', 'another tab') })
  const app = renderer.createApp({
    setup() {
      provide(workspaceTabKeys, computed(() => keys.value))
      return () => h(KeepAlive, null, { default: () => active.value ? h(CachedFile, { key: tabKey }) : h(Other, { key: 'other' }) })
    },
  })
  app.provide(ssrContextKey, { modules: new Set() })
  app.mount(node('root'))
  unmount = () => app.unmount()
  return { active, keys, resolved, retainedTabDisposed }
}

beforeEach(() => { fixture.resolveFile.mockReset(); fixture.resources = [] })
afterEach(() => { unmount?.(); unmount = undefined })

describe('closed file tab resources inside KeepAlive', () => {
  it('unmounts the preview and releases its resources when the cached tab key is removed', async () => {
    const surface = mount(true)
    await nextTick()
    expect(fixture.resources).toEqual([{ fileID: file.id, released: false }])

    surface.active.value = false
    await nextTick()
    expect(fixture.resources[0]?.released).toBe(false)
    expect(surface.retainedTabDisposed).not.toHaveBeenCalled()

    surface.keys.value = new Set()
    await nextTick()
    expect(fixture.resources[0]?.released).toBe(true)
    // The cached lightweight shell still exists; its nested preview was
    // actually disposed in response to the injected key set changing.
    expect(surface.retainedTabDisposed).not.toHaveBeenCalled()
    expect(fixture.resolveFile).not.toHaveBeenCalled()
    surface.active.value = true
    await nextTick()
    expect(fixture.resources).toHaveLength(1)
  })

  it('aborts pending metadata for a removed cached tab and ignores a late successful response', async () => {
    let finish!: (value: FileRef) => void
    const pending = new Promise<FileRef>(resolve => { finish = resolve })
    fixture.resolveFile.mockReturnValue(pending)
    const surface = mount()
    await nextTick()
    expect(fixture.resolveFile).toHaveBeenCalledOnce()
    const signal = fixture.resolveFile.mock.calls[0]![2]!
    expect(signal.aborted).toBe(false)

    surface.active.value = false
    await nextTick()
    expect(signal.aborted).toBe(false)
    surface.keys.value = new Set()
    await nextTick()
    expect(signal.aborted).toBe(true)
    expect(surface.retainedTabDisposed).not.toHaveBeenCalled()

    finish(file)
    await pending
    await nextTick()
    surface.active.value = true
    await nextTick()
    expect(surface.resolved).not.toHaveBeenCalled()
    expect(fixture.resources).toEqual([])
    expect(fixture.resolveFile).toHaveBeenCalledOnce()
  })
})
