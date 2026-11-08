/* eslint-disable vue/one-component-per-file -- Minimal host and sibling components exercise real KeepAlive hooks. */
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { createRenderer, defineComponent, h, KeepAlive, nextTick, ref, ssrContextKey, type Ref, type RenderFunction } from 'vue'
import { compileScript, compileTemplate, parse } from 'vue/compiler-sfc'
import SubagentSurface from './SubagentSurface.vue'

const fixture = vi.hoisted(() => ({ observations: [] as Array<{ id: Readonly<Ref<string>>; changes: string[] }> }))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('../composables/useSession', async () => {
  const { ref, watch } = await import('vue')
  const { emptyState } = await import('../lib/session-state')
  return {
    useSession: (id: Readonly<Ref<string>>) => {
      const observation = { id, changes: [] as string[] }
      fixture.observations.push(observation)
      watch(id, value => observation.changes.push(value), { immediate: true, flush: 'sync' })
      return { state: ref(emptyState()), error: ref(''), loadingHistory: ref(false), older: async () => false, reconnect: vi.fn() }
    },
  }
})
vi.mock('@astral/ui', async () => {
  const { computed, defineComponent, h } = await import('vue')
  return {
    useDelayedPending: (value: () => boolean) => computed(value),
    Button: defineComponent({ setup: (_props, { slots }) => () => h('button', slots.default?.()) }),
  }
})
vi.mock('./Transcript.vue', () => ({ default: { name: 'Transcript', render: () => null } }))

interface HostNode {
  type: string
  text: string
  props: Record<string, unknown>
  children: HostNode[]
  parent: HostNode | null
}
function node(type: string, text = ''): HostNode { return { type, text, props: {}, children: [], parent: null } }
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
  patchProp: (target, key, _previous, value) => { target.props[key] = value },
  parentNode: target => target.parent,
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
  const filename = 'SubagentSurface.vue'
  const { descriptor } = parse(readFileSync(new URL('./SubagentSurface.vue', import.meta.url), 'utf8'))
  const script = compileScript(descriptor, { id: filename })
  const template = compileTemplate({ filename, id: filename, source: descriptor.template!.content, compilerOptions: { mode: 'function', bindingMetadata: script.bindings } })
  if (template.errors.length) throw new Error(template.errors.map(String).join('\n'))
  return new Function('Vue', template.code)(Vue) as RenderFunction
}

let unmount: (() => void) | undefined
beforeEach(() => { fixture.observations = [] })
afterEach(() => { unmount?.(); unmount = undefined })

describe('subagent observation in the cached workspace', () => {
  it('pauses the observed Session for inactive tabs, inactive workspaces and a closed panel, then resumes the cached child', async () => {
    const tab = ref('child')
    const workspace = ref('first-root')
    const viewing = ref(true)
    const Child = Object.assign({}, SubagentSurface, { render: clientRender() })
    const Other = defineComponent({ setup: () => () => h('section', 'another tab') })
    const Workspace = defineComponent({
      setup: () => () => h(KeepAlive, null, { default: () => tab.value === 'child'
        ? h(Child, { key: 'child', sessionID: 'child', viewing: viewing.value })
        : h(Other, { key: 'other-tab' }) }),
    })
    const app = renderer.createApp({
      setup: () => () => h(KeepAlive, null, { default: () => workspace.value === 'first-root'
        ? h(Workspace, { key: 'first-root' })
        : h(Other, { key: 'second-root' }) }),
    })
    app.provide(ssrContextKey, { modules: new Set() })
    app.mount(node('root'))
    unmount = () => app.unmount()
    await nextTick()
    expect(fixture.observations).toHaveLength(1)
    const observed = fixture.observations[0]!
    expect(observed.id.value).toBe('child')

    tab.value = 'other'
    await nextTick()
    expect(observed.id.value).toBe('')
    tab.value = 'child'
    await nextTick()
    expect(observed.id.value).toBe('child')

    workspace.value = 'second-root'
    await nextTick()
    expect(observed.id.value).toBe('')
    workspace.value = 'first-root'
    await nextTick()
    expect(observed.id.value).toBe('child')

    viewing.value = false
    await nextTick()
    expect(observed.id.value).toBe('')
    // Reactivating the cached tab while the whole panel remains closed must
    // not reconnect its hidden observer.
    tab.value = 'other'
    await nextTick()
    tab.value = 'child'
    await nextTick()
    expect(observed.id.value).toBe('')
    viewing.value = true
    await nextTick()
    expect(observed.id.value).toBe('child')

    expect(fixture.observations).toHaveLength(1)
    expect(observed.changes).toEqual(['child', '', 'child', '', 'child', '', 'child'])
  })
})
