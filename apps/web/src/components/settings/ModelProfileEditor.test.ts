/* eslint-disable vue/one-component-per-file -- UI primitives are isolated test stubs. */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, createSSRApp, defineComponent, getCurrentInstance, h, nextTick, ssrContextKey, type PropType } from 'vue'
import { describeModel } from '@astral/sdk'
import { Modal } from '@astral/ui'
import { renderToString } from 'vue/server-renderer'
import type { Profile, Provider } from '../../lib/types'
import type { AstralCoreModelsIntegrationDiscovery as Discovery } from '@astral/sdk'
import ModelProfileEditor from './ModelProfileEditor.vue'

const fixture = vi.hoisted(() => ({
  preview: undefined as Profile | undefined,
  chooseDefaultReasoning: undefined as undefined | ((value: string) => void),
  draftProfile: undefined as undefined | (() => Profile),
  chooseModel: undefined as undefined | ((value: string) => void),
  selectProtocol: undefined as undefined | ((value: string) => void),
  save: undefined as undefined | (() => Promise<void>),
  saveModel: vi.fn(async ({ body }: { body: Profile }) => ({ data: body })),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@astral/sdk', async importOriginal => ({ ...await importOriginal<object>(), describeModel: vi.fn(async () => ({ data: fixture.preview })), saveModel: fixture.saveModel }))
vi.mock('./ReasoningFormatSelect.vue', () => ({ default: { render: () => null } }))
vi.mock('@astral/ui', () => ({
  Modal: defineComponent({ setup(_props, { slots }) {
    const state = (getCurrentInstance()!.parent! as unknown as { setupState: {
      preview: Profile | undefined; chooseDefaultReasoning: (value: string) => void; draftProfile: () => Profile
      chooseModel: (value: string) => void; selectProtocol: (value: string) => void; save: () => Promise<void>
    } }).setupState
    state.preview = fixture.preview
    fixture.chooseDefaultReasoning = state.chooseDefaultReasoning
    fixture.draftProfile = state.draftProfile
    fixture.chooseModel = state.chooseModel
    fixture.selectProtocol = state.selectProtocol
    fixture.save = state.save
    return () => h('div', slots.default?.())
  } }),
  Button: defineComponent({ setup(_props, { slots }) { return () => h('button', slots.default?.()) } }),
  Select: defineComponent({ props: {
    modelValue: { type: String, default: '' }, label: { type: String, default: '' }, options: { type: Array as PropType<{ value: string; label: string }[]>, default: () => [] },
  }, setup(props) {
    return () => h('select', { 'aria-label': props.label, value: props.modelValue },
      props.options.map((option: { value: string; label: string }) => h('option', { value: option.value }, option.label)))
  } }),
}))
const provider = { id: 'provider', preset_id: 'commandcode', protocol: 'openai-responses' } as Provider
const profile = {
  id: 'model', provider_id: provider.id, model_id: 'vendor/model', name: 'Model',
  created_at: '', updated_at: '', revision: 1,
  recommendations: { context_window: 128000, max_output_tokens: 8192 }, overrides: {},
} as Profile
function described(error = ''): Profile {
  return { ...profile, reasoning_error: error, reasoning_controls: {
    format: 'openai-responses',
    options: [{ id: 'interface-default', value: {} }, { id: 'effort:high', value: { effort: 'high' } }],
    diagnostics: [{ code: 'unavailable-option', path: 'reasoning.options.off', message: 'reasoning.enabled: this route has no declared reasoning-off value' }],
  } }
}
beforeEach(() => { vi.useFakeTimers(); fixture.preview = described(); vi.clearAllMocks() })
afterEach(() => { vi.clearAllTimers(); vi.useRealTimers() })
async function render(connection = provider, model = profile, catalog: Discovery[] = []) {
  return renderToString(createSSRApp(ModelProfileEditor, { open: true, provider: connection, profile: model, catalog }))
}
type TestNode = { parent?: TestNode; children: TestNode[]; text?: string }
function mountEditor() {
  const renderer = createRenderer<TestNode, TestNode>({
    createElement: () => ({ children: [] }), createText: text => ({ children: [], text }), createComment: text => ({ children: [], text }),
    patchProp: () => {}, setText: (node, text) => { node.text = text }, setElementText: (node, text) => { node.text = text },
    parentNode: node => node.parent || null,
    nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] || null,
    insert: (node, parent, anchor) => {
      if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
      const at = anchor ? parent.children.indexOf(anchor) : -1
      parent.children.splice(at < 0 ? parent.children.length : at, 0, node); node.parent = parent
    },
    remove: node => { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1) },
    insertStaticContent: (_content, parent) => {
      const node = { children: [], parent }; parent.children.push(node); return [node, node]
    },
  })
  // Run the real component setup and watchers; the collector replaces its
  // server-only compiled template for this reactive lifecycle assertion.
  const app = renderer.createApp({ ...ModelProfileEditor, render: () => h(Modal) }, { open: true, provider, profile, catalog: [] })
  app.provide(ssrContextKey, { modules: new Set() })
  app.mount({ children: [] })
  return () => app.unmount()
}
describe('model profile reasoning feedback', () => {
  it('keeps filtered candidates out of errors and preserves exact default choices', async () => {
    const html = await render()
    expect(html).toContain('value="interface-default"')
    expect(html).toContain('value="effort:high"')
    expect(html).not.toContain('reasoning.enabled')
    expect(html).not.toContain('class="error-box"')
    expect(fixture.draftProfile!().overrides.defaults).toBeUndefined()
    fixture.chooseDefaultReasoning!('effort:high')
    expect(fixture.draftProfile!().overrides.defaults).toEqual({ reasoning: { effort: 'high' } })
    fixture.chooseDefaultReasoning!('interface-default')
    expect(fixture.draftProfile!().overrides.defaults).toEqual({ reasoning: {} })
  })
  it('still displays an actual reasoning configuration error', async () => {
    fixture.preview = described('reasoning settings: invalid custom mapping')
    const html = await render()
    expect(html).toContain('role="alert"')
    expect(html).toContain('reasoning settings: invalid custom mapping')
    expect(html).not.toContain('no declared reasoning-off value')
  })
  it('settles after the server binds recommendations without repeatedly describing an unchanged draft', async () => {
    fixture.preview = { ...described(), recommendation_target: { model_id: profile.model_id, protocol: 'openai-responses', dialect: 'openai', contract: 'new-contract' } }
    const unmount = mountEditor()
    try {
      await vi.advanceTimersByTimeAsync(1000)
      await nextTick()
      const calls = vi.mocked(describeModel).mock.calls.length
      expect(calls).toBeGreaterThan(0)
      expect(calls).toBeLessThanOrEqual(2)
      await vi.advanceTimersByTimeAsync(1000)
      expect(vi.mocked(describeModel).mock.calls).toHaveLength(calls)
      expect(fixture.draftProfile!().recommendation_target).toEqual(fixture.preview!.recommendation_target)
    } finally { unmount() }
  })
  for (const preset of ['', 'commandcode']) {
    it(`saves a newly selected model and exact default with its catalog target (preset=${preset})`, async () => {
      const connection = { ...provider, preset_id: preset, protocol: 'openai-completions', dialect: 'compatible' }
      const old = { ...profile, protocol: 'openai-completions', dialect: 'compatible', recommendation_target: {
        model_id: profile.model_id, protocol: 'openai-completions', dialect: 'compatible', contract: 'old-contract',
      } }
      const discovery: Discovery = { id: 'vendor/new', name: 'New model', protocol: 'openai-responses', dialect: 'openai',
        recommendations: { ...profile.recommendations, reasoning: { supported: true, efforts: ['high'] } },
        recommendation_target: { model_id: 'vendor/new', protocol: 'openai-responses', dialect: 'openai', contract: 'new-contract' },
      }
      await render(connection, old, [discovery])
      fixture.chooseModel!(discovery.id)
      fixture.selectProtocol!('openai-responses')
      fixture.chooseDefaultReasoning!('effort:high')
      await fixture.save!()
      expect(fixture.saveModel).toHaveBeenCalledOnce()
      const saved = fixture.saveModel.mock.calls[0]![0].body
      expect(saved).toMatchObject({ model_id: 'vendor/new', protocol: 'openai-responses',
        recommendation_target: discovery.recommendation_target,
        recommendations: discovery.recommendations,
        overrides: { defaults: { reasoning: { effort: 'high' } } },
      })
      expect(saved.overrides.reasoning).toBeUndefined()
    })
  }
})
