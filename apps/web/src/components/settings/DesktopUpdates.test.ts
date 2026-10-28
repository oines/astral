/* eslint-disable vue/one-component-per-file -- Shared layout primitives are isolated test stubs. */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, getCurrentInstance, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { DesktopUpdateState } from '../../../../desktop/src/shared/updates'
import { updatesEn } from '../../i18n/updates'
import DesktopUpdates from './DesktopUpdates.vue'

interface Setup {
  state?: DesktopUpdateState
  error: string
  busy: boolean
  act: (intent?: 'check' | 'update' | 'downloads') => Promise<void>
}
const fixture = vi.hoisted(() => ({ state: undefined as DesktopUpdateState | undefined, setup: undefined as Setup | undefined }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: keyof typeof updatesEn, values: Record<string, string> = {}) =>
  Object.entries(values).reduce((text, [name, value]) => text.replace(`{${name}}`, value), updatesEn[key] || key) }) }))
vi.mock('@astral/ui', () => ({
  SettingsSection: defineComponent({ setup(_props, { slots }) {
    fixture.setup = (getCurrentInstance()!.parent! as unknown as { setupState: Setup }).setupState
    fixture.setup.state = fixture.state
    return () => h('section', slots.default?.())
  } }),
  SettingsRow: defineComponent({ props: { label: { type: String, default: '' }, description: { type: String, default: '' } }, setup(props, { slots }) {
    return () => h('div', [h('strong', props.label), h('p', props.description), slots.default?.()])
  } }),
  Button: defineComponent({ props: { busy: Boolean, disabled: Boolean }, setup(props, { attrs, slots }) {
    return () => h('button', { ...attrs, disabled: props.busy || props.disabled }, slots.default?.())
  } }),
}))
const check = vi.fn(async () => ({ phase: 'available', currentVersion: '0.1.0', version: '0.1.1' } as DesktopUpdateState))
const update = vi.fn(async () => {})
const openExternal = vi.fn(async () => {})
beforeEach(() => {
  vi.clearAllMocks()
  fixture.state = { phase: 'available', currentVersion: '0.1.0', version: '0.1.1' }
  fixture.setup = undefined
  vi.stubGlobal('window', { astralDesktop: { openExternal, updates: { check, update } } })
})
afterEach(() => { vi.unstubAllGlobals() })
const render = () => renderToString(createSSRApp(DesktopUpdates))

describe('Desktop on-demand update actions', () => {
  it('explains download and task interruption before offering update, without starting it', async () => {
    const html = await render()
    expect(html).toContain('0.1.1 is available')
    expect(html).toContain('restarts Astral and interrupts running tasks')
    expect(html).toContain('Update and restart')
    expect(html).toContain('Check for updates')
    expect(check).not.toHaveBeenCalled()
    expect(update).not.toHaveBeenCalled()
  })
  it('refreshes an available version without authorizing download or installation', async () => {
    await render()
    await fixture.setup!.act('check')
    expect(check).toHaveBeenCalledOnce()
    expect(update).not.toHaveBeenCalled()
  })
  it('dispatches the explicit update action instead of installing an old prepared target', async () => {
    await render()
    await fixture.setup!.act()
    expect(update).toHaveBeenCalledOnce()
    expect(check).not.toHaveBeenCalled()
  })
  it('never treats a failed check as authorization to update', async () => {
    fixture.state = { phase: 'error', currentVersion: '0.1.0', version: '0.1.1', retry: 'check', error: 'Metadata could not be fetched' }
    const html = await render()
    expect(html).toContain('Retry check')
    expect(html).toContain('Metadata could not be fetched')
    expect(html).toContain('role="alert"')
    await fixture.setup!.act()
    expect(check).toHaveBeenCalledOnce()
    expect(update).not.toHaveBeenCalled()
  })
  it('keeps an explicit update retry separate from the safe check action', async () => {
    fixture.state = { phase: 'error', currentVersion: '0.1.0', retry: 'update', error: 'Package checksum mismatch' }
    const html = await render()
    expect(html).toContain('Retry update')
    expect(html).toContain('restarts Astral and interrupts running tasks')
    expect(html).toContain('Package checksum mismatch')
    await fixture.setup!.act('check')
    expect(check).toHaveBeenCalledOnce()
    expect(update).not.toHaveBeenCalled()
    await fixture.setup!.act()
    expect(update).toHaveBeenCalledOnce()
  })
  it('disables repeat actions while a requested update is pending and retains its real failure', async () => {
    let fail: (error: Error) => void = () => {}
    update.mockImplementationOnce(() => new Promise<void>((_resolve, reject) => { fail = reject }))
    await render()
    const first = fixture.setup!.act()
    expect(fixture.setup!.busy).toBe(true)
    await fixture.setup!.act()
    await fixture.setup!.act('check')
    expect(update).toHaveBeenCalledOnce()
    expect(check).not.toHaveBeenCalled()
    fail(new Error('Not enough disk space'))
    await first
    expect(fixture.setup!.busy).toBe(false)
    expect(fixture.setup!.error).toBe('Not enough disk space')
  })
  it('shows actual download progress and exposes no duplicate update action', async () => {
    fixture.state = { phase: 'downloading', currentVersion: '0.1.0', version: '0.1.1', percent: 35.8 }
    const html = await render()
    expect(html).toContain('Downloading 0.1.1 · 35%')
    expect(html).toContain('<progress')
    expect(html).not.toContain('<button')
    await fixture.setup!.act()
    expect(update).not.toHaveBeenCalled()
  })
  it('keeps unsupported packages on the manual-download route', async () => {
    fixture.state = { phase: 'unavailable', currentVersion: '0.1.0', unavailable: 'windows-zip' }
    const html = await render()
    expect(html).toContain('Windows installer')
    await fixture.setup!.act()
    expect(openExternal).toHaveBeenCalledWith('https://github.com/oines/astral/releases/latest')
    expect(check).not.toHaveBeenCalled()
    expect(update).not.toHaveBeenCalled()
  })
  it('does not expose native update controls in an ordinary Web page', async () => {
    vi.stubGlobal('window', {})
    expect(await render()).not.toContain('Software update')
    expect(fixture.setup).toBeUndefined()
  })
})
