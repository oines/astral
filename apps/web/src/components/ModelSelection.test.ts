import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { Profile } from '../lib/types'
import ModelSelection from './ModelSelection.vue'

const fixture = vi.hoisted(() => ({
  models: [] as Profile[],
  options: [] as Array<{ value: string; label: string }>,
  choose: undefined as ((value: string) => void) | undefined,
  select: vi.fn(),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('../stores/app', () => ({ useAppStore: () => ({ models: fixture.models, availableModels: fixture.models.filter(model => model.available) }) }))
vi.mock('@astral/ui', async () => {
  const { defineComponent } = await import('vue')
  return { ModelPicker: defineComponent({
    props: { models: { type: Array, required: true } },
    emits: ['update:modelValue'],
    setup(props, { emit }) {
      fixture.options = props.models as typeof fixture.options
      fixture.choose = value => emit('update:modelValue', value)
      return () => null
    },
  }) }
})

beforeEach(() => {
  fixture.models = [{ id: 'fixed', name: 'Fixed model', available: true, provider_id: 'provider' }] as Profile[]
  fixture.options = []
  fixture.choose = undefined
  fixture.select.mockClear()
})

async function render(props: Record<string, unknown> = {}) {
  await renderToString(createSSRApp(ModelSelection, {
    modelValue: 'fixed', reasoning: { effort: 'high' }, emptyLabel: 'Follow current Session model', onSelect: fixture.select, ...props,
  }))
}

describe('optional model selection', () => {
  it('allows returning to the Session model without an explicit allowEmpty prop and clears the fixed reasoning', async () => {
    await render()
    expect(fixture.options[0]).toMatchObject({ value: '', label: 'Follow current Session model' })
    fixture.choose!('')
    expect(fixture.select).toHaveBeenCalledExactlyOnceWith({ profile_id: '', reasoning: undefined })
  })

  it('keeps the empty choice excluded when a caller explicitly disables it', async () => {
    await render({ allowEmpty: false })
    expect(fixture.options.map(option => option.value)).toEqual(['fixed'])
  })

  it('does not add an empty choice to selectors without an empty label', async () => {
    await render({ emptyLabel: undefined })
    expect(fixture.options.map(option => option.value)).toEqual(['fixed'])
  })
})
