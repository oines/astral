<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, ConfirmPopover, Select, SettingsRow, SettingsSection, Switch, useDelayedPending } from '@astral/ui'
import { Clock3, Pencil, Plus, Trash2 } from '@astral/icons'
import { useAppStore } from '../stores/app'
import { errorText, object } from '../lib/api'
import { repeatCron, repeatFields, scheduleInput, type RepeatKind, type Schedule, type ScheduleInput } from '../lib/schedules'
import { formatDateTime, wallDateTime, wallTimeInstant } from '../lib/timezone'

const app = useAppStore()
const { t, locale } = useI18n()
const tasks = computed(() => app.schedules.filter(task => task.agent_id === app.activeAgentID).sort((a, b) => {
  if (a.next_run_at && b.next_run_at) return a.next_run_at.localeCompare(b.next_run_at)
  if (a.next_run_at || b.next_run_at) return a.next_run_at ? -1 : 1
  return b.updated_at.localeCompare(a.updated_at)
}))
const editor = ref<{ agentID: string; current?: Schedule }>()
const draftAtInstant = ref<string>()
const draft = reactive({ title: '', sessionMode: 'existing' as 'existing' | 'new', sessionID: '', prompt: '', enabled: true, repeat: 'once' as RepeatKind, at: '', time: '09:00', weekday: '1', cron: '0 9 * * *' })
const editorError = ref('')
const saving = ref(false)
const action = ref('')
const waitingAction = useDelayedPending(() => action.value)
const actionErrors = ref<Record<string, string>>({})
const deleting = ref('')
const showLoading = useDelayedPending(() => !app.schedulesReady && !app.schedulesError)
const sessions = computed(() => app.sessions.filter(session => session.agent_id === app.activeAgentID))
const sessionOptions = computed(() => sessions.value.map(session => ({ value: session.id, label: session.archived ? `${session.title} · ${t('archive')}` : session.title })))
const sameBot = computed(() => !editor.value || editor.value.agentID === app.activeAgentID)
const executionOptions = computed(() => [{ value: 'existing', label: t('scheduleUseExistingSession') }, { value: 'new', label: t('scheduleNewSessionEachTime') }])
const repeatOptions = computed(() => [
  { value: 'once', label: t('scheduleOnce') }, { value: 'daily', label: t('scheduleDaily') },
  { value: 'weekly', label: t('scheduleWeekly') }, { value: 'cron', label: t('scheduleCustom') },
])
const weekdayOptions = computed(() => Array.from({ length: 7 }, (_, day) => ({ value: String(day), label: t(`scheduleWeekdays.${day}`) })))
function sessionName(id?: string) { return app.sessions.find(session => session.id === id)?.title || t('missing') }
function hasLatestSession(task: Schedule) { return !!task.last_session_id && app.sessions.some(session => session.id === task.last_session_id && session.agent_id === task.agent_id) }
function setSessionMode(value: string) {
  draft.sessionMode = value === 'new' ? 'new' : 'existing'
  draft.sessionID = draft.sessionMode === 'new' ? '' : sessions.value[0]?.id || ''
}
function formatTime(value?: string | null) {
  return formatDateTime(value || undefined, locale.value, app.displayTimezone, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}
function ruleLabel(task: Schedule) {
  if (task.timing.kind === 'once') return t('scheduleAtLabel', { time: formatTime(task.timing.at) })
  const fields = repeatFields(task.timing)
  if (fields.kind === 'daily') return t('scheduleDailyAt', { time: fields.time })
  if (fields.kind === 'weekly') return t('scheduleWeeklyAt', { day: t(`scheduleWeekdays.${fields.weekday}`), time: fields.time })
  return task.timing.cron || ''
}
function lastStatus(task: Schedule) {
  const labels: Record<string, string> = { dispatching: 'scheduleDispatching', delivered: 'scheduleDelivered', missed: 'scheduleMissed', failed: 'scheduleFailed' }
  const status = task.last_status || ''
  return labels[status] ? t(labels[status]) : status
}
function ended(task: Schedule) { return task.timing.kind === 'once' && !task.next_run_at && !!task.last_status && task.last_status !== 'dispatching' }
function begin(current?: Schedule) {
  if (saving.value) return
  const timing = current?.timing || { kind: 'once' as const }
  const repeat = repeatFields(timing)
  const sessionMode = current ? current.session_mode : sessions.value.length ? 'existing' : 'new'
  draftAtInstant.value = timing.at || new Date(Date.now() + 60 * 60_000).toISOString()
  Object.assign(draft, {
    title: current?.title || '', sessionMode, sessionID: sessionMode === 'new' ? '' : current?.session_id || sessions.value[0]?.id || '', prompt: current?.prompt || '', enabled: current?.enabled ?? true,
    repeat: repeat.kind, at: wallDateTime(draftAtInstant.value, app.displayTimezone), time: repeat.time, weekday: repeat.weekday, cron: repeat.cron,
  })
  editor.value = { agentID: app.activeAgentID, current }
  editorError.value = ''
}
function input(): ScheduleInput {
  if (!draft.title.trim() || !draft.prompt.trim()) throw new Error(t('scheduleRequired'))
  if (draft.sessionMode === 'existing' && !draft.sessionID) throw new Error(t('scheduleSessionRequired'))
  const base = { title: draft.title.trim(), ...(draft.sessionMode === 'new' ? { session_mode: 'new' as const } : { session_mode: 'existing' as const, session_id: draft.sessionID }), prompt: draft.prompt, enabled: draft.enabled }
  if (draft.repeat === 'once') {
    const original = editor.value?.current?.timing.at
    if (!app.timezoneCompatible && !original) throw new Error(t('scheduleTimezoneUnavailable'))
    const at = original && (!app.timezoneCompatible || wallDateTime(original, app.displayTimezone) === draft.at) ? new Date(original) : wallTimeInstant(draft.at, app.displayTimezone)
    if (!at) throw new Error(t('scheduleWallTimeInvalid'))
    if (draft.enabled && at.getTime() <= Date.now()) throw new Error(t('scheduleFutureRequired'))
    return { ...base, timing: { kind: 'once', at: at.toISOString() } }
  }
  let cron: string
  try { cron = repeatCron(draft.repeat, draft.time, draft.weekday, draft.cron) }
  catch { throw new Error(t('scheduleTimeRequired')) }
  if (cron.split(/\s+/).length !== 5) throw new Error(t('scheduleCronRequired'))
  return { ...base, timing: { kind: 'cron', cron } }
}
watch(() => app.displayTimezone, (zone, previous) => {
  if (!editor.value || draft.repeat !== 'once' || !previous) return
  const original = editor.value.current?.timing.at
  const at = original && wallDateTime(original, previous) === draft.at ? new Date(original) : draft.at ? wallTimeInstant(draft.at, previous) : draftAtInstant.value ? new Date(draftAtInstant.value) : undefined
  if (at) { draftAtInstant.value = at.toISOString(); draft.at = wallDateTime(draftAtInstant.value, zone) }
})
async function save() {
  if (!editor.value || saving.value || !sameBot.value) return
  const editing = editor.value
  editorError.value = ''
  let payload: ScheduleInput
  try { payload = input() } catch (error) { editorError.value = errorText(error); return }
  saving.value = true
  try {
    await app.saveSchedule(payload, editing.current, editing.agentID)
    editor.value = undefined
  } catch (error) {
    const uncertain = !editing.current && typeof object(error).code !== 'string'
    editorError.value = `${errorText(error)}${uncertain ? ` ${t('scheduleCreateUncertain')}` : ''}`
    await app.refreshSchedules().catch(() => undefined)
  } finally { saving.value = false }
}
async function toggle(task: Schedule, enabled: boolean) {
  if (action.value) return
  action.value = task.id
  delete actionErrors.value[task.id]
  try { await app.saveSchedule({ ...scheduleInput(task), enabled }, task) }
  catch (error) { actionErrors.value[task.id] = errorText(error); await app.refreshSchedules().catch(() => undefined) }
  finally { action.value = '' }
}
async function remove(task: Schedule) {
  if (action.value) return
  action.value = task.id
  delete actionErrors.value[task.id]
  try {
    await app.deleteSchedule(task)
    if (editor.value?.current?.id === task.id) editor.value = undefined
    deleting.value = ''
  } catch (error) { actionErrors.value[task.id] = errorText(error) }
  finally { action.value = '' }
}
</script>
<template>
  <div class="schedules-scroll">
    <div class="settings-content schedules-content">
      <header class="settings-title">
        <h1>{{ t('schedules') }}</h1>
        <Button
          :disabled="!app.activeAgentID || !!editor"
          @click="begin()"
        >
          <Plus />{{ t('scheduleAdd') }}
        </Button>
      </header>
      <form
        v-if="editor"
        class="settings-form stack"
        @submit.prevent="save"
      >
        <SettingsSection :title="t(editor.current ? 'scheduleEdit' : 'scheduleAdd')">
          <SettingsRow :label="t('scheduleTitle')">
            <input
              v-model="draft.title"
              :aria-label="t('scheduleTitle')"
              :disabled="saving"
              required
              maxlength="200"
            >
          </SettingsRow>
          <SettingsRow
            :label="t('scheduleExecutionMode')"
            :description="draft.sessionMode === 'new' ? t('scheduleNewSessionHint') : undefined"
          >
            <Select
              :model-value="draft.sessionMode"
              :options="executionOptions"
              :label="t('scheduleExecutionMode')"
              :disabled="saving"
              @update:model-value="setSessionMode"
            />
          </SettingsRow>
          <SettingsRow
            v-if="draft.sessionMode === 'existing'"
            :label="t('scheduleSession')"
            :description="!sessions.length ? t('scheduleSessionRequired') : undefined"
          >
            <Select
              v-model="draft.sessionID"
              :options="sessionOptions"
              :label="t('scheduleSession')"
              :disabled="saving || !sameBot"
              required
            />
          </SettingsRow>
          <SettingsRow :label="t('scheduleRepeat')">
            <Select
              v-model="draft.repeat"
              :options="repeatOptions"
              :label="t('scheduleRepeat')"
              :disabled="saving"
            />
          </SettingsRow>
          <SettingsRow
            v-if="draft.repeat === 'once'"
            :label="t('scheduleAt')"
            :description="!app.timezoneCompatible ? t('scheduleTimezoneUnavailable') : undefined"
          >
            <div class="settings-text">
              <input
                v-model="draft.at"
                type="datetime-local"
                :aria-label="t('scheduleAt')"
                :disabled="saving || !app.timezoneCompatible"
                required
              >
              <RouterLink
                v-if="!app.timezoneCompatible"
                to="/settings/general"
                class="settings-inline-link"
              >
                {{ t('configureTimezone') }}
              </RouterLink>
            </div>
          </SettingsRow>
          <SettingsRow
            v-if="draft.repeat === 'daily' || draft.repeat === 'weekly'"
            :label="t('scheduleTime')"
          >
            <input
              v-model="draft.time"
              type="time"
              :aria-label="t('scheduleTime')"
              :disabled="saving"
              required
            >
          </SettingsRow>
          <SettingsRow
            v-if="draft.repeat === 'weekly'"
            :label="t('scheduleWeekday')"
          >
            <Select
              v-model="draft.weekday"
              :options="weekdayOptions"
              :label="t('scheduleWeekday')"
              :disabled="saving"
            />
          </SettingsRow>
          <SettingsRow
            v-if="draft.repeat === 'cron'"
            :label="t('scheduleCron')"
            :description="t('scheduleCronHint')"
          >
            <input
              v-model="draft.cron"
              :aria-label="t('scheduleCron')"
              :disabled="saving"
              required
              spellcheck="false"
            >
          </SettingsRow>
          <div class="schedule-prompt-field label">
            <label for="schedule-prompt">{{ t('schedulePrompt') }}</label>
            <textarea
              id="schedule-prompt"
              v-model="draft.prompt"
              :disabled="saving"
              required
              rows="5"
            />
          </div>
          <SettingsRow :label="t('scheduleEnabled')">
            <Switch
              v-model="draft.enabled"
              :label="t('scheduleEnabled')"
              :disabled="saving"
            />
          </SettingsRow>
          <p
            v-if="!sameBot || editorError"
            class="settings-feedback-error text-danger"
            role="alert"
          >
            {{ !sameBot ? t('scheduleReadOnlyBot') : editorError }}
          </p>
          <div class="settings-submit-row">
            <div class="settings-actions">
              <Button
                :disabled="saving"
                @click="editor = undefined"
              >
                {{ t('cancel') }}
              </Button>
              <Button
                type="submit"
                variant="primary"
                :busy="saving"
                :disabled="!sameBot"
              >
                {{ t(editor.current ? 'save' : 'scheduleCreate') }}
              </Button>
            </div>
          </div>
        </SettingsSection>
      </form>
      <div
        v-if="app.schedulesError"
        class="error-box row wrap"
        role="alert"
      >
        <span class="grow">{{ app.schedulesError }}</span>
        <Button @click="app.refreshSchedules().catch(() => undefined)">
          {{ t('retry') }}
        </Button>
      </div>
      <p
        v-if="showLoading"
        class="muted"
        role="status"
      >
        {{ t('loading') }}
      </p>
      <div
        v-else-if="app.schedulesReady && !tasks.length && !editor"
        class="empty-state"
      >
        <Clock3 />
        <strong>{{ t('scheduleEmpty') }}</strong>
        <p>{{ t('scheduleEmptyHint') }}</p>
        <Button
          v-if="app.activeAgentID"
          @click="begin()"
        >
          <Plus />{{ t('scheduleAdd') }}
        </Button>
      </div>
      <SettingsSection v-if="tasks.length">
        <div
          v-for="task in tasks"
          :key="task.id"
          class="resource-row schedule-row"
        >
          <div class="schedule-summary">
            <strong>{{ task.title }}</strong>
            <small v-if="task.session_mode === 'new'">{{ t('scheduleNewSessionEachTime') }}</small>
            <RouterLink
              v-else
              class="schedule-session"
              :to="`/sessions/${task.session_id}`"
            >
              {{ sessionName(task.session_id) }}
            </RouterLink>
            <RouterLink
              v-if="task.session_mode === 'new' && hasLatestSession(task)"
              class="schedule-session"
              :to="`/sessions/${task.last_session_id}`"
            >
              {{ t('scheduleLatestSession') }} · {{ sessionName(task.last_session_id) }}
            </RouterLink>
            <small>{{ ruleLabel(task) }}</small>
            <small v-if="task.enabled && task.next_run_at">{{ t('scheduleNext', { time: formatTime(task.next_run_at) }) }}</small>
            <small v-else>{{ t(ended(task) ? 'scheduleEnded' : 'schedulePaused') }}</small>
            <small v-if="task.last_status && task.last_run_at">{{ t('scheduleLast', { status: lastStatus(task), time: formatTime(task.last_run_at) }) }}</small>
            <small
              v-if="task.last_error"
              class="text-danger"
            >{{ task.last_error }}</small>
            <small
              v-if="actionErrors[task.id] && deleting !== task.id"
              class="text-danger"
              role="alert"
            >{{ actionErrors[task.id] }}</small>
          </div>
          <div class="schedule-actions">
            <span
              v-if="waitingAction && action === task.id"
              class="spinner"
              role="status"
              :aria-label="t('loading')"
            />
            <Switch
              :model-value="task.enabled"
              :label="`${t(task.enabled ? 'schedulePause' : 'scheduleResume')} · ${task.title}`"
              :disabled="!!action || task.last_status === 'dispatching' || ended(task)"
              @update:model-value="toggle(task, $event)"
            />
            <Button
              size="icon"
              variant="ghost"
              :aria-label="`${t('scheduleEdit')} · ${task.title}`"
              :title="t('scheduleEdit')"
              :disabled="!!action || saving"
              @click="begin(task)"
            >
              <Pencil />
            </Button>
            <ConfirmPopover
              :open="deleting === task.id"
              :title="t('scheduleDeleteTitle', { name: task.title })"
              :description="t('scheduleDeleteHint')"
              :confirm-label="t('scheduleDelete')"
              :cancel-label="t('cancel')"
              :busy="action === task.id"
              :error="actionErrors[task.id]"
              @update:open="deleting = $event ? task.id : ''"
              @confirm="remove(task)"
            >
              <template #trigger>
                <Button
                  size="icon"
                  variant="ghost"
                  :aria-label="`${t('scheduleDelete')} · ${task.title}`"
                  :title="t('scheduleDelete')"
                  :disabled="!!action || saving"
                >
                  <Trash2 />
                </Button>
              </template>
            </ConfirmPopover>
          </div>
        </div>
      </SettingsSection>
    </div>
  </div>
</template>
