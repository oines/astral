<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useNow } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { Button, ConfirmPopover, SettingsRow, SettingsSection, Switch, useDelayedPending } from '@astral/ui'
import { Copy, Pencil, Trash2 } from '@astral/icons'
import { useAppStore } from '../stores/app'
import { useWakeEndpoints } from '../composables/useWakeEndpoints'
import { useClipboard } from '../composables/useClipboard'
import { errorText, object } from '../lib/api'
import { formatDateTime } from '../lib/timezone'
import { wakeBlocked, wakeExample, type WakeEndpoint } from '../lib/wake-endpoints'
import CopyValue from './CopyValue.vue'

const app = useAppStore()
const { t, locale } = useI18n()
const feed = useWakeEndpoints(computed(() => app.activeAgentID), computed(() => app.authorized))
const now = useNow({ interval: 1000 })
const endpoints = computed(() => feed.items.value.filter(endpoint => endpoint.agent_id === app.activeAgentID).sort((a, b) => (b.last_received_at || b.created_at).localeCompare(a.last_received_at || a.created_at) || a.id.localeCompare(b.id)))
const editor = ref<WakeEndpoint>()
const editorElement = ref<HTMLFormElement>()
const deliveryURL = computed(() => {
  if (!editor.value) return undefined
  const current = feed.items.value.find(endpoint => endpoint.id === editor.value!.id)
  return current ? current.trigger_url : editor.value.trigger_url
})
const draft = reactive({ title: '', prompt: '', notes: '', enabled: true })
const editorError = ref('')
const saving = ref(false)
const action = ref('')
const actionErrors = ref<Record<string, string>>({})
const deleting = ref('')
const { copy, copied } = useClipboard()
const waiting = useDelayedPending(() => action.value)
const loading = useDelayedPending(() => !feed.ready.value && !feed.error.value)
const sameBot = computed(() => !editor.value || editor.value.agent_id === app.activeAgentID)
function sessionName(id: string) { return app.sessions.find(session => session.id === id)?.title || t('conversationDeleted') }
function computerName(id: string) { return app.computers.find(computer => computer.id === id)?.name || id }
function formatTime(value?: string) { return formatDateTime(value, locale.value, app.displayTimezone, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' }) }
function close() { editor.value = undefined; editorError.value = '' }
function begin(endpoint: WakeEndpoint) {
  if (saving.value || action.value) return
  editor.value = { ...endpoint }
  Object.assign(draft, { title: endpoint.title, prompt: endpoint.prompt || '', notes: endpoint.notes || '', enabled: endpoint.enabled })
  editorError.value = ''
  void nextTick(() => {
    editorElement.value?.scrollIntoView({ block: 'start' })
    editorElement.value?.querySelector('input')?.focus({ preventScroll: true })
  })
}
async function save() {
  const current = editor.value
  if (!current || saving.value || !sameBot.value) return
  editorError.value = ''
  if (!draft.title.trim()) { editorError.value = t('wakeRequired'); return }
  saving.value = true
  try {
    await feed.update(current, { title: draft.title.trim(), prompt: draft.prompt, notes: draft.notes, enabled: draft.enabled })
    if (editor.value === current) close()
  } catch (e) {
    if (editor.value === current) editorError.value = object(e).code === 'conflict' ? t('wakeConflict') : errorText(e)
    await feed.refresh().catch(() => undefined)
  } finally { saving.value = false }
}
async function toggle(endpoint: WakeEndpoint, enabled: boolean) {
  if (action.value || saving.value) return
  action.value = endpoint.id; delete actionErrors.value[endpoint.id]
  try { await feed.update(endpoint, { enabled }) }
  catch (e) { actionErrors.value[endpoint.id] = errorText(e); await feed.refresh().catch(() => undefined) }
  finally { action.value = '' }
}
async function remove(endpoint: WakeEndpoint) {
  if (action.value || saving.value) return
  action.value = endpoint.id; delete actionErrors.value[endpoint.id]
  try { await feed.remove(endpoint); if (editor.value?.id === endpoint.id) close(); deleting.value = '' }
  catch (e) { actionErrors.value[endpoint.id] = errorText(e) }
  finally { action.value = '' }
}
async function copyExample() {
  const current = editor.value
  if (!current || !deliveryURL.value || !sameBot.value) return
  try { await copy(wakeExample({ ...current, trigger_url: deliveryURL.value }, `event-${crypto.randomUUID()}`, t('wakeExampleText'))) }
  catch (e) { editorError.value = errorText(e) }
}
watch(() => app.activeAgentID, () => { deleting.value = '' })
</script>

<template>
  <SettingsSection :title="t('wakeEndpoints')">
    <div
      v-if="feed.error.value"
      class="error-box row wrap"
      role="alert"
    >
      <span class="grow">{{ feed.error.value }}</span>
      <Button @click="feed.refresh().catch(() => undefined)">
        {{ t('retry') }}
      </Button>
    </div>
    <p
      v-if="loading"
      class="settings-context-note"
      role="status"
    >
      {{ t('loading') }}
    </p>
    <div
      v-else-if="feed.ready.value && !endpoints.length"
      class="settings-context-note"
    >
      <p>{{ t('wakeEmpty') }}</p><p>{{ t('wakeEmptyHint') }}</p>
    </div>
    <div
      v-for="endpoint in endpoints"
      :key="endpoint.id"
      class="resource-row schedule-row"
    >
      <div class="schedule-summary">
        <strong>{{ endpoint.title }}</strong>
        <RouterLink
          class="schedule-session"
          :to="`/sessions/${endpoint.session_id}`"
        >
          {{ sessionName(endpoint.session_id) }}
        </RouterLink>
        <small v-if="!endpoint.enabled">{{ t('wakePaused') }}</small>
        <small v-else-if="wakeBlocked(endpoint, now.getTime())">{{ t('wakeBlocked', { time: formatTime(endpoint.blocked_until) }) }}</small>
        <small v-if="endpoint.last_received_at">{{ t('wakeLast', { time: formatTime(endpoint.last_received_at) }) }}</small>
        <small
          v-if="endpoint.last_error"
          class="text-danger"
        >{{ endpoint.last_error }}</small>
        <small
          v-if="actionErrors[endpoint.id] && deleting !== endpoint.id"
          class="text-danger"
          role="alert"
        >{{ actionErrors[endpoint.id] }}</small>
      </div>
      <div class="schedule-actions">
        <span
          v-if="waiting && action === endpoint.id"
          class="spinner"
          role="status"
          :aria-label="t('loading')"
        />
        <Switch
          :model-value="endpoint.enabled"
          :label="`${t(endpoint.enabled ? 'wakePause' : 'wakeResume')} · ${endpoint.title}`"
          :disabled="!!action || saving"
          @update:model-value="toggle(endpoint, $event)"
        />
        <Button
          size="icon"
          variant="ghost"
          :aria-label="`${t('wakeEdit')} · ${endpoint.title}`"
          :title="t('wakeEdit')"
          :disabled="!!action || saving"
          @click="begin(endpoint)"
        >
          <Pencil />
        </Button>
        <ConfirmPopover
          :open="deleting === endpoint.id"
          :title="t('wakeDeleteTitle', { name: endpoint.title })"
          :description="t('wakeDeleteHint')"
          :confirm-label="t('wakeDelete')"
          :cancel-label="t('cancel')"
          :busy="action === endpoint.id"
          :error="actionErrors[endpoint.id]"
          @update:open="deleting = $event ? endpoint.id : ''"
          @confirm="remove(endpoint)"
        >
          <template #trigger>
            <Button
              size="icon"
              variant="ghost"
              :aria-label="`${t('wakeDelete')} · ${endpoint.title}`"
              :title="t('wakeDelete')"
              :disabled="!!action || saving"
            >
              <Trash2 />
            </Button>
          </template>
        </ConfirmPopover>
      </div>
    </div>
  </SettingsSection>
  <form
    v-if="editor"
    ref="editorElement"
    class="settings-form stack"
    @submit.prevent="save"
  >
    <SettingsSection :title="`${t('wakeEdit')} · ${editor.title}`">
      <SettingsRow :label="t('wakeTitle')">
        <input
          v-model="draft.title"
          :aria-label="t('wakeTitle')"
          :disabled="saving"
          required
          maxlength="200"
        >
      </SettingsRow>
      <SettingsRow
        :label="t('wakeComputer')"
        :description="t('wakeLocalHint')"
      >
        <span>{{ computerName(editor.computer_id) }}</span>
      </SettingsRow>
      <SettingsRow :label="t('wakeTarget')">
        <RouterLink :to="`/sessions/${editor.session_id}`">
          {{ sessionName(editor.session_id) }}
        </RouterLink>
      </SettingsRow>
      <div class="schedule-prompt-field label">
        <label for="wake-prompt">{{ t('wakePrompt') }}</label><textarea
          id="wake-prompt"
          v-model="draft.prompt"
          :disabled="saving"
          rows="3"
        />
      </div>
      <div class="schedule-prompt-field label">
        <label for="wake-notes">{{ t('wakeNotes') }}</label><textarea
          id="wake-notes"
          v-model="draft.notes"
          :disabled="saving"
          rows="3"
        />
        <small class="muted">{{ t('wakeNotesHint') }}</small>
      </div>
      <SettingsRow :label="t('scheduleEnabled')">
        <Switch
          v-model="draft.enabled"
          :label="t('scheduleEnabled')"
          :disabled="saving || !sameBot"
        />
      </SettingsRow>
      <SettingsRow :label="t('wakeURL')">
        <CopyValue
          v-if="deliveryURL"
          :value="deliveryURL"
          :label="t('wakeURL')"
        /><span
          v-else
          class="muted"
        >{{ t('wakeURLMissing') }}</span>
      </SettingsRow>
      <SettingsRow :label="t('wakeExample')">
        <Button
          size="small"
          :disabled="!!action || !deliveryURL || !sameBot"
          @click="copyExample"
        >
          <Copy />{{ t(copied ? 'copied' : 'wakeExample') }}
        </Button>
      </SettingsRow>
      <p
        v-if="!sameBot || editorError"
        class="settings-feedback-error text-danger"
        role="alert"
      >
        {{ !sameBot ? t('wakeReadOnlyBot') : editorError }}
      </p>
      <div class="settings-submit-row">
        <div class="settings-actions">
          <Button
            :disabled="saving"
            @click="close"
          >
            {{ t('cancel') }}
          </Button><Button
            type="submit"
            variant="primary"
            :busy="saving"
            :disabled="!sameBot"
          >
            {{ t('save') }}
          </Button>
        </div>
      </div>
    </SettingsSection>
  </form>
</template>
