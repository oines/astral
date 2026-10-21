<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { useIntervalFn } from '@vueuse/core'
import { listProvidersQuery, sessionContextQuery } from '@astral/sdk/colada'
import { createSession, clearPending, continueSession, decideApproval, inputReceipt, resolveTool, selectComputer, selectModel, stopSession, submitInput, uploadAttachment } from '@astral/sdk'
import { ArrowUp, Square, Plus, X, CircleAlert, Monitor, File, ChevronDown, Check, User } from '@astral/icons'
import { Button, Modal, Popover, Select, ModelPicker } from '@astral/ui'
import type { SessionView } from '../lib/types'
import { errorText, object, request } from '../lib/api'
import { prepareInputAttempt, sameInputContent } from '../lib/input-attempt'
import { useAppStore } from '../stores/app'
import OutboxReceipts from './OutboxReceipts.vue'
const props = defineProps<{ view: SessionView; live: boolean }>()
const emit = defineEmits<{ sent: [text: string]; compact: []; started: [id: string] }>()
const app = useAppStore()
const providers = useQuery(listProvidersQuery())
const { t } = useI18n()
const draftKey = props.view.id || `new:${props.view.agent_id}`
if (!app.drafts[draftKey]) app.drafts[draftKey] = { text: '', files: [] }
const draft = app.drafts[draftKey]!
const text = computed({ get: () => draft.text, set: value => { draft.text = value } })
const files = computed({ get: () => draft.files, set: value => { draft.files = value } })
const attempt = computed({ get: () => draft.attempt, set: value => { draft.attempt = value } })
const homeSelection = computed({ get: () => draft.selection || {}, set: value => { draft.selection = value } })
const view = computed(() => props.view.id ? props.view : { ...(draft.session || props.view), ...homeSelection.value })
const busy = ref(false)
const uploading = ref(false)
const error = ref('')
const pick = ref<HTMLInputElement>()
const mode = ref(app.data?.preferences.running_input || 'follow_up')
const inputContent = computed(() => ({ text: text.value, files: files.value.map(file => file.uri), mode: mode.value }))
const retrying = computed(() => attempt.value && sameInputContent(attempt.value, inputContent.value))
watch(inputContent, () => { if (!busy.value) error.value = '' })
watch(() => app.data?.preferences.running_input, value => { if (value) mode.value = value })
const running = computed(() => ['running', 'stopping', 'compacting'].includes(view.value.status))
const agent = computed(() => app.agents.find(a => a.id === view.value.agent_id))
const computers = computed(() => app.computers.filter(c => agent.value?.allowed_computers.includes(c.id) && !c.revoked))
const profile = computed(() => app.models.find(p => p.id === view.value.profile_id))
const values = computed(() => ({ ...profile.value?.recommendations, ...profile.value?.overrides }))
const caps = computed(() => object(values.value.reasoning))
const efforts = computed(() => Array.isArray(caps.value.efforts) ? caps.value.efforts.filter((x): x is string => typeof x === 'string') : [])
const context = useQuery(() => ({ ...sessionContextQuery({ path: { id: view.value.id } }), key: ['context', view.value.id, view.value.head_id, view.value.profile_id ?? '', view.value.run_id ?? ''], staleTime: 5000, enabled: Boolean(view.value.id) }))
useIntervalFn(() => { if (running.value && document.visibilityState === 'visible') void context.refetch() }, 2000)
const ratio = computed(() => context.data.value ? Math.min(100, Math.round(context.data.value.estimated_input / context.data.value.context_window * 100)) : 0)
const resolution = ref('')
const note = ref('')
const computer = computed(() => computers.value.find(c => c.id === view.value.computer_id))
const modelBusy = ref(false)
const defaultReasoning = computed(() => object(object(values.value.defaults).reasoning))
const reasoningValue = computed(() => view.value.reasoning?.enabled === false ? 'off' : view.value.reasoning?.effort || (view.value.reasoning?.enabled ? 'on' : 'default'))
const defaultReasoningLabel = computed(() => defaultReasoning.value.enabled === false ? t('off') : typeof defaultReasoning.value.effort === 'string' ? t(defaultReasoning.value.effort) : defaultReasoning.value.enabled === true ? t('on') : t('default'))
const reasoningLabel = computed(() => reasoningValue.value === 'default' ? defaultReasoningLabel.value : t(reasoningValue.value))
const reasoningOptions = computed(() => !efforts.value.length && !caps.value.toggle && !caps.value.disable ? [] : [
  { value: 'default', label: defaultReasoningLabel.value === t('default') ? t('default') : `${t('default')} · ${defaultReasoningLabel.value}` },
  ...(caps.value.disable ? [{ value: 'off', label: t('off') }] : []),
  ...(caps.value.toggle ? [{ value: 'on', label: t('on') }] : []),
  ...efforts.value.map(value => ({ value, label: t(value) })),
])
const modelOptions = computed(() => app.availableModels.map(model => ({ value: model.id, label: model.name, group: model.provider_id, groupLabel: providers.data.value?.find(provider => provider.id === model.provider_id)?.name || t('providers'), keywords: model.model_id })))
async function action(fn: () => Promise<unknown>) {
  error.value = ''
  try { await fn() } catch (e) { error.value = errorText(e) }
}
async function ensureSession() {
  if (props.view.id) return
  const desired = { ...view.value }
  if (!draft.session) {
    const created = await request(createSession({ body: { agent_id: desired.agent_id, title: text.value.trim().slice(0, 64) || t('newSession') } }))
    draft.session = { ...created, head_id: '', rollout_id: '', event_sequence: 0, status: 'idle', approvals: [], pending: [], unresolved: [] }
  }
  const session = draft.session
  if (desired.computer_id && desired.computer_id !== session.computer_id) {
    await request(selectComputer({ path: { id: session.id }, body: { computer_id: desired.computer_id } }))
    draft.session.computer_id = desired.computer_id
  }
  if (desired.profile_id && (desired.profile_id !== session.profile_id || JSON.stringify(desired.reasoning) !== JSON.stringify(session.reasoning))) {
    await request(selectModel({ path: { id: session.id }, body: { profile_id: desired.profile_id, reasoning: desired.reasoning } }))
    draft.session.profile_id = desired.profile_id
    draft.session.reasoning = desired.reasoning
  }
}
async function changeComputer(id: string) {
  if (!props.view.id) { homeSelection.value = { ...homeSelection.value, computer_id: id }; return }
  await action(async () => { await request(selectComputer({ path: { id: view.value.id }, body: { computer_id: id } })); if (draft.session) draft.session.computer_id = id })
}
async function send() {
  if (!props.live || busy.value || uploading.value || (!text.value.trim() && !files.value.length)) return
  busy.value = true
  error.value = ''
  try {
    await ensureSession()
    const current = await prepareInputAttempt(attempt.value, inputContent.value, async id => {
      const result = await inputReceipt({ path: { id: view.value.id, input: id }, throwOnError: false })
      if (result.response?.status !== 404 && result.error) throw result.error
      return result.data
    })
    attempt.value = current
    try {
      await request(submitInput({ path: { id: view.value.id }, body: current }))
    } catch (e) {
      // A transport failure can happen after durable acceptance. Reconcile by
      // the same input ID before showing a retry that reuses the exact payload.
      const receipt = await request(inputReceipt({ path: { id: view.value.id, input: current.id } })).catch(() => undefined)
      if (!receipt || (!receipt.input_id && receipt.state !== 'applied')) throw e
    }
    text.value = ''
    files.value = []
    attempt.value = undefined
    emit('sent', current.text)
    if (!props.view.id) {
      const id = view.value.id
      draft.session = undefined
      emit('started', id)
    }
  } catch (e) { error.value = errorText(e) }
  finally { busy.value = false }
}
async function upload(event: Event) {
  const input = event.target as HTMLInputElement
  uploading.value = true
  error.value = ''
  try {
    await ensureSession()
    for (const file of Array.from(input.files ?? [])) {
      const result = await request(uploadAttachment({ path: { id: view.value.id }, query: { name: file.name, computer_id: view.value.computer_id }, body: file }))
      files.value.push(result)
    }
  } catch (e) { error.value = `${t('attachmentError')}: ${errorText(e)}` }
  finally { uploading.value = false; input.value = '' }
}
function keyboard(event: KeyboardEvent) {
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing && !event.altKey) { event.preventDefault(); void send() }
}
async function changeModel(id: string) {
  if (modelBusy.value || id === view.value.profile_id) return
  if (!props.view.id) { homeSelection.value = { ...homeSelection.value, profile_id: id, reasoning: undefined }; return }
  modelBusy.value = true
  try { await action(async () => { await request(selectModel({ path: { id: view.value.id }, body: { profile_id: id } })); if (draft.session) { draft.session.profile_id = id; draft.session.reasoning = undefined } }) }
  finally { modelBusy.value = false }
}
async function changeReasoning(value: string) {
  if (modelBusy.value || value === reasoningValue.value) return
  const reasoning = value === 'default' ? undefined : value === 'off' ? { enabled: false } : value === 'on' ? { enabled: true } : { enabled: true, effort: value }
  if (!props.view.id) { homeSelection.value = { ...homeSelection.value, reasoning }; return }
  modelBusy.value = true
  try { await action(async () => { await request(selectModel({ path: { id: view.value.id }, body: { profile_id: view.value.profile_id ?? '', reasoning } })); if (draft.session) draft.session.reasoning = reasoning }) }
  finally { modelBusy.value = false }
}
async function resolve(outcome: 'succeeded' | 'failed') {
  await action(async () => {
    await request(resolveTool({ path: { id: view.value.id, execution: resolution.value }, body: { execution: { result: { outcome, is_error: outcome === 'failed', content: [{ type: 'text', text: note.value }] } } } }))
    resolution.value = ''
    note.value = ''
  })
}
watch(() => props.view.agent_id, () => { homeSelection.value = {}; error.value = '' })
</script>
<template>
  <div class="composer-area">
    <OutboxReceipts
      v-if="view.id"
      :session-i-d="view.id"
    />
    <div
      v-if="view.approvals?.length"
      class="takeover stack"
    >
      <div
        v-for="approval in view.approvals"
        :key="approval.ID"
        class="stack"
      >
        <h3 class="row">
          <CircleAlert />{{ t('approval') }}
        </h3>
        <p>{{ approval.Request.prompt }}</p>
        <pre>{{ JSON.stringify(approval.Invocation.call, null, 2) }}</pre>
        <div class="row">
          <Button
            variant="primary"
            @click="action(() => request(decideApproval({ path: { id: view.id, approval: approval.ID }, body: { decision: { approved: true } } })))"
          >
            {{ t('allow') }}
          </Button>
          <Button @click="action(() => request(decideApproval({ path: { id: view.id, approval: approval.ID }, body: { decision: { approved: false } } })))">
            {{ t('deny') }}
          </Button>
        </div>
      </div>
    </div>
    <div
      v-if="view.unresolved?.length"
      class="takeover stack"
    >
      <h3 class="row">
        <CircleAlert />{{ t('unknown') }}
      </h3><p class="muted">
        {{ t('unknownHint') }}
      </p>
      <div
        v-for="tool in view.unresolved"
        :key="tool.invocation.execution_id"
        class="row between"
      >
        <code>{{ tool.invocation.call.name }}</code>
        <Button
          size="small"
          @click="resolution = tool.invocation.execution_id"
        >
          {{ t('inspect') }}
        </Button>
      </div>
    </div>
    <div
      v-if="view.error || (view.status === 'needs_attention' && view.last_run?.error)"
      class="error-box row between"
    >
      <span>{{ view.error || view.last_run?.error }}</span><Button
        size="small"
        @click="action(() => request(continueSession({ path: { id: view.id } })))"
      >
        {{ t('continue') }}
      </Button>
    </div>
    <div
      v-if="view.pending?.length"
      class="pending-inputs"
    >
      <div
        v-for="pending in view.pending"
        :key="pending.id"
        class="row"
      >
        <span class="badge">{{ t('waiting') }}</span><span>{{ pending.metadata.display_text || pending.kind }}</span>
      </div>
      <Button
        variant="ghost"
        size="small"
        @click="action(() => request(clearPending({ path: { id: view.id }, body: { kind: 'all' } })))"
      >
        {{ t('clearPending') }}
      </Button>
    </div>
    <div
      v-if="!running && !view.error && (view.pending?.length || view.last_run?.status === 'cancelled')"
      class="row"
    >
      <Button
        size="small"
        @click="action(() => request(continueSession({ path: { id: view.id } })))"
      >
        {{ t('continue') }}
      </Button>
    </div>
    <div
      v-if="error"
      class="error-box"
    >
      {{ error }}
    </div>
    <form
      class="composer"
      @submit.prevent="send"
    >
      <div
        v-if="files.length || uploading"
        class="attachment-tray"
      >
        <span
          v-for="(file, i) in files"
          :key="file.file.id"
          class="attachment-chip"
        >
          <File /><span>{{ file.file.display_name }}</span>
          <button
            v-if="!busy"
            type="button"
            :aria-label="t('delete')"
            @click="files.splice(i, 1)"
          ><X /></button>
        </span>
        <span
          v-if="uploading"
          class="attachment-chip muted"
        ><span class="spinner" />{{ t('upload') }}</span>
      </div>
      <div class="composer-input-row">
        <input
          ref="pick"
          type="file"
          hidden
          multiple
          @change="upload"
        >
        <Button
          variant="ghost"
          size="icon"
          class="composer-add"
          :aria-label="t('attach')"
          :title="t('attach')"
          :disabled="!view.computer_id || uploading || busy"
          @click="pick?.click()"
        >
          <Plus />
        </Button>
        <textarea
          v-model="text"
          :placeholder="t('message')"
          :aria-label="t('message')"
          :readonly="busy"
          rows="1"
          @keydown="keyboard"
        />
        <div class="composer-submit-actions">
          <Button
            v-if="running"
            class="composer-stop"
            variant="primary"
            size="icon"
            :aria-label="t('stop')"
            :title="t('stop')"
            :disabled="view.status === 'stopping'"
            @click="action(() => request(stopSession({ path: { id: view.id } })))"
          >
            <Square />
          </Button>
          <Button
            v-if="!running || text.trim() || files.length || attempt"
            class="composer-send"
            type="submit"
            variant="primary"
            size="icon"
            :aria-label="t(retrying ? 'retry' : 'send')"
            :title="t(retrying ? 'retry' : 'send')"
            :busy="busy"
            :disabled="uploading || (!text.trim() && !files.length) || !live"
          >
            <ArrowUp />
          </Button>
        </div>
      </div>
    </form>
    <div class="composer-environment">
      <div class="composer-environment-left">
        <Popover
          side="top"
          :label="t('computer')"
        >
          <template #trigger>
            <button
              type="button"
              class="composer-picker computer-picker"
              :disabled="running"
              :aria-label="t('computer')"
              :title="computer?.name"
            >
              <Monitor /><span>{{ computer?.name || t('missing') }}</span><ChevronDown />
            </button>
          </template>
          <template #default="{ close }">
            <div class="popover-label">
              {{ t('computer') }}
            </div>
            <button
              v-for="c in computers"
              :key="c.id"
              type="button"
              class="menu-item"
              @click="changeComputer(c.id); close()"
            >
              <Monitor /><span>{{ c.name }}<small
                v-if="!c.online"
                class="muted"
              > · {{ t('offline') }}</small></span><Check v-if="view.computer_id === c.id" />
            </button>
          </template>
        </Popover>
        <div class="composer-agent">
          <User /><span
            :title="view.id ? t('agentLocked') : agent?.name"
          >{{ agent?.name }}</span>
        </div>
      </div>
      <div class="composer-environment-right">
        <ModelPicker
          :model-value="view.profile_id || ''"
          :model-label="profile?.name || t('missing')"
          :models="modelOptions"
          :reasoning-value="reasoningValue"
          :reasoning-label="reasoningLabel"
          :reasoning-options="reasoningOptions"
          :labels="{ model: t('model'), reasoning: t('reasoning'), search: t('searchModels'), empty: t('noModelMatch') }"
          :busy="modelBusy"
          @update:model-value="changeModel"
          @update:reasoning-value="changeReasoning"
        >
          <button
            type="button"
            class="composer-picker model-picker"
            :aria-label="t('model')"
            :title="profile?.name"
          >
            <span>{{ profile?.name || t('missing') }}</span><ChevronDown />
          </button>
        </ModelPicker>
        <Popover
          v-if="view.id"
          side="top"
          align="end"
          :label="t('context')"
        >
          <template #trigger>
            <button
              type="button"
              class="context-button"
              :title="`${t('context')}: ${ratio}%`"
              :aria-label="t('context')"
            >
              <span
                class="context-ring"
                :style="{ '--progress': `${ratio}%` }"
              ><span /></span>
            </button>
          </template>
          <div class="context-menu">
            <strong>{{ t('context') }} <span>{{ ratio }}%</span></strong>
            <div class="context-meter">
              <span :style="{ width: `${ratio}%` }" />
            </div>
            <p class="muted">
              {{ context.data.value?.estimated_input?.toLocaleString() ?? '—' }} / {{ context.data.value?.context_window?.toLocaleString() ?? '—' }} tokens
            </p>
            <Button
              variant="secondary"
              size="small"
              :disabled="running"
              @click="emit('compact')"
            >
              {{ t('compact') }}
            </Button>
          </div>
        </Popover>
        <Select
          v-if="running"
          v-model="mode"
          class="send-mode"
          :label="t('inputDefault')"
          :options="[{ value: 'follow_up', label: t('followUp') }, { value: 'steer', label: t('steer') }]"
        />
      </div>
    </div>
    <div
      v-if="!live"
      class="composer-connection"
      role="status"
    >
      <span class="spinner" />{{ t('reconnecting') }}
    </div>
    <Modal
      :open="Boolean(resolution)"
      :title="t('unknown')"
      :description="t('unknownHint')"
      @update:open="value => { if (!value) resolution = '' }"
    >
      <details class="form-disclosure">
        <summary>{{ t('details') }}</summary><pre>{{ JSON.stringify(view.unresolved?.find(item => item.invocation.execution_id === resolution), null, 2) }}</pre>
      </details>
      <label class="label">{{ t('result') }}<textarea
        v-model="note"
        required
      /></label>
      <div class="row">
        <Button
          :disabled="!note.trim()"
          @click="resolve('succeeded')"
        >
          {{ t('confirmedSuccess') }}
        </Button><Button
          :disabled="!note.trim()"
          @click="resolve('failed')"
        >
          {{ t('confirmedFailure') }}
        </Button>
      </div>
    </Modal>
  </div>
</template>
