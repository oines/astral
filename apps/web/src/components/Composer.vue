<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { createSession, continueSession, decideApproval, inputReceipt, movePending, removePending, updatePending, selectComputer, selectModel, stopSession, submitInput, uploadAttachment } from '@astral/sdk'
import { ArrowUp, ArrowDown, Square, Plus, X, CircleAlert, Monitor, File, ChevronDown, Check, Bot, ListEnd, CornerDownRight, Trash2, Ellipsis, Pencil, Settings2 } from '@astral/icons'
import { Button, Popover, useDelayedPending } from '@astral/ui'
import type { SessionView } from '../lib/types'
import { clone, errorText, fileURL, jsonEqual, request } from '../lib/api'
import { prepareInputAttempt, sameInputContent } from '../lib/input-attempt'
import { useAppStore } from '../stores/app'
import ModelSelection from './ModelSelection.vue'
import ComposerContext from './ComposerContext.vue'
import type { ModelSelection as Selection } from '../lib/model-reasoning'
import { modelUnavailableLabel } from '../lib/model-availability'
import { useImagePreviewStore } from '../stores/image-preview'
import { useSurfaceStore } from '../stores/surface'
import type { FileRef } from '../lib/types'
import { revealOverflowLabel } from '../lib/overflow-label'
const props = defineProps<{ view: SessionView; live: boolean }>()
const showConnection = useDelayedPending(() => !props.live)
const emit = defineEmits<{ compact: []; started: [id: string] }>()
const app = useAppStore()
const images = useImagePreviewStore()
const surface = useSurfaceStore()
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
const mode = ref(app.data?.preferences.running_input || 'follow_up')
type Pending = SessionView['pending'][number]
const editing = ref<{ id: string; text: string; files: NonNullable<Pending['metadata']['files']> }>()
const composerText = computed({ get: () => editing.value?.text ?? text.value, set: value => { if (editing.value) editing.value.text = value; else text.value = value } })
const attachments = computed(() => editing.value?.files ?? files.value.map(item => item.file))
const queueBusy = ref(false)
const dragging = ref('')
const dropBefore = ref<string | undefined>()
const fileDrag = ref(false)
const showContext = ref(false)
const contextTrigger = ref<HTMLButtonElement>()
const contextInMenu = ref(false)
function openActions(open: boolean) {
  showContext.value = false
  // The menu is portaled outside the chat container. Follow the visibility
  // selected by its container query without maintaining a second breakpoint.
  if (open) contextInMenu.value = !contextTrigger.value?.getClientRects().length
}
const sendLabel = computed(() => editing.value ? t('save') : !text.value.trim() && !files.value.length && canContinue.value ? t('continue') : retrying.value ? t('retry') : running.value ? t(mode.value === 'follow_up' ? 'queueMessage' : 'steerNow') : t('send'))
const inputContent = computed(() => ({ text: text.value, files: files.value.map(file => file.uri), mode: mode.value }))
const retrying = computed(() => !busy.value && attempt.value && sameInputContent(attempt.value, inputContent.value))
watch(inputContent, () => { if (!busy.value) error.value = '' })
watch(() => app.data?.preferences.running_input, value => { if (value) mode.value = value })
const running = computed(() => ['running', 'stopping', 'compacting'].includes(view.value.status))
const selectedProfile = computed(() => app.models.find(model => model.id === view.value.profile_id))
const modelAvailable = computed(() => !!selectedProfile.value?.available && (!props.view.id || view.value.model_available))
const unavailableReason = computed(() => selectedProfile.value?.unavailable_reason || view.value.model_unavailable_reason || 'model_missing')
const canContinue = computed(() => view.value.can_continue)
const continuing = computed(() => !editing.value && !composerText.value.trim() && !attachments.value.length && canContinue.value)
const agent = computed(() => app.agents.find(a => a.id === view.value.agent_id))
const computers = computed(() => app.computers.filter(c => agent.value?.allowed_computers.includes(c.id) && !c.revoked))
const contextUsage = computed(() => view.value.context_usage)
const ratio = computed(() => contextUsage.value?.context_window ? Math.min(100, Math.round(contextUsage.value.input_tokens / contextUsage.value.context_window * 100)) : undefined)
const computer = computed(() => computers.value.find(c => c.id === view.value.computer_id))
const modelBusy = ref(false)
function previewAttachment(file: FileRef) {
  if (file.media_type.startsWith('image/')) {
    const imageFiles = attachments.value.filter(item => item.media_type.startsWith('image/'))
    images.show(imageFiles.map(item => ({ src: fileURL(item.id, view.value.id), downloadUrl: fileURL(item.id, view.value.id, true), name: item.display_name })), imageFiles.findIndex(item => item.id === file.id))
  } else surface.open({ kind: 'file', id: file.id, sessionID: view.value.id, ref: file })
}
async function action(fn: () => Promise<unknown>) {
  error.value = ''
  try { await fn() } catch (e) { error.value = errorText(e) }
}
async function ensureSession() {
  if (props.view.id) return
  const desired = { ...view.value }
  const explicitModel = homeSelection.value.profile_id ? clone({ profile_id: homeSelection.value.profile_id, reasoning: homeSelection.value.reasoning }) : undefined
  if (!draft.session) {
    draft.confirmedModelSelection = undefined
    const created = await request(createSession({ body: { agent_id: desired.agent_id, title: '' } }))
    draft.session = created
  }
  const session = draft.session
  if (desired.computer_id && desired.computer_id !== session.computer_id) {
    await request(selectComputer({ path: { id: session.id }, body: { computer_id: desired.computer_id } }))
    draft.session.computer_id = desired.computer_id
  }
  const confirmed = draft.confirmedModelSelection
  // A matching active fallback model does not confirm the user's preference.
  // Remember the explicit write so input retries do not reset later fallback.
  if (explicitModel && (confirmed?.sessionID !== session.id || confirmed.profile_id !== explicitModel.profile_id || !jsonEqual(confirmed.reasoning, explicitModel.reasoning))) {
    await request(selectModel({ path: { id: session.id }, body: explicitModel }))
    draft.session.profile_id = explicitModel.profile_id
    draft.session.reasoning = explicitModel.reasoning
    draft.confirmedModelSelection = { sessionID: session.id, ...explicitModel }
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
  if (!modelAvailable.value) return
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
      draft.confirmedModelSelection = undefined
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
  // Safari can end composition before the confirming Enter keydown while
  // retaining keyCode 229. Leave that key to the input method.
  if (event.isComposing || event.keyCode === 229) return
  if (event.key === 'Escape' && editing.value) { editing.value = undefined; return }
  if (event.key === 'Enter' && !event.shiftKey && !event.altKey) { event.preventDefault(); void send(event.metaKey || event.ctrlKey ? 'steer' : undefined) }
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
      v-if="!modelAvailable"
      class="muted small"
      role="status"
    >
      {{ modelUnavailableLabel(unavailableReason, t) }}{{ t('selectAvailableModelHint') }}
    </p>
    <p
      v-else-if="view.error"
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
        class="composer-form"
        :class="{ 'file-drag': fileDrag }"
        @submit.prevent="send()"
        @dragover="dragFiles"
        @dragleave.self="fileDrag = false"
        @drop="dropFiles"
      >
        <div class="composer">
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
              <button
                v-if="file.media_type.startsWith('image/')"
                type="button"
                class="attachment-preview-image"
                :aria-label="t('previewImage', { name: file.display_name })"
                @click="previewAttachment(file)"
              >
                <img
                  :src="fileURL(file.id, view.id)"
                  :alt="file.display_name"
                >
              </button>
              <button
                v-else
                type="button"
                class="attachment-preview-file"
                @click="previewAttachment(file)"
              ><File /><span>{{ file.display_name }}</span></button>
              <button
                v-if="!busy && !editing"
                type="button"
                class="attachment-remove"
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
              :label="showContext ? t('context') : t('common.more')"
              @update:open="openActions"
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
                <ComposerContext
                  v-if="showContext"
                  autofocus
                  :usage="contextUsage"
                  :ratio="ratio"
                  :disabled="running || !modelAvailable"
                  @compact="emit('compact'); close()"
                />
                <template v-else>
                  <button
                    type="button"
                    class="menu-item"
                    :disabled="!view.computer_id || uploading || busy || Boolean(editing)"
                    @click="pick?.click(); close()"
                  >
                    <Plus />{{ t('attach') }}
                  </button>
                  <button
                    type="button"
                    class="menu-item"
                    @click="toggleQueue(); close()"
                  >
                    <ListEnd />{{ t(mode === 'follow_up' ? 'disableQueue' : 'enableQueue') }}
                  </button>
                  <button
                    v-if="view.id && contextInMenu"
                    type="button"
                    class="menu-item"
                    @click="showContext = true"
                  >
                    <span
                      class="context-ring"
                      :style="{ '--progress': `${ratio ?? 0}%` }"
                    ><span /></span>{{ t('context') }}
                  </button>
                </template>
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
                class="composer-cancel"
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
                :disabled="uploading || (!editing && !modelAvailable && !continuing) || (!composerText.trim() && !attachments.length && (Boolean(editing) || !canContinue)) || !live"
              >
                <Check v-if="editing" /><ListEnd v-else-if="running && mode === 'follow_up'" /><ArrowUp v-else />
              </Button>
            </div>
          </div>
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
                  :aria-label="t('computer')"
                  @pointerenter="revealOverflowLabel($event, 'span')"
                  @focus="revealOverflowLabel($event, 'span')"
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
                  :disabled="running"
                  @click="changeComputer(c.id); close()"
                >
                  <Monitor /><span>{{ c.name }}<small
                    v-if="!c.online"
                    class="muted"
                  > · {{ t('offline') }}</small></span><Check v-if="view.computer_id === c.id" />
                </button>
                <hr class="separator">
                <RouterLink
                  class="menu-item"
                  :to="{ path: '/settings/agents', query: { agent: view.agent_id, tab: 'computers' } }"
                  @click="close()"
                >
                  <Settings2 />{{ t('manageComputers') }}
                </RouterLink>
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
              :unavailable-reason="view.model_unavailable_reason"
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
                  ref="contextTrigger"
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
              <ComposerContext
                :usage="contextUsage"
                :ratio="ratio"
                :disabled="running || !modelAvailable"
                @compact="emit('compact')"
              />
            </Popover>
          </div>
        </div>
      </form>
    </div>
    <div
      v-if="showConnection"
      class="composer-connection"
      role="status"
    >
      <span class="spinner" />{{ t('reconnecting') }}
    </div>
  </div>
</template>
