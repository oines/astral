import { createSchedule, deleteSchedule, listSchedules, updateSchedule } from '@astral/sdk'
import type { AstralInternalDomainSchedule, AstralInternalDomainScheduleTiming, AstralInternalScheduleInput } from '@astral/sdk'
import { request } from './api'

export type ScheduleTiming = AstralInternalDomainScheduleTiming
export type ScheduleInput = AstralInternalScheduleInput
export type Schedule = AstralInternalDomainSchedule
export const scheduleAPI = {
  list: () => request(listSchedules()),
  create: (agent_id: string, input: ScheduleInput) => request(createSchedule({ body: { agent_id, ...input } })),
  update: (schedule: Schedule, input: ScheduleInput) => request(updateSchedule({ path: { id: schedule.id }, body: { revision: schedule.revision, ...input } })),
  remove: (schedule: Schedule) => request(deleteSchedule({ path: { id: schedule.id }, query: { revision: schedule.revision } })),
}
export function scheduleInput(schedule: Schedule): ScheduleInput {
  return { title: schedule.title, session_mode: schedule.session_mode, ...(schedule.session_mode === 'new' ? {} : { session_id: schedule.session_id }), prompt: schedule.prompt, enabled: schedule.enabled, timing: { ...schedule.timing } }
}
export function hasFutureSchedule(schedule: Schedule) { return schedule.enabled && !!schedule.next_run_at }
export function hasScheduledSession(schedule: Schedule) { return hasFutureSchedule(schedule) && schedule.session_mode === 'existing' && !!schedule.session_id }

export type RepeatKind = 'once' | 'daily' | 'weekly' | 'cron'
export function repeatFields(timing: ScheduleTiming): { kind: RepeatKind; time: string; weekday: string; cron: string } {
  const cron = timing.cron || '0 9 * * *'
  const parts = cron.trim().split(/\s+/)
  if (timing.kind === 'once') return { kind: 'once', time: '09:00', weekday: '1', cron }
  if (parts.length === 5 && /^\d+$/.test(parts[0]!) && /^\d+$/.test(parts[1]!) && parts[2] === '*' && parts[3] === '*') {
    const time = `${parts[1]!.padStart(2, '0')}:${parts[0]!.padStart(2, '0')}`
    if (parts[4] === '*') return { kind: 'daily', time, weekday: '1', cron }
    if (/^[0-6]$/.test(parts[4]!)) return { kind: 'weekly', time, weekday: parts[4]!, cron }
  }
  return { kind: 'cron', time: '09:00', weekday: '1', cron }
}
export function repeatCron(kind: RepeatKind, time: string, weekday: string, custom: string) {
  if (kind === 'cron') return custom.trim()
  const [hour, minute] = time.split(':').map(Number)
  if (!Number.isInteger(hour) || !Number.isInteger(minute) || hour! < 0 || hour! > 23 || minute! < 0 || minute! > 59) throw new Error('Invalid time.')
  return `${minute} ${hour} * * ${kind === 'weekly' ? weekday : '*'}`
}
