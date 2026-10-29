/* eslint-disable vue/one-component-per-file -- Shared controls are isolated SSR test stubs. */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, getCurrentInstance, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import SchedulesPage from './SchedulesPage.vue'
import type { Schedule, ScheduleInput } from '../lib/schedules'

interface Setup {
  draft: { title: string; sessionMode: string; sessionID: string; prompt: string; enabled: boolean; repeat: string; at: string; time: string; weekday: string; cron: string }
  editor?: { agentID: string; current?: Schedule }
  editorError: string
  begin: (current?: Schedule) => void
  save: () => Promise<void>
  toggle: (task: Schedule, enabled: boolean) => Promise<void>
  setSessionMode: (value: string) => void
}
const fixture = vi.hoisted(() => ({
  setup: undefined as Setup | undefined,
  app: { activeAgentID: 'bot', timezone: 'UTC', displayTimezone: 'UTC', timezoneCompatible: true, schedules: [] as Schedule[], schedulesReady: true, schedulesError: '', sessions: [{ id: 'session', agent_id: 'bot', title: 'My conversation', archived: false }], saveSchedule: vi.fn(async (_input: ScheduleInput, _current?: Schedule, _agentID?: string) => {}), refreshSchedules: vi.fn(async () => {}), deleteSchedule: vi.fn(async () => {}) },
}))
vi.mock('../stores/app', () => ({ useAppStore: () => fixture.app }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values: Record<string, unknown> = {}) => `${key}${Object.values(values).length ? ` ${Object.values(values).join(' ')}` : ''}`, locale: { value: 'en' } }) }))
vi.mock('@astral/ui', () => ({
  Button: defineComponent({ setup(_props, { slots, attrs }) {
    if (!fixture.setup) fixture.setup = (getCurrentInstance()!.parent! as unknown as { setupState: Setup }).setupState
    return () => h('button', attrs, slots.default?.())
  } }),
  SettingsSection: defineComponent({ setup(_props, { slots }) { return () => h('section', slots.default?.()) } }),
  SettingsRow: defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.()) } }),
  ConfirmPopover: defineComponent({ setup(_props, { slots }) { return () => h('div', slots.trigger?.()) } }),
  Select: defineComponent({ setup() { return () => h('select') } }),
  Switch: defineComponent({ setup() { return () => h('button') } }),
  useDelayedPending: () => false,
}))
const task: Schedule = { id: 'task', agent_id: 'bot', session_mode: 'existing', session_id: 'session', title: 'API-created task', prompt: 'Read my inbox', enabled: true, timing: { kind: 'cron', cron: '0 9 * * *' }, next_run_at: '2027-01-01T01:00:00Z', revision: 4, created_at: '', updated_at: '' }
async function render() {
  const app = createSSRApp(SchedulesPage)
  app.component('RouterLink', defineComponent({ setup(_props, { slots }) { return () => h('a', slots.default?.()) } }))
  return renderToString(app)
}
beforeEach(() => {
  fixture.setup = undefined
  fixture.app.schedules = [task]
  fixture.app.activeAgentID = 'bot'
  fixture.app.timezone = 'UTC'
  fixture.app.displayTimezone = 'UTC'
  fixture.app.timezoneCompatible = true
  fixture.app.sessions = [{ id: 'session', agent_id: 'bot', title: 'My conversation', archived: false }]
  vi.clearAllMocks()
})
describe('shared Gateway and UI schedules', () => {
  it('requires a supported global timezone for new one-time wall-clock input', async () => {
    fixture.app.displayTimezone = 'System'
    fixture.app.timezoneCompatible = false
    await render()
    fixture.setup!.begin()
    Object.assign(fixture.setup!.draft, { title: 'One-time task', prompt: 'Run once', repeat: 'once', at: '2027-01-01T08:30' })
    await fixture.setup!.save()
    expect(fixture.app.saveSchedule).not.toHaveBeenCalled()
    expect(fixture.setup!.editorError).toBe('scheduleTimezoneUnavailable')
  })
  it('converts a one-time wall clock using the global timezone without a per-task timezone', async () => {
    fixture.app.timezone = 'Asia/Shanghai'
    fixture.app.displayTimezone = 'Asia/Shanghai'
    await render()
    fixture.setup!.begin()
    Object.assign(fixture.setup!.draft, { title: 'One-time task', prompt: 'Run once', repeat: 'once', at: '2027-01-01T08:30' })
    await fixture.setup!.save()
    expect(fixture.app.saveSchedule.mock.calls[0]![0].timing).toEqual({ kind: 'once', at: '2027-01-01T00:30:00.000Z' })
  })
  it('preserves the original instant and precision when editing a one-time task in a repeated DST hour', async () => {
    fixture.app.timezone = 'America/New_York'
    fixture.app.displayTimezone = 'America/New_York'
    await render()
    const current: Schedule = { ...task, timing: { kind: 'once', at: '2027-11-07T06:30:27.123Z' } }
    fixture.setup!.begin(current)
    expect(fixture.setup!.draft.at).toBe('2027-11-07T01:30')
    await fixture.setup!.save()
    expect(fixture.app.saveSchedule.mock.calls[0]![0].timing.at).toBe('2027-11-07T06:30:27.123Z')
  })
  it('can create a fresh-session plan before the Bot has any conversations', async () => {
    fixture.app.sessions = []
    await render()
    fixture.setup!.begin()
    expect(fixture.setup!.draft.sessionMode).toBe('new')
    Object.assign(fixture.setup!.draft, { title: 'Independent report', prompt: 'Prepare a report from scratch', repeat: 'daily', time: '08:30' })
    await fixture.setup!.save()
    expect(fixture.app.saveSchedule).toHaveBeenCalledWith({ title: 'Independent report', session_mode: 'new', prompt: 'Prepare a report from scratch', enabled: true, timing: { kind: 'cron', cron: '30 8 * * *' } }, undefined, 'bot')
  })
  it('clears the selected conversation when switching to a fresh session each time', async () => {
    await render()
    fixture.setup!.begin(task)
    expect(fixture.setup!.draft.sessionID).toBe('session')
    fixture.setup!.setSessionMode('new')
    expect(fixture.setup!.draft.sessionID).toBe('')
    Object.assign(fixture.setup!.draft, { title: 'Independent task' })
    await fixture.setup!.save()
    const input = fixture.app.saveSchedule.mock.calls[0]![0]
    expect(input.session_mode).toBe('new')
    expect(input).not.toHaveProperty('session_id')
  })
  it('shows the new-session execution mode and links only a known latest generated session', async () => {
    fixture.app.schedules = [{ ...task, session_mode: 'new', session_id: undefined, last_session_id: 'session' }]
    const html = await render()
    expect(html).toContain('scheduleNewSessionEachTime')
    expect(html).toContain('scheduleLatestSession')
    expect(html).toContain('My conversation')
    fixture.setup = undefined
    fixture.app.schedules = [{ ...task, session_mode: 'new', session_id: undefined, last_session_id: 'deleted' }]
    expect(await render()).not.toContain('scheduleLatestSession')
  })
  it('lists API-created plans and their delivery state without claiming Agent work is completed', async () => {
    fixture.app.schedules = [{ ...task, enabled: false, timing: { kind: 'once', at: '2026-10-01T00:00:00Z' }, next_run_at: undefined, last_status: 'delivered', last_run_at: '2026-10-01T00:00:00Z' }]
    const html = await render()
    expect(html).toContain('API-created task')
    expect(html).toContain('My conversation')
    expect(html).toContain('scheduleDelivered')
    expect(html).toContain('scheduleEnded')
    expect(html).not.toContain('scheduleCompleted')
    expect(fixture.app.saveSchedule).not.toHaveBeenCalled()
  })
  it('creates a manually entered recurring task without a task-specific timezone', async () => {
    await render()
    fixture.setup!.begin()
    Object.assign(fixture.setup!.draft, { title: 'Daily report', prompt: 'Prepare the report', repeat: 'daily', time: '08:30' })
    await fixture.setup!.save()
    expect(fixture.app.saveSchedule).toHaveBeenCalledWith({ title: 'Daily report', session_mode: 'existing', session_id: 'session', prompt: 'Prepare the report', enabled: true, timing: { kind: 'cron', cron: '30 8 * * *' } }, undefined, 'bot')
    expect(fixture.setup!.editor).toBeUndefined()
  })
  it('pauses a task with its revision and preserves all other fields', async () => {
    await render()
    await fixture.setup!.toggle(task, false)
    expect(fixture.app.saveSchedule).toHaveBeenCalledWith({ title: task.title, session_mode: 'existing', session_id: task.session_id, prompt: task.prompt, enabled: false, timing: task.timing }, task)
  })
  it('pauses a fresh-session task without turning its latest conversation into a fixed target', async () => {
    await render()
    const independent: Schedule = { ...task, session_mode: 'new', session_id: undefined, last_session_id: 'session' }
    await fixture.setup!.toggle(independent, false)
    expect(fixture.app.saveSchedule).toHaveBeenCalledWith({ title: task.title, session_mode: 'new', prompt: task.prompt, enabled: false, timing: task.timing }, independent)
  })
  it('keeps the draft and refreshes task records after an uncertain create instead of retrying', async () => {
    await render()
    fixture.setup!.begin()
    Object.assign(fixture.setup!.draft, { title: 'Daily report', prompt: 'Prepare the report', repeat: 'daily' })
    fixture.app.saveSchedule.mockRejectedValueOnce(new TypeError('Disconnected'))
    await fixture.setup!.save()
    expect(fixture.app.saveSchedule).toHaveBeenCalledOnce()
    expect(fixture.app.refreshSchedules).toHaveBeenCalledOnce()
    expect(fixture.setup!.editor).toBeDefined()
    expect(fixture.setup!.draft.prompt).toBe('Prepare the report')
    expect(fixture.setup!.editorError).toContain('scheduleCreateUncertain')
  })
})
