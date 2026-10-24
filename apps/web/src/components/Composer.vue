<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { createSession, continueSession, decideApproval, inputReceipt, movePending, removePending, updatePending, selectComputer, selectModel, stopSession, submitInput, uploadAttachment } from '@astral/sdk'
import { ArrowUp, ArrowDown, Square, Plus, X, CircleAlert, Monitor, File, ChevronDown, Check, Bot, ListEnd, CornerDownRight, Trash2, Ellipsis, Pencil } from '@astral/icons'
import { Button, Popover } from '@astral/ui'
import type { SessionView } from '../lib/types'
import { errorText, fileURL, request } from '../lib/api'
import { prepareInputAttempt, sameInputContent } from '../lib/input-attempt'
import { useAppStore } from '../stores/app'
import ModelSelection from './ModelSelection.vue'
import type { ModelSelection as Selection } from '../lib/model-reasoning'
const props = defineProps<{ view: SessionView; live: boolean }>()
const emit = defineEmits<{ compact: []; started: [id: string] }>()
const app = useAppStore()
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
const textarea = ref<HTMLTextAreaElement>()
const preferredMode = ref(app.data?.preferences.running_input || 'follow_up')
const mode = computed({ get: () => view.value.binding_id ? 'steer' : preferredMode.value, set: value => { preferredMode.value = value } })
type Pending = SessionView['pending'][number]
const editing = ref<{ id: string; text: string; files: NonNullable<Pending['metadata']['files']> }>()
const composerText = computed({ get: () => editing.value?.text ?? text.value, set: value => { if (editing.value) editing.value.text = value; else text.value = value } })
const attachments = computed(() => editing.value?.files ?? files.value.map(item => item.file))
const queueBusy = ref(false)
const dragging = ref('')
const dropBefore = ref<string | undefined>()
const fileDrag = ref(false)
const sendLabel = computed(() => editing.value ? t('save') : !text.value.trim() && !files.value.length && canContinue.value ? t('continue') : retrying.value ? t('retry') : running.value ? t(mode.value === 'follow_up' ? 'queueMessage' : 'steerNow') : t('send'))
const inputContent = computed(() => ({ text: text.value, files: files.value.map(file => file.uri), mode: mode.value }))
const retrying = computed(() => attempt.value && sameInputContent(attempt.value, inputContent.value))
watch(inputContent, () => { if (!busy.value) error.value = '' })
watch(() => app.data?.preferences.running_input, value => { if (value) mode.value = value })
const running = computed(() => ['running', 'stopping', 'compacting'].includes(view.value.status))
const canContinue = computed(() => view.value.can_continue)
const agent = computed(() => app.agents.find(a => a.id === view.value.agent_id))
const computers = computed(() => app.computers.filter(c => agent.value?.allowed_computers.includes(c.id) && !c.revoked))
const contextUsage = computed(() => view.value.context_usage)
const ratio = computed(() => contextUsage.value?.context_window ? Math.min(100, Math.round(contextUsage.value.input_tokens / contextUsage.value.context_window * 100)) : undefined)
const computer = computed(() => computers.value.find(c => c.id === view.value.computer_id))
const modelBusy = ref(false)
async function action(fn: () => Promise<unknown>) {
  error.value = ''
  try { await fn() } catch (e) { error.value = errorText(e) }
}
async function ensureSession() {
  if (props.view.id) return
  const desired = { ...view.value }
  if (!draft.session) {
    const created = await request(createSession({ body: { agent_id: desired.agent_id, title: '' } }))
    draft.session = { ...created, head_id: '', rollout_id: '', event_sequence: 0, status: 'idle', can_continue: false, approvals: [], pending: [], unresolved: [] }
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
async function send(overrideMode?: string) {
  if (editing.value) {
    const edit = editing.value
    await queueAction(async () => {
      await request(updatePending({ path: { id: view.value.id, input: edit.id }, body: { text: edit.text } }))
      editing.value = undefined
    })
    return
  }
  if (!props.live || busy.value || uploading.value) return
  if (!text.value.trim() && !files.value.length) {
    if (canContinue.value) {
      busy.value = true
      try { await action(() => request(continueSession({ path: { id: view.value.id } }))) }
      finally { busy.value = false }
    }
    return
  }
  busy.value = true
  error.value = ''
  try {
    await ensureSession()
    const current = await prepareInputAttempt(attempt.value, { ...inputContent.value, mode: overrideMode || mode.value }, async id => {
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
  await uploadFiles(Array.from(input.files ?? []))
  input.value = ''
}
async function uploadFiles(selected: globalThis.File[]) {
  if (uploading.value || busy.value || editing.value || !view.value.computer_id) return
  uploading.value = true
  error.value = ''
  try {
    await ensureSession()
    for (const file of selected) {
      const result = await request(uploadAttachment({ path: { id: view.value.id }, query: { name: file.name, computer_id: view.value.computer_id }, body: file }))
      files.value.push(result)
    }
  } catch (e) { error.value = `${t('attachmentError')}: ${errorText(e)}` }
  finally { uploading.value = false }
}
function paste(event: ClipboardEvent) {
  const selected = Array.from(event.clipboardData?.files ?? [])
  if (!selected.length || editing.value) return
  event.preventDefault()
  void uploadFiles(selected)
}
function dragFiles(event: DragEvent) {
  if (!event.dataTransfer?.types.includes('Files') || editing.value) return
  event.preventDefault()
  event.dataTransfer.dropEffect = 'copy'
  fileDrag.value = true
}
function dropFiles(event: DragEvent) {
  fileDrag.value = false
  if (!event.dataTransfer?.files.length || editing.value) return
  event.preventDefault()
  void uploadFiles(Array.from(event.dataTransfer.files))
}
function keyboard(event: KeyboardEvent) {
  if (event.key === 'Escape' && editing.value) { editing.value = undefined; return }
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing && !event.altKey) { event.preventDefault(); void send(event.metaKey || event.ctrlKey ? 'steer' : undefined) }
}
async function queueAction(fn: () => Promise<unknown>) {
  if (queueBusy.value) return
  queueBusy.value = true
  try { await action(fn) } finally { queueBusy.value = false }
}
async function editPending(pending: Pending) {
  editing.value = { id: pending.id, text: pending.metadata.display_text, files: pending.metadata.files ?? [] }
  await nextTick()
  textarea.value?.focus()
}
async function deletePending(id: string) {
  await queueAction(async () => {
    await request(removePending({ path: { id: view.value.id, input: id } }))
    if (editing.value?.id === id) editing.value = undefined
  })
}
function moveInput(id: string, beforeID = '') {
  return queueAction(() => request(movePending({ path: { id: view.value.id, input: id }, body: { before_id: beforeID } })))
}
function dragStart(event: DragEvent, id: string) {
  dragging.value = id
  event.dataTransfer!.effectAllowed = 'move'
  event.dataTransfer!.setData('text/plain', id)
}
function dragOver(event: DragEvent, index: number) {
  if (!dragging.value) return
  event.preventDefault()
  event.dataTransfer!.dropEffect = 'move'
  const bounds = (event.currentTarget as HTMLElement).getBoundingClientRect()
  dropBefore.value = view.value.pending[index + (event.clientY > bounds.top + bounds.height / 2 ? 1 : 0)]?.id ?? ''
}
function dragEnd() { dragging.value = ''; dropBefore.value = undefined }
function dropInput(event: DragEvent) {
  if (!dragging.value || dropBefore.value === undefined) return
  event.preventDefault()
  void moveInput(dragging.value, dropBefore.value)
  dragEnd()
}
async function toggleQueue() {
  const next = mode.value === 'follow_up' ? 'steer' : 'follow_up'
  await action(async () => { await app.preferences({ running_input: next }); mode.value = next })
}
async function changeModel(selection: Selection) {
  if (modelBusy.value) return
  if (!props.view.id) { homeSelection.value = { ...homeSelection.value, ...selection }; return }
  modelBusy.value = true
  try { await action(async () => { await request(selectModel({ path: { id: view.value.id }, body: selection })); if (draft.session) Object.assign(draft.session, selection) }) }
  finally { modelBusy.value = false }
}
watch(() => props.view.agent_id, () => { homeSelection.value = {}; error.value = '' })
</script>
<template>
  <div class="composer-area">
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
    <p
      v-if="view.error"
      class="muted small"
      role="status"
    >
      {{ view.error }}
    </p>
    <div
      v-if="error"
      class="error-box"
    >
      {{ error }}
    </div>
    <div class="composer-stack">
      <div
        v-if="view.pending?.length"
        class="pending-inputs"
        :class="{ 'drop-at-end': dragging && dropBefore === '' }"
        role="list"
        :aria-label="t('queuedMessages')"
        @drop="dropInput"
      >
        <div
          v-for="(pending, index) in view.pending"
          :key="pending.id"
          class="pending-input"
          :class="{ dragging: dragging === pending.id, 'drop-before': dragging && dropBefore === pending.id, editing: editing?.id === pending.id }"
          role="listitem"
          :draggable="!queueBusy"
          @dragstart="dragStart($event, pending.id)"
          @dragover="dragOver($event, index)"
          @dragend="dragEnd"
        >
          <ListEnd
            class="queue-icon"
            :aria-label="t('dragToReorder')"
          />
          <span
            class="pending-text"
            :title="pending.metadata.display_text"
          >{{ pending.metadata.display_text || pending.metadata.files?.map(file => file.display_name).join(', ') }}</span>
          <button
            type="button"
            class="queue-steer"
            :disabled="queueBusy || pending.kind === 'steer'"
            :title="t('steer')"
            @click="queueAction(() => request(updatePending({ path: { id: view.id, input: pending.id }, body: { mode: 'steer' } })))"
          >
            <CornerDownRight /><span>{{ t(pending.kind === 'steer' ? 'applying' : 'steerNow') }}</span>
          </button>
          <button
            type="button"
            class="queue-action"
            :aria-label="t('delete')"
            :title="t('delete')"
            :disabled="queueBusy"
            @click="deletePending(pending.id)"
          >
            <Trash2 />
          </button>
          <Popover
            side="top"
            align="end"
            :label="t('common.more')"
          >
            <template #trigger>
              <button
                type="button"
                class="queue-action"
                :aria-label="t('common.more')"
                :disabled="queueBusy"
              >
                <Ellipsis />
              </button>
            </template>
            <template #default="{ close }">
              <button
                v-if="pending.metadata.source === 'web'"
                type="button"
                class="menu-item"
                @click="editPending(pending); close()"
              >
                <Pencil />{{ t('editMessage') }}
              </button>
              <button
                type="button"
                class="menu-item"
                :disabled="index === 0"
                @click="moveInput(pending.id, view.pending[index - 1]?.id); close()"
              >
                <ArrowUp />{{ t('moveUp') }}
              </button>
              <button
                type="button"
                class="menu-item"
                :disabled="index === view.pending.length - 1"
                @click="moveInput(pending.id, view.pending[index + 2]?.id); close()"
              >
                <ArrowDown />{{ t('moveDown') }}
              </button>
              <button
                v-if="!view.binding_id"
                type="button"
                class="menu-item"
                @click="toggleQueue(); close()"
              >
                <ListEnd />{{ t(mode === 'follow_up' ? 'disableQueue' : 'enableQueue') }}
              </button>
            </template>
          </Popover>
        </div>
      </div>
      <form
        class="composer"
        :class="{ 'file-drag': fileDrag }"
        @submit.prevent="send()"
        @dragover="dragFiles"
        @dragleave.self="fileDrag = false"
        @drop="dropFiles"
      >
        <div
          v-if="editing"
          class="composer-editing"
        >
          <Pencil />{{ t('editMessage') }}<button
            type="button"
            :aria-label="t('cancel')"
            @click="editing = undefined"
          >
            <X />
          </button>
        </div>
        <div
          v-if="attachments.length || uploading"
          class="attachment-tray"
        >
          <span
            v-for="(file, i) in attachments"
            :key="file.id"
            class="attachment-chip"
            :class="{ 'attachment-image': file.media_type.startsWith('image/') }"
          >
            <img
              v-if="file.media_type.startsWith('image/')"
              :src="fileURL(file.id, view.id)"
              :alt="file.display_name"
            >
            <template v-else><File /><span>{{ file.display_name }}</span></template>
            <button
              v-if="!busy && !editing"
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
          <Popover
            side="top"
            :label="t('common.more')"
          >
            <template #trigger>
              <button
                type="button"
                class="composer-add"
                :aria-label="t('attach')"
                :title="t('attach')"
              >
                <Plus />
              </button>
            </template>
            <template #default="{ close }">
              <button
                type="button"
                class="menu-item"
                :disabled="!view.computer_id || uploading || busy || Boolean(editing)"
                @click="pick?.click(); close()"
              >
                <Plus />{{ t('attach') }}
              </button>
              <button
                v-if="!view.binding_id"
                type="button"
                class="menu-item"
                @click="toggleQueue(); close()"
              >
                <ListEnd />{{ t(mode === 'follow_up' ? 'disableQueue' : 'enableQueue') }}
              </button>
            </template>
          </Popover>
          <textarea
            ref="textarea"
            v-model="composerText"
            :placeholder="t('message')"
            :aria-label="t('message')"
            :readonly="busy || queueBusy"
            rows="1"
            @keydown="keyboard"
            @paste="paste"
          />
          <div class="composer-submit-actions">
            <Button
              v-if="editing"
              variant="ghost"
              size="small"
              @click="editing = undefined"
            >
              {{ t('cancel') }}
            </Button>
            <Button
              v-if="running && !composerText.trim() && !attachments.length && !editing"
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
              v-else
              class="composer-send"
              type="submit"
              variant="primary"
              size="icon"
              :aria-label="sendLabel"
              :title="sendLabel"
              :busy="busy || queueBusy"
              :disabled="uploading || (!composerText.trim() && !attachments.length && (Boolean(editing) || !canContinue)) || !live"
            >
              <Check v-if="editing" /><ListEnd v-else-if="running && mode === 'follow_up'" /><ArrowUp v-else />
            </Button>
          </div>
        </div>
      </form>
    </div>
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
          <Bot /><span
            :title="view.id ? t('agentLocked') : agent?.name"
          >{{ agent?.name }}</span>
        </div>
      </div>
      <div class="composer-environment-right">
        <ModelSelection
          :model-value="view.profile_id || ''"
          :reasoning="view.reasoning"
          :busy="modelBusy"
          composer
          @select="changeModel"
        />
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
              :title="`${t('context')}: ${ratio === undefined ? '—' : `${ratio}%`}`"
              :aria-label="t('context')"
            >
              <span
                class="context-ring"
                :style="{ '--progress': `${ratio ?? 0}%` }"
              ><span /></span>
            </button>
          </template>
          <div class="context-menu">
            <strong>{{ t('context') }} <span>{{ ratio === undefined ? '—' : `${ratio}%` }}</span></strong>
            <div class="context-meter">
              <span :style="{ width: `${ratio ?? 0}%` }" />
            </div>
            <p class="muted">
              {{ contextUsage?.input_tokens.toLocaleString() ?? '—' }} / {{ contextUsage?.context_window?.toLocaleString() ?? '—' }} tokens
            </p>
            <p
              v-if="!contextUsage"
              class="muted small"
            >
              {{ t('contextNotReported') }}
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
      </div>
    </div>
    <div
      v-if="!live"
      class="composer-connection"
      role="status"
    >
      <span class="spinner" />{{ t('reconnecting') }}
    </div>
  </div>
</template>
