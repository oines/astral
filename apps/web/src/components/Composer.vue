<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { useIntervalFn } from '@vueuse/core'
import { sessionContextQuery } from '@astral/sdk/colada'
import { clearPending, continueSession, decideApproval, inputReceipt, resolveTool, selectComputer, selectModel, stopSession, submitInput, uploadAttachment, type AstralInternalHarnessInputRequest } from '@astral/sdk'
import { ArrowUp, CircleStop, Paperclip, X, CircleAlert, Monitor, SlidersHorizontal } from '@astral/icons'
import { Button, Modal } from '@astral/ui'
import type { FileRef, SessionView } from '../lib/types'
import { errorText, object, request } from '../lib/api'
import { useAppStore } from '../stores/app'
import { useSurfaceStore } from '../stores/surface'
import OutboxReceipts from './OutboxReceipts.vue'
const props = defineProps<{ view: SessionView; live: boolean }>()
const emit = defineEmits<{ sent: [text: string]; compact: [] }>()
const app = useAppStore()
const surface = useSurfaceStore()
const { t } = useI18n()
const text = ref('')
const files = ref<Array<{ uri: string; file: FileRef }>>([])
const busy = ref(false)
const uploading = ref(false)
const error = ref('')
const pick = ref<HTMLInputElement>()
const mode = ref(app.data?.preferences.running_input || 'follow_up')
const attempt = ref<AstralInternalHarnessInputRequest>()
const running = computed(() => ['running', 'stopping', 'compacting'].includes(props.view.status))
const agent = computed(() => app.agents.find(a => a.id === props.view.agent_id))
const computers = computed(() => app.computers.filter(c => agent.value?.allowed_computers.includes(c.id) && !c.revoked))
const profile = computed(() => app.models.find(p => p.id === props.view.profile_id))
const values = computed(() => ({ ...profile.value?.recommendations, ...profile.value?.overrides }))
const caps = computed(() => object(values.value.reasoning))
const efforts = computed(() => Array.isArray(caps.value.efforts) ? caps.value.efforts.filter((x): x is string => typeof x === 'string') : [])
const context = useQuery(() => ({ ...sessionContextQuery({ path: { id: props.view.id } }), key: ['context', props.view.id, props.view.head_id, props.view.profile_id ?? '', props.view.run_id ?? ''], staleTime: 5000 }))
useIntervalFn(() => { if (running.value && document.visibilityState === 'visible') void context.refetch() }, 2000)
const ratio = computed(() => context.data.value ? Math.min(100, Math.round(context.data.value.estimated_input / context.data.value.context_window * 100)) : 0)
const resolution = ref('')
const note = ref('')
const controlsOpen = ref(false)
async function action(fn: () => Promise<unknown>) {
  error.value = ''
  try { await fn() } catch (e) { error.value = errorText(e) }
}
async function send() {
  if (busy.value || uploading.value || (!attempt.value && !text.value.trim() && !files.value.length)) return
  const current = attempt.value ?? { id: crypto.randomUUID(), text: text.value, files: files.value.map(f => f.uri), mode: mode.value }
  attempt.value = current
  busy.value = true
  error.value = ''
  try {
    try {
      await request(submitInput({ path: { id: props.view.id }, body: current }))
    } catch (e) {
      // A transport failure can happen after durable acceptance. Reconcile by
      // the same input ID before showing a retry that reuses the exact payload.
      const receipt = await request(inputReceipt({ path: { id: props.view.id, input: current.id } })).catch(() => undefined)
      if (!receipt || (!receipt.input_id && receipt.state !== 'applied')) throw e
    }
    text.value = ''
    files.value = []
    attempt.value = undefined
    emit('sent', current.text)
  } catch (e) { error.value = errorText(e) }
  finally { busy.value = false }
}
async function upload(event: Event) {
  const input = event.target as HTMLInputElement
  uploading.value = true
  error.value = ''
  try {
    for (const file of Array.from(input.files ?? [])) {
      const result = await request(uploadAttachment({ path: { id: props.view.id }, query: { name: file.name, computer_id: props.view.computer_id }, body: file }))
      files.value.push(result)
    }
  } catch (e) { error.value = `${t('attachmentError')}: ${errorText(e)}` }
  finally { uploading.value = false; input.value = '' }
}
function keyboard(event: KeyboardEvent) {
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing && !event.altKey) { event.preventDefault(); void send() }
}
async function changeModel(id: string) {
  await action(() => request(selectModel({ path: { id: props.view.id }, body: { profile_id: id } })))
}
async function changeReasoning(value: string) {
  const reasoning = value === 'default' ? undefined : value === 'off' ? { enabled: false } : value === 'on' ? { enabled: true } : { enabled: true, effort: value }
  await action(() => request(selectModel({ path: { id: props.view.id }, body: { profile_id: props.view.profile_id ?? '', reasoning } })))
}
async function resolve(outcome: 'succeeded' | 'failed') {
  await action(async () => {
    await request(resolveTool({ path: { id: props.view.id, execution: resolution.value }, body: { execution: { result: { outcome, is_error: outcome === 'failed', content: [{ type: 'text', text: note.value }] } } } }))
    resolution.value = ''
    note.value = ''
  })
}
watch(() => props.view.id, () => { text.value = ''; files.value = []; attempt.value = undefined; error.value = '' })
</script>
<template>
  <div class="composer-area">
    <OutboxReceipts :session-i-d="view.id" />
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
          @click="resolution = tool.invocation.execution_id; surface.open({ kind: 'json', title: tool.invocation.call.name, data: tool })"
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
        v-if="files.length"
        class="attachment-tray"
      >
        <span
          v-for="(file, i) in files"
          :key="file.file.id"
          class="file-chip"
        >{{ file.file.display_name }}<button
          v-if="!attempt"
          type="button"
          :aria-label="t('delete')"
          @click="files.splice(i, 1)"
        ><X /></button></span>
      </div>
      <textarea
        v-model="text"
        :placeholder="t('message')"
        :aria-label="t('message')"
        :readonly="Boolean(attempt)"
        rows="2"
        @keydown="keyboard"
      />
      <div class="composer-actions">
        <div class="row composer-left">
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
            :aria-label="t('attach')"
            :title="t('attach')"
            :disabled="!view.computer_id || uploading || Boolean(attempt)"
            @click="pick?.click()"
          >
            <Paperclip />
          </Button>
          <span
            v-if="uploading"
            class="muted small"
          >{{ t('upload') }}</span>
          <label
            v-else
            class="inline-select computer-select"
          ><Monitor /><select
            :value="view.computer_id || ''"
            :disabled="running"
            :aria-label="t('computer')"
            :title="t('computer')"
            @change="action(() => request(selectComputer({ path: { id: view.id }, body: { computer_id: ($event.target as HTMLSelectElement).value } })))"
          >
            <option
              value=""
              disabled
            >{{ t('missing') }}</option>
            <option
              v-if="view.computer_id && !computers.some(c => c.id === view.computer_id)"
              :value="view.computer_id"
            >{{ t('missing') }}</option>
            <option
              v-for="c in computers"
              :key="c.id"
              :value="c.id"
            >{{ c.name }}{{ c.online ? '' : ' · ' + t('offline') }}</option>
          </select></label>
          <Button
            variant="ghost"
            size="icon"
            :aria-label="t('model')"
            :title="t('model')"
            @click="controlsOpen = !controlsOpen"
          >
            <SlidersHorizontal />
          </Button>
          <button
            type="button"
            class="context-ring"
            :style="{ '--progress': `${ratio}%` }"
            :title="`${t('context')}: ${context.data.value?.estimated_input ?? '—'} / ${context.data.value?.context_window ?? '—'}`"
            :aria-label="t('context')"
            @click="controlsOpen = !controlsOpen"
          >
            <span />
          </button>
        </div>
        <div class="row">
          <select
            v-if="running"
            v-model="mode"
            class="send-mode"
            :aria-label="t('inputDefault')"
          >
            <option value="follow_up">
              {{ t('followUp') }}
            </option><option value="steer">
              {{ t('steer') }}
            </option>
          </select>
          <Button
            v-if="running"
            size="icon"
            :aria-label="t('stop')"
            :title="t('stop')"
            :disabled="view.status === 'stopping'"
            @click="action(() => request(stopSession({ path: { id: view.id } })))"
          >
            <CircleStop />
          </Button>
          <Button
            type="submit"
            variant="primary"
            size="icon"
            :aria-label="t(attempt ? 'retry' : 'send')"
            :title="t(attempt ? 'retry' : 'send')"
            :busy="busy"
            :disabled="uploading || (!text.trim() && !files.length) || !live"
          >
            <ArrowUp />
          </Button>
        </div>
      </div>
      <div
        v-if="controlsOpen"
        class="composer-controls"
      >
        <label class="label">{{ t('model') }}<select
          :value="view.profile_id"
          @change="changeModel(($event.target as HTMLSelectElement).value)"
        ><option
          v-if="!profile"
          :value="view.profile_id"
        >{{ t('missing') }}</option><option
          v-for="p in app.models"
          :key="p.id"
          :value="p.id"
        >{{ p.name }}</option></select></label>
        <label
          v-if="efforts.length || caps.toggle"
          class="label"
        >{{ t('reasoning') }}<select
          :value="view.reasoning?.enabled === false ? 'off' : view.reasoning?.effort || (view.reasoning?.enabled ? 'on' : 'default')"
          @change="changeReasoning(($event.target as HTMLSelectElement).value)"
        ><option value="default">{{ t('default') }}</option><option
          v-if="caps.disable"
          value="off"
        >{{ t('off') }}</option><option
          v-if="caps.toggle"
          value="on"
        >{{ t('on') }}</option><option
          v-for="effort in efforts"
          :key="effort"
          :value="effort"
        >{{ effort }}</option></select></label>
        <div class="row wrap muted small">
          <span>{{ t('context') }}: {{ context.data.value?.estimated_input?.toLocaleString() ?? '—' }} / {{ context.data.value?.context_window?.toLocaleString() ?? '—' }}</span><Button
            variant="ghost"
            size="small"
            :disabled="running"
            @click="emit('compact')"
          >
            {{ t('compact') }}
          </Button>
        </div>
      </div>
    </form>
    <div class="composer-footnote">
      {{ profile?.name || t('missing') }}<span v-if="running"> · {{ t(view.status) }}</span><span v-if="!live"> · {{ t('reconnecting') }}</span>
    </div>
    <Modal
      :open="Boolean(resolution)"
      :title="t('unknown')"
      :description="t('unknownHint')"
      @update:open="value => { if (!value) resolution = '' }"
    >
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
