/* eslint-disable vue/one-component-per-file -- Shared menu primitives are isolated SSR test stubs. */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, getCurrentInstance, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import TimezonePicker from './TimezonePicker.vue'

interface Setup { search: string; label: string; options: Array<{ value: string; label: string }>; choose: (value: string) => void }
const fixture = vi.hoisted(() => ({ setup: undefined as Setup | undefined }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values: Record<string, string> = {}) => `${key}${values.timezone ? ` (${values.timezone})` : ''}` }) }))
vi.mock('@astral/ui', () => ({ Menu: defineComponent({ setup(_props, { slots }) {
  fixture.setup = (getCurrentInstance()!.parent! as unknown as { setupState: Setup }).setupState
  return () => h('div', [slots.trigger?.(), slots.default?.()])
} }),
  MenuFilter: defineComponent({ setup() { return () => h('input') } }),
  MenuItemIndicator: defineComponent({ setup(_props, { slots }) { return () => h('span', slots.default?.()) } }),
  MenuRadioGroup: defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.()) } }),
  MenuRadioItem: defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.()) } }),
}))
beforeEach(() => { fixture.setup = undefined })
describe('global timezone picker', () => {
  it('shows the Server system timezone as the inherited default', async () => {
    await renderToString(createSSRApp(TimezonePicker, { modelValue: '', system: 'Asia/Tokyo' }))
    expect(fixture.setup!.label).toBe('timezoneSystem (Asia/Tokyo)')
    expect(fixture.setup!.options[0]).toEqual({ value: '__astral_system_timezone__', label: 'timezoneSystem (Asia/Tokyo)' })
  })
  it('keeps a fixed or custom system zone as inheritance instead of offering it as an invalid IANA override', async () => {
    await renderToString(createSSRApp(TimezonePicker, { modelValue: '', system: '+08:00' }))
    expect(fixture.setup!.label).toBe('timezoneSystem (+08:00)')
    expect(fixture.setup!.options.some(option => option.value === '+08:00')).toBe(false)
    expect(fixture.setup!.options[0]!.value).toBe('__astral_system_timezone__')
  })
  it('searches IANA names and saves selection or system inheritance immediately', async () => {
    const update = vi.fn()
    await renderToString(createSSRApp(TimezonePicker, { modelValue: '', system: 'Asia/Tokyo', 'onUpdate:modelValue': update }))
    fixture.setup!.search = 'new york'
    expect(fixture.setup!.options.map(option => option.value)).toEqual(['America/New_York'])
    fixture.setup!.choose('America/New_York')
    expect(update).toHaveBeenLastCalledWith('America/New_York')
    fixture.setup!.choose('__astral_system_timezone__')
    expect(update).toHaveBeenLastCalledWith('')
  })
})
