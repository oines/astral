<script setup lang="ts">
import { computed, nextTick, onActivated, onBeforeUnmount, onDeactivated, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { deleteSession } from '@astral/sdk'
import { Bot, ChevronDown, ChevronRight, Copy, Search } from '@astral/icons'
import { Button, ConfirmPopover, Select, SettingsRow, SettingsSection, useDelayedPending } from '@astral/ui'
import { useAppStore } from '../../stores/app'
import { useClipboard } from '../../composables/useClipboard'
import { bytes, errorText, request } from '../../lib/api'
import { platformLabel } from '../../lib/im-platforms'
import { readStorageTasks, writeStorageTasks, storageTaskRequest, type StorageTaskRequest } from '../../lib/storage-tasks'
import {
  cleanStorageApplication, cleanStorageChat, getStorage, getStorageCleanup, getStorageCleanupOperation,
  getStorageConversationOperation, reclaimStorage, refreshStorage, resetStorageChat,
  retryStorageCleanupOperation, retryStorageConversationOperation, storageShare, storageRequestRejected,
  type EstimateState, type StorageChat, type StorageCleanup, type StorageCleanupOperation,
  type StorageConversationOperation, type StorageSession, type StorageSnapshot,
} from '../../lib/storage'

const { t, locale } = useI18n()
const route = useRoute()
const app = useAppStore()
const { copy, copied } = useClipboard()
const snapshot = ref<StorageSnapshot>()
const loading = ref(false)
const loadError = ref('')
const waiting = useDelayedPending(loading)
const busy = ref('')
const errors = ref<Record<string, string>>({})
const confirming = ref('')
const selectedBot = computed(() => typeof route.query.bot === 'string' ? route.query.bot : '')
const selectedChat = computed(() => typeof route.query.chat === 'string' ? route.query.chat : '')
const bot = computed(() => snapshot.value?.agents.find(value => value.agent_id === selectedBot.value))
const chat = computed(() => bot.value?.chats.find(value => value.conversation_id === selectedChat.value))
const maximumBotBytes = computed(() => Math.max(1, ...(snapshot.value?.agents.map(value => value.logical_bytes) || [])))
const maximumChatBytes = computed(() => Math.max(1, ...(bot.value?.chats.map(value => value.logical_bytes) || [])))
const disk = computed(() => snapshot.value?.physical)
const diskAvailable = computed(() => !!disk.value?.disk_total_bytes && disk.value.disk_used_bytes !== undefined)
const diskAstral = computed(() => Math.min(disk.value?.total_bytes || 0, disk.value?.disk_used_bytes || 0))
const diskOther = computed(() => Math.max(0, (disk.value?.disk_used_bytes || 0) - diskAstral.value))
const search = ref('')
const sort = ref('largest')
const sessions = computed(() => (bot.value?.sessions || []).filter(value => value.title.toLocaleLowerCase().includes(search.value.toLocaleLowerCase())).toSorted((a, b) => sort.value === 'name' ? a.title.localeCompare(b.title) : sort.value === 'smallest' ? a.logical_bytes - b.logical_bytes : b.logical_bytes - a.logical_bytes))
const cleanupOpen = ref(false)
const cleanup = ref<StorageCleanup>()
const cleanupLoading = ref(false)
const cleanupWaiting = useDelayedPending(cleanupLoading)
const cleanupTokens = ref<string[]>([])
const taskMemory = readStorageTasks(localStorage)
cleanupOpen.value = !!taskMemory.application
const pendingRequests = ref(taskMemory.requests)
const unconfirmedRequests = computed(() => Object.fromEntries(Object.entries(pendingRequests.value).filter(([key]) => key !== busy.value)))
const cleanupOperation = ref<StorageCleanupOperation | undefined>(taskMemory.application)
const cleanupSelectionBytes = computed(() => (cleanup.value?.items || []).filter(value => cleanupTokens.value.includes(value.token)).reduce((total, value) => total + value.bytes, 0))
const cleanupActive = computed(() => cleanupOperation.value?.state === 'running' || !!pendingRequests.value.cleanup)
const operations = ref<Record<string, StorageConversationOperation>>(taskMemory.conversations)
watch([operations, cleanupOperation, pendingRequests], () => { writeStorageTasks(localStorage, { requests: pendingRequests.value, conversations: operations.value, application: cleanupOperation.value }) }, { deep: true, flush: 'sync' })
const archiveBefore = ref<Record<string, string>>({})
const retentionDraft = ref<Record<string, number>>({})
const retentionAnchor = ref<HTMLElement>()
const retentionControl = computed(() => retentionAnchor.value?.querySelector<HTMLElement>('[role="combobox"]') || undefined)
const chatOperation = computed(() => chat.value ? operations.value[chat.value.conversation_id] : undefined)
const chatActive = computed(() => chatOperation.value?.state === 'queued' || chatOperation.value?.state === 'running' || Object.values(pendingRequests.value).some(value => value.conversation_id === selectedChat.value))
const estimating = computed(() => snapshot.value?.estimate.state === 'running')
const estimateWaiting = useDelayedPending(estimating)
const reclaimState = computed(() => snapshot.value?.reclaim.state || 'idle')
const reclaiming = computed(() => ['queued', 'waiting', 'running'].includes(reclaimState.value))
const reclaimWaiting = useDelayedPending(reclaiming)
let active = false
let timer: ReturnType<typeof setTimeout> | undefined
let fetching = false

function href(botID = '', chatID = '') { return { path: '/settings/storage', query: { ...(botID ? { bot: botID } : {}), ...(chatID ? { chat: chatID } : {}) } } }
function size(value: number, state?: EstimateState) {
  if (state === 'complete') return t('storageEstimated', { size: bytes(value) })
  if (state === 'partial' && value > 0) return t('storageEstimatePartial', { size: bytes(value) })
  return t(state === 'failed' || state === 'partial' ? 'storageEstimateFailed' : 'storageEstimatePending')
}
function botState(value: NonNullable<typeof bot.value>): EstimateState {
  const states = [...value.chats, ...value.sessions].map(item => item.estimate_state)
  if (!states.length) return snapshot.value?.estimate.state === 'complete' ? 'complete' : 'pending'
  if (states.every(state => state === 'complete')) return 'complete'
  if (states.some(state => state === 'pending' || state === 'running')) return 'pending'
  return 'partial'
}
function account(value: StorageChat) { return [platformLabel(value.platform, locale.value), value.account_name || t('storageUnknownAccount')].join(' · ') }
function cleanupProtection(reason?: string) {
  if (reason === 'update_running') return t('storageCleanupProtectedUpdate')
  if (reason === 'update_pending') return t('storageCleanupProtectedPending')
  if (reason === 'update_recovery') return t('storageCleanupProtectedRecovery')
  return t('storageLatestRecovery')
}
function formatTime(value: string) { const date = new Date(value); return Number.isNaN(date.valueOf()) ? value : date.toLocaleString(locale.value) }
function isWorking(value?: StorageConversationOperation) { return value?.state === 'queued' || value?.state === 'running' }
function operationText(value: StorageConversationOperation) { return t(value.state === 'queued' ? 'storageTaskQueued' : value.state === 'running' ? 'storageTaskRunning' : value.state === 'failed' ? 'storageTaskFailed' : 'storageTaskComplete') }
function reclaimText() { return t(`storageReclaim${reclaimState.value.charAt(0).toUpperCase()}${reclaimState.value.slice(1)}`) }
function closeConfirmation(open: boolean, key: string) {
  confirming.value = open ? key : ''
  if (!open && key.startsWith('retention:')) delete retentionDraft.value[key.slice('retention:'.length)]
}
function retentionFocusReturned() {
  const value = chat.value
  if (!value) return
  const days = retentionDraft.value[value.conversation_id]
  if (days === undefined || days === 0 || confirming.value) return
  // The Select returns focus only after its exit animation has unmounted.
  void nextTick(() => { if (active && selectedChat.value === value.conversation_id) confirming.value = `retention:${value.conversation_id}` })
}
async function run(key: string, action: () => Promise<void>, close = true) {
  if (busy.value) return
  busy.value = key
  delete errors.value[key]
  try { await action(); if (close) confirming.value = '' }
  catch (error) {
    errors.value[key] = errorText(error)
    if (storageRequestRejected(error)) delete pendingRequests.value[key]
    if (storageRequestRejected(error) || key.startsWith('delete:') || key.startsWith('session:')) await load()
  }
  finally { busy.value = ''; schedulePoll() }
}
async function load() {
  if (fetching) return
  fetching = true
  loading.value = true
  try { snapshot.value = await getStorage(); loadError.value = '' }
  catch (error) { loadError.value = `${t('storageRefreshFailed')} ${errorText(error)}` }
  finally { fetching = false; loading.value = false }
}
async function loadCleanup() {
  if (cleanupLoading.value) return
  cleanupLoading.value = true
  try {
    cleanup.value = await getStorageCleanup()
    cleanupTokens.value = cleanup.value.items.filter(value => !value.protected && !value.retained_default).map(value => value.token)
    delete errors.value.cleanupLoad
  } catch (error) { errors.value.cleanupLoad = errorText(error) }
  finally { cleanupLoading.value = false }
}
async function toggleCleanup() { cleanupOpen.value = !cleanupOpen.value; if (cleanupOpen.value) await loadCleanup() }
function selectToken(token: string, checked: boolean) { cleanupTokens.value = checked ? [...new Set([...cleanupTokens.value, token])] : cleanupTokens.value.filter(value => value !== token) }
async function refresh() { await run('refresh', async () => { snapshot.value = await refreshStorage() }, false) }
async function stopEstimate() { await run('refresh', async () => { snapshot.value = await refreshStorage(true) }, false) }
async function retryReclaim() { await run('reclaim', async () => { snapshot.value = await reclaimStorage() }, false) }
async function cleanApplication() {
  await run('cleanup', async () => {
    const intent = storageTaskRequest(pendingRequests.value, 'cleanup', { kind: 'application', tokens: [...cleanupTokens.value] })
    cleanupOperation.value = await cleanStorageApplication(intent.operation_id, intent.tokens!)
    delete pendingRequests.value.cleanup
    if (cleanupOperation.value.state === 'complete') { await Promise.all([load(), loadCleanup()]) }
  })
}
async function retryApplication() {
  const value = cleanupOperation.value
  if (!value) return
  await run('cleanupRetry', async () => {
    cleanupOperation.value = await retryStorageCleanupOperation(value.operation_id)
    if (cleanupOperation.value.state === 'complete') await Promise.all([load(), loadCleanup()])
  }, false)
}
async function cleanArchive(value: StorageChat) {
  const date = archiveBefore.value[value.conversation_id]
  await run(`archive:${value.conversation_id}`, async () => {
    const before = date ? new Date(`${date}T00:00:00`) : new Date()
    if (before && Number.isNaN(before.valueOf())) throw new Error(t('storageInvalidDate'))
    const key = `archive:${value.conversation_id}`
    const intent = storageTaskRequest(pendingRequests.value, key, { kind: 'archive', conversation_id: value.conversation_id, before: before.toISOString() })
    operations.value[value.conversation_id] = await cleanStorageChat(value.conversation_id, intent.operation_id, intent.before)
    delete pendingRequests.value[key]
    if (operations.value[value.conversation_id]!.state === 'complete') await load()
  })
}
async function chooseRetention(value: StorageChat, days: string) {
  const next = Number(days)
  if (next === value.retention_days) return
  retentionDraft.value[value.conversation_id] = next
  if (next === 0) await saveRetention(value)

}
async function saveRetention(value: StorageChat) {
  const days = retentionDraft.value[value.conversation_id]
  if (days === undefined) return
  await run(`retention:${value.conversation_id}`, async () => {
    const key = `retention:${value.conversation_id}`
    const intent = storageTaskRequest(pendingRequests.value, key, { kind: 'retention', conversation_id: value.conversation_id, retention_days: days })
    const result = await cleanStorageChat(value.conversation_id, intent.operation_id, undefined, intent.retention_days)
    if (days !== 0 || result.state !== 'complete') operations.value[value.conversation_id] = result
    delete pendingRequests.value[key]
    if (result.state === 'failed') throw new Error(result.error || t('storageTaskFailed'))
    await load()
    delete retentionDraft.value[value.conversation_id]
  })
}
async function resetChat(value: StorageChat) {
  await run(`reset:${value.conversation_id}`, async () => {
    const key = `reset:${value.conversation_id}`
    const intent = storageTaskRequest(pendingRequests.value, key, { kind: 'reset', conversation_id: value.conversation_id, revision: value.binding_revision })
    operations.value[value.conversation_id] = await resetStorageChat(value.conversation_id, intent.operation_id, intent.revision!)
    delete pendingRequests.value[key]
    await reconcileOperation(operations.value[value.conversation_id]!)
  })
}
async function reconcileOperation(value: StorageConversationOperation) {
  if (value.state !== 'complete') return
  if (value.old_session_id) app.forgetSession(value.old_session_id)
  await Promise.allSettled([load(), app.refresh()])
}
async function retryChat(value: StorageConversationOperation) {
  await run(`retry:${value.conversation_id}`, async () => {
    const next = await retryStorageConversationOperation(value.id)
    operations.value[value.conversation_id] = next
    await reconcileOperation(next)
  }, false)
}
async function presentSession(value: StorageSession) {
  await run(`session:${value.session_id}`, async () => { await app.presentation(value.session_id, { archived: !value.archived }); await load() }, false)
}
async function deleteStoredSession(value: StorageSession) {
  await run(`delete:${value.session_id}`, async () => {
    await request(deleteSession({ path: { id: value.session_id }, query: { revision: value.revision } }))
    app.forgetSession(value.session_id)
    await Promise.allSettled([load(), app.refresh()])
  })
}
async function checkRequest(key: string, value: StorageTaskRequest) {
  if (value.kind === 'application') cleanupOperation.value = await getStorageCleanupOperation(value.operation_id)
  else {
    const next = await getStorageConversationOperation(value.operation_id)
    operations.value[value.conversation_id!] = next
    await reconcileOperation(next)
  }
  delete pendingRequests.value[key]
  delete errors.value[key]
}
async function retryRequest(key: string, value: StorageTaskRequest) {
  await run(key, async () => {
    try { await checkRequest(key, value) }
    catch {
      // Explicit retry reuses the original payload and identity.
      if (value.kind === 'application') cleanupOperation.value = await cleanStorageApplication(value.operation_id, value.tokens!)
      else if (value.kind === 'reset') operations.value[value.conversation_id!] = await resetStorageChat(value.conversation_id!, value.operation_id, value.revision!)
      else operations.value[value.conversation_id!] = await cleanStorageChat(value.conversation_id!, value.operation_id, value.before, value.retention_days)
      delete pendingRequests.value[key]
    }
    const next = value.conversation_id ? operations.value[value.conversation_id] : undefined
    if (next) await reconcileOperation(next)
    if (value.kind === 'application' && cleanupOperation.value?.state === 'complete') await loadCleanup()
    await load()
  })
}
function schedulePoll() {
  clearTimeout(timer)
  if (!active) return
  const pending = estimating.value || reclaiming.value || cleanupActive.value || Object.values(operations.value).some(isWorking) || Object.keys(pendingRequests.value).length > 0
  if (pending) timer = setTimeout(() => { void poll() }, 1500)
}
let polling = false
async function poll() {
  if (!active || polling) return
  polling = true
  try {
    const results = await Promise.allSettled([
      ...Object.entries(pendingRequests.value).map(async ([key, value]) => { try { await checkRequest(key, value) } catch (error) { errors.value[key] = errorText(error) } }),
      ...Object.values(operations.value).filter(isWorking).map(async value => {
        const next = await getStorageConversationOperation(value.id)
        operations.value[value.conversation_id] = next
        if (next.state === 'complete') await reconcileOperation(next)
      }),
      ...(cleanupOperation.value?.state === 'running' ? [(async () => {
        cleanupOperation.value = await getStorageCleanupOperation(cleanupOperation.value!.operation_id)
        if (cleanupOperation.value.state === 'complete') await loadCleanup()
      })()] : []),
    ])
    const failure = results.find(result => result.status === 'rejected')
    if (failure?.status === 'rejected') errors.value.poll = errorText(failure.reason)
    else delete errors.value.poll
    await load()
  } finally { polling = false; schedulePoll() }
}
onActivated(async () => { active = true; await Promise.allSettled([poll(), ...(cleanupOpen.value ? [loadCleanup()] : [])]) })
onDeactivated(() => { active = false; clearTimeout(timer) })
onBeforeUnmount(() => { active = false; clearTimeout(timer) })
</script>
<template>
  <div class="settings-sections storage-settings">
    <div class="settings-title storage-heading">
      <div>
        <nav
          v-if="selectedBot"
          class="storage-breadcrumbs"
          :aria-label="t('storageOverview')"
        >
          <RouterLink :to="href()">
            {{ t('storageOverview') }}
          </RouterLink><ChevronRight />
          <RouterLink
            v-if="selectedChat"
            :to="href(selectedBot)"
          >
            {{ bot?.name || t('agent') }}
          </RouterLink>
          <span v-else>{{ bot?.name || t('agent') }}</span><ChevronRight v-if="selectedChat" /><span v-if="selectedChat">{{ chat?.title || t('storageChats') }}</span>
        </nav>
        <h1>{{ selectedChat ? chat?.title || t('storageChats') : selectedBot ? bot?.name || t('agent') : t('storageOverview') }}</h1>
        <p
          v-if="chat"
          class="muted small"
        >
          {{ account(chat) }}
        </p>
      </div>
      <Button
        v-if="!selectedChat"
        size="small"
        :busy="busy === 'refresh'"
        :disabled="!!busy"
        @click="estimating ? stopEstimate() : refresh()"
      >
        {{ t(estimating ? 'storageCancelEstimate' : 'storageRecalculate') }}
      </Button>
    </div>
    <p
      v-if="loadError"
      class="error-box storage-feedback"
      role="alert"
    >
      {{ loadError }} <button
        class="text-button"
        type="button"
        @click="load"
      >
        {{ t('retry') }}
      </button>
    </p>
    <p
      v-if="waiting && !snapshot"
      class="storage-feedback muted"
      role="status"
    >
      <span
        class="spinner"
        aria-hidden="true"
      />{{ t('loading') }}
    </p>
    <p
      v-if="errors.refresh"
      class="error-box storage-feedback"
      role="alert"
    >
      {{ errors.refresh }}
    </p>
    <p
      v-if="errors.poll"
      class="error-box storage-feedback"
      role="alert"
    >
      {{ errors.poll }} <button
        type="button"
        class="text-button"
        @click="poll"
      >
        {{ t('retry') }}
      </button>
    </p>
    <div
      v-if="snapshot && (reclaimWaiting || reclaimState === 'failed' || reclaimState === 'complete')"
      class="storage-task"
      :class="{ 'storage-task-error': reclaimState === 'failed' }"
      :role="reclaimState === 'failed' ? 'alert' : 'status'"
    >
      <span
        v-if="reclaimWaiting"
        class="spinner"
        aria-hidden="true"
      />
      <div>
        <strong>{{ reclaimText() }}</strong><p v-if="reclaimState !== 'complete'">
          {{ snapshot.reclaim.error || t('storageReclaimHint') }}
        </p><p v-if="errors.reclaim">
          {{ errors.reclaim }}
        </p>
      </div>
      <Button
        v-if="reclaimState === 'failed'"
        size="small"
        :busy="busy === 'reclaim'"
        @click="retryReclaim"
      >
        {{ t('storageRetryReclaim') }}
      </Button>
    </div>
    <div
      v-for="(value, key) in unconfirmedRequests"
      :key="key"
      class="storage-task storage-task-error"
      role="alert"
    >
      <div>
        <strong>{{ t('storageTaskUnconfirmed') }}</strong><p>{{ t('storageTaskUnconfirmedHint') }}</p><p v-if="errors[key]">
          {{ errors[key] }}
        </p>
      </div><Button
        size="small"
        :busy="busy === key"
        @click="retryRequest(String(key), value)"
      >
        {{ t('storageCheckAndRetry') }}
      </Button>
    </div>
    <template v-if="snapshot && !selectedBot">
      <SettingsSection :title="t('storageDisk')">
        <div class="storage-disk">
          <template v-if="diskAvailable && disk">
            <div class="storage-disk-heading">
              <strong>{{ bytes(disk.disk_used_bytes!) }} {{ t('storageDiskUsed') }}</strong><span class="muted">{{ t('storageDiskTotal', { size: bytes(disk.disk_total_bytes!) }) }}</span>
            </div>
            <div
              class="storage-disk-bar"
              role="img"
              :aria-label="`${t('storageDiskUsed')} ${bytes(disk.disk_used_bytes!)}; ${t('storageDiskFree')} ${bytes(disk.available_bytes)}`"
            >
              <span
                class="storage-segment-astral"
                :style="{ width: storageShare(diskAstral, disk.disk_total_bytes!) }"
              /><span
                class="storage-segment-other"
                :style="{ width: storageShare(diskOther, disk.disk_total_bytes!) }"
              />
            </div>
            <dl class="storage-legend">
              <div><dt><i class="storage-segment-astral" />{{ t('storageAstralPhysical') }}</dt><dd>{{ bytes(disk.total_bytes) }}</dd></div><div><dt><i class="storage-segment-other" />{{ t('storageDiskOther') }}</dt><dd>{{ bytes(diskOther) }}</dd></div><div><dt><i class="storage-segment-free" />{{ t('storageDiskFree') }}</dt><dd>{{ bytes(disk.available_bytes) }}</dd></div>
            </dl>
          </template>
          <p
            v-else
            class="muted"
          >
            {{ t('storageDiskUnavailable') }}
          </p>
          <p
            v-if="disk?.errors?.length"
            class="storage-warning"
            role="alert"
          >
            {{ t('storagePhysicalWarnings') }} {{ disk.errors.join(' ') }}
          </p>
        </div>
      </SettingsSection>
      <SettingsSection
        :title="t('storageBots')"
        :description="t('storageBotEstimate')"
      >
        <RouterLink
          v-for="value in snapshot.agents"
          :key="value.agent_id"
          :to="href(value.agent_id)"
          class="resource-row storage-object-row"
        >
          <Bot /><span><strong>{{ value.name }}</strong><span
            class="storage-size-track"
            aria-hidden="true"
          ><span :style="{ width: storageShare(value.logical_bytes, maximumBotBytes) }" /></span></span><span class="storage-object-size">{{ size(value.logical_bytes, botState(value)) }}</span><ChevronRight />
        </RouterLink>
        <p
          v-if="!snapshot.agents.length"
          class="empty"
        >
          {{ t('storageNoBots') }}
        </p>
      </SettingsSection>
      <SettingsSection class="storage-cleanup-section">
        <button
          type="button"
          class="resource-row"
          :aria-expanded="cleanupOpen"
          @click="toggleCleanup"
        >
          <span><strong>{{ t('storageApplicationCleanup') }}</strong><small>{{ t('storageApplicationCleanupHint') }}</small></span><ChevronDown v-if="cleanupOpen" /><ChevronRight v-else />
        </button>
        <template v-if="cleanupOpen">
          <p
            v-if="cleanupWaiting"
            class="storage-feedback muted"
            role="status"
          >
            <span
              class="spinner"
              aria-hidden="true"
            />{{ t('loading') }}
          </p>
          <p
            v-if="errors.cleanupLoad || cleanup?.error"
            class="storage-feedback error-box"
            role="alert"
          >
            {{ errors.cleanupLoad || cleanup?.error }} <button
              type="button"
              class="text-button"
              @click="loadCleanup"
            >
              {{ t('retry') }}
            </button>
          </p>
          <label
            v-for="item in cleanup?.items || []"
            :key="item.token"
            class="resource-row storage-cleanup-item"
          ><input
            type="checkbox"
            :checked="cleanupTokens.includes(item.token)"
            :disabled="item.protected || cleanupActive || !!busy"
            @change="selectToken(item.token, ($event.target as HTMLInputElement).checked)"
          ><span><strong>{{ item.name }}</strong><small>{{ t(`storageCleanupKinds.${item.kind}`) }}<template v-if="item.protected || item.retained_default"> · {{ cleanupProtection(item.reason) }}</template></small></span><span class="storage-object-size">{{ bytes(item.bytes) }}</span></label>
          <p
            v-if="cleanup && !cleanup.items.length && !errors.cleanupLoad"
            class="empty"
          >
            {{ t('storageCleanupEmpty') }}
          </p>
          <div
            v-if="cleanup?.items.some(item => !item.protected)"
            class="storage-cleanup-footer"
          >
            <span class="muted small">{{ t('storageCleanupSelected', { size: bytes(cleanupSelectionBytes) }) }}</span><ConfirmPopover
              :open="confirming === 'cleanup'"
              :title="t('storageCleanupConfirm')"
              :description="t('storageCleanupHint')"
              :confirm-label="t('storageCleanupAction')"
              :cancel-label="t('cancel')"
              :busy="busy === 'cleanup'"
              :error="errors.cleanup"
              @update:open="closeConfirmation($event, 'cleanup')"
              @confirm="cleanApplication"
            >
              <template #trigger>
                <Button
                  size="small"
                  :disabled="!cleanupTokens.length || cleanupActive || !!busy"
                >
                  {{ t('storageCleanupAction') }}
                </Button>
              </template>
            </ConfirmPopover>
          </div>
          <div
            v-if="cleanupOperation"
            class="storage-task"
            :class="{ 'storage-task-error': cleanupOperation.state === 'failed' }"
            :role="cleanupOperation.state === 'failed' ? 'alert' : 'status'"
          >
            <div>
              <strong>{{ t(cleanupOperation.state === 'running' ? 'storageTaskRunning' : cleanupOperation.state === 'failed' ? 'storageTaskFailed' : 'storageTaskComplete') }}</strong><p v-if="cleanupOperation.error || errors.cleanupRetry">
                {{ cleanupOperation.error || errors.cleanupRetry }}
              </p>
            </div><Button
              v-if="cleanupOperation.state === 'failed'"
              size="small"
              :busy="busy === 'cleanupRetry'"
              @click="retryApplication"
            >
              {{ t('retry') }}
            </Button>
          </div>
        </template>
      </SettingsSection>
      <SettingsSection>
        <SettingsRow
          :label="t('storageDirectory')"
          :description="snapshot.directory"
        >
          <Button
            variant="ghost"
            size="icon"
            :aria-label="t(copied ? 'copied' : 'copyDirectory')"
            @click="copy(snapshot.directory)"
          >
            <Copy />
          </Button>
        </SettingsRow>
      </SettingsSection>
    </template>
    <template v-else-if="snapshot && bot && !selectedChat">
      <SettingsSection
        :title="t('storageChats')"
        :description="t('storageChatsHint')"
      >
        <RouterLink
          v-for="value in bot.chats"
          :key="value.conversation_id"
          :to="href(bot.agent_id, value.conversation_id)"
          class="resource-row storage-object-row"
        >
          <span><strong>{{ value.title }}</strong><small>{{ account(value) }}</small><span
            class="storage-size-track"
            aria-hidden="true"
          ><span :style="{ width: storageShare(value.logical_bytes, maximumChatBytes) }" /></span></span><span class="storage-object-size">{{ size(value.logical_bytes, value.estimate_state) }}</span><ChevronRight />
        </RouterLink>
        <p
          v-if="!bot.chats.length"
          class="empty"
        >
          {{ t('storageNoChats') }}
        </p>
      </SettingsSection>
      <SettingsSection
        :title="t('storageOtherSessions')"
        :description="t('storageOtherSessionsHint')"
      >
        <div
          v-if="bot.sessions.length"
          class="storage-session-toolbar"
        >
          <div class="search-input">
            <Search /><input
              v-model="search"
              :placeholder="t('storageSearchSessions')"
              :aria-label="t('storageSearchSessions')"
            >
          </div><Select
            v-model="sort"
            :label="t('storageSort')"
            :options="[{ value: 'largest', label: t('storageSortLargest') }, { value: 'smallest', label: t('storageSortSmallest') }, { value: 'name', label: t('storageSortName') }]"
          />
        </div>
        <div
          v-for="value in sessions"
          :key="value.session_id"
          class="resource-row storage-session-row"
        >
          <div class="storage-session-copy">
            <RouterLink :to="`/sessions/${value.session_id}`">
              <strong>{{ value.title }}</strong>
            </RouterLink><small>{{ t(value.archived ? 'storageSessionArchived' : 'storageSessionActive') }} · {{ size(value.logical_bytes, value.estimate_state) }}</small><small v-if="value.error">{{ value.error }}</small><p
              v-if="errors[`session:${value.session_id}`]"
              class="field-error"
              role="alert"
            >
              {{ errors[`session:${value.session_id}`] }}
            </p>
          </div><div class="row storage-session-actions">
            <Button
              variant="ghost"
              size="small"
              :busy="busy === `session:${value.session_id}`"
              :disabled="!!busy"
              @click="presentSession(value)"
            >
              {{ t(value.archived ? 'restoreConversation' : 'archive') }}
            </Button><ConfirmPopover
              v-if="value.archived"
              :open="confirming === `delete:${value.session_id}`"
              :title="t('deleteConversationTitle', { name: value.title })"
              :description="t('deleteConversationHint')"
              :confirm-label="t('permanentlyDelete')"
              :cancel-label="t('cancel')"
              :busy="busy === `delete:${value.session_id}`"
              :error="errors[`delete:${value.session_id}`]"
              @update:open="closeConfirmation($event, `delete:${value.session_id}`)"
              @confirm="deleteStoredSession(value)"
            >
              <template #trigger>
                <Button
                  variant="ghost"
                  size="small"
                  class="danger-text"
                  :disabled="!!busy"
                >
                  {{ t('permanentlyDelete') }}
                </Button>
              </template>
            </ConfirmPopover>
          </div>
        </div>
        <p
          v-if="!sessions.length"
          class="empty"
        >
          {{ t(search ? 'noResults' : 'storageNoOtherSessions') }}
        </p>
      </SettingsSection>
    </template>
    <template v-else-if="snapshot && bot && chat">
      <div
        v-if="chatOperation"
        class="storage-task"
        :class="{ 'storage-task-error': chatOperation.state === 'failed' }"
        :role="chatOperation.state === 'failed' ? 'alert' : 'status'"
      >
        <div>
          <strong>{{ operationText(chatOperation) }}</strong><p v-if="chatOperation.removed_count !== undefined && chatOperation.state === 'complete'">
            {{ t('storageArchiveRemoved', { count: chatOperation.removed_count }) }}
          </p><p v-if="chatOperation.error || errors[`retry:${chat.conversation_id}`]">
            {{ chatOperation.error || errors[`retry:${chat.conversation_id}`] }}
          </p>
        </div><Button
          v-if="chatOperation.state === 'failed'"
          size="small"
          :busy="busy === `retry:${chat.conversation_id}`"
          @click="retryChat(chatOperation)"
        >
          {{ t('retry') }}
        </Button>
      </div>
      <SettingsSection
        :title="t('storageIMArchive')"
        :description="t('storageArchiveDescription')"
      >
        <SettingsRow :label="t('storageIMArchive')">
          <span>{{ size(chat.im_bytes, chat.estimate_state) }}</span>
        </SettingsRow>
        <SettingsRow
          :label="t('storageRetention')"
          :description="t('storageRetentionHint')"
        >
          <div
            ref="retentionAnchor"
            class="storage-retention-control"
          >
            <Select
              :model-value="String(chat.retention_days || 0)"
              :label="t('storageRetention')"
              :disabled="chatActive || !!busy"
              :options="[{ value: '0', label: t('storageRetentionForever') }, ...[30, 90, 180].map(days => ({ value: String(days), label: t('storageRetentionDays', { days }) }))]"
              @update:model-value="chooseRetention(chat, $event)"
              @close-auto-focus="retentionFocusReturned"
            />
          </div><ConfirmPopover
            :open="confirming === `retention:${chat.conversation_id}`"
            :anchor="retentionControl"
            :title="t('storageRetentionConfirm')"
            :description="t('storageRetentionConfirmHint', { days: retentionDraft[chat.conversation_id] })"
            :confirm-label="t('storageRetentionApply')"
            :cancel-label="t('cancel')"
            :busy="busy === `retention:${chat.conversation_id}`"
            :error="errors[`retention:${chat.conversation_id}`]"
            @update:open="closeConfirmation($event, `retention:${chat.conversation_id}`)"
            @confirm="saveRetention(chat)"
          />
        </SettingsRow>
        <p
          v-if="errors[`retention:${chat.conversation_id}`] && confirming !== `retention:${chat.conversation_id}`"
          class="field-error storage-feedback"
          role="alert"
        >
          {{ errors[`retention:${chat.conversation_id}`] }}
        </p>
        <SettingsRow :label="t('storageBeforeDate')">
          <input
            v-model="archiveBefore[chat.conversation_id]"
            type="date"
            :aria-label="t('storageBeforeDate')"
            :disabled="chatActive || !!busy"
          >
        </SettingsRow>
        <SettingsRow :label="archiveBefore[chat.conversation_id] || t('storageAllDates')">
          <ConfirmPopover
            :open="confirming === `archive:${chat.conversation_id}`"
            :title="t('storageArchiveConfirm')"
            :description="archiveBefore[chat.conversation_id] ? t('storageArchiveConfirmBefore', { date: archiveBefore[chat.conversation_id] }) : t('storageArchiveConfirmAll')"
            :confirm-label="t('storageClearArchive')"
            :cancel-label="t('cancel')"
            :busy="busy === `archive:${chat.conversation_id}`"
            :error="errors[`archive:${chat.conversation_id}`]"
            @update:open="closeConfirmation($event, `archive:${chat.conversation_id}`)"
            @confirm="cleanArchive(chat)"
          >
            <template #trigger>
              <Button
                size="small"
                :disabled="chatActive || !!busy"
              >
                {{ t('storageClearArchive') }}
              </Button>
            </template>
          </ConfirmPopover>
        </SettingsRow>
      </SettingsSection>
      <SettingsSection
        :title="t('storageSessionRecords')"
        :description="t('storageSessionDescription')"
      >
        <SettingsRow :label="t('storageSessionRecords')">
          <span>{{ size(chat.rollout_bytes, chat.estimate_state) }}</span><RouterLink
            v-if="chat.session_id"
            :to="`/sessions/${chat.session_id}`"
            class="button button-ghost button-small"
          >
            {{ t('storageOpenSession') }}
          </RouterLink>
        </SettingsRow>
        <SettingsRow :label="t('storageResetSession')">
          <ConfirmPopover
            :open="confirming === `reset:${chat.conversation_id}`"
            :title="t('storageResetConfirm', { name: chat.title })"
            :description="t('storageResetHint')"
            :confirm-label="t('storageResetSession')"
            :cancel-label="t('cancel')"
            :busy="busy === `reset:${chat.conversation_id}`"
            :error="errors[`reset:${chat.conversation_id}`]"
            @update:open="closeConfirmation($event, `reset:${chat.conversation_id}`)"
            @confirm="resetChat(chat)"
          >
            <template #trigger>
              <Button
                size="small"
                :disabled="!chat.binding_id || chatActive || !!busy"
              >
                {{ t('storageResetSession') }}
              </Button>
            </template>
          </ConfirmPopover>
        </SettingsRow>
      </SettingsSection>
      <p
        v-if="chat.error"
        class="storage-warning"
        role="alert"
      >
        {{ chat.error }}
      </p>
    </template>
    <p
      v-else-if="snapshot && selectedBot"
      class="error-box"
      role="alert"
    >
      {{ t(bot ? 'storageChatMissing' : 'storageBotMissing') }}
    </p>
    <div
      v-if="snapshot && !selectedChat"
      class="storage-estimate-status small muted"
      role="status"
    >
      <span
        v-if="estimateWaiting"
        class="spinner"
        aria-hidden="true"
      /><span v-if="estimateWaiting">{{ t('storageEstimating') }}</span><span v-else-if="snapshot.estimate.updated_at">{{ t('storageEstimateUpdated', { time: formatTime(snapshot.estimate.updated_at) }) }}</span><p
        v-if="snapshot.estimate.error"
        class="storage-warning"
      >
        {{ snapshot.estimate.error }}
      </p>
    </div>
  </div>
</template>
<style scoped>
.storage-heading { align-items: flex-start; }
.storage-heading > div { min-width: 0; }
.storage-heading h1 { overflow-wrap: anywhere; }
.storage-heading p { margin-top: 6px; }
.storage-breadcrumbs { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-bottom: 14px; font-size: 12px; color: var(--muted); }
.storage-breadcrumbs svg { width: 12px; height: 12px; }
.storage-breadcrumbs a { color: var(--muted); }
.storage-disk { padding: var(--settings-row-padding) 0; display: flex; flex-direction: column; gap: 16px; }
.storage-disk-heading { display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 8px; font-size: 13px; }
.storage-disk-heading strong { font-weight: 500; }
.storage-disk-bar { height: 20px; display: flex; background: var(--surface); border: 1px solid var(--panel-line); border-radius: 5px; overflow: hidden; }
.storage-segment-astral { background: var(--ink); }
.storage-segment-other { background: var(--subtle); }
.storage-segment-free { background: var(--surface); border: 1px solid var(--panel-line); }
.storage-legend { display: flex; flex-wrap: wrap; gap: 12px 24px; margin: 0; font-size: 12px; }
.storage-legend > div { display: flex; flex-direction: column; gap: 5px; }
.storage-legend dt { display: flex; align-items: center; gap: 6px; color: var(--muted); }
.storage-legend dd { margin: 0 0 0 14px; }
.storage-legend i { width: 8px; height: 8px; border-radius: 2px; }
.storage-object-row > svg:first-child { flex-shrink: 0; width: 18px; margin-left: 0; }
.storage-object-row > svg:last-child { flex-shrink: 0; }
.resource-row > .storage-object-size { flex: 0 0 auto; max-width: 44%; font-size: 12px; color: var(--muted); text-align: right; overflow-wrap: anywhere; }
.storage-size-track { display: block; height: 4px; width: 100%; max-width: 240px; margin-top: 5px; overflow: hidden; border-radius: 2px; background: var(--surface); }
.storage-size-track > span { display: block; height: 100%; background: var(--subtle); }
.storage-cleanup-item { cursor: pointer; }
.storage-cleanup-item:has(input:disabled) { cursor: default; }
.storage-cleanup-item input { flex-shrink: 0; }
.storage-cleanup-footer { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 12px; padding: var(--settings-row-padding) var(--settings-group-padding); }
.storage-session-toolbar { display: flex; flex-wrap: wrap; gap: 10px; padding: var(--settings-row-padding) var(--settings-group-padding); }
.storage-session-toolbar .search-input { flex: 1; min-width: 180px; }
.storage-session-toolbar :deep(.select-trigger) { width: 140px; }
.storage-retention-control { width: 100%; }
.storage-retention-control :deep(.select-trigger) { width: 100%; }
.storage-session-copy { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 3px; overflow-wrap: anywhere; }
.storage-session-actions { flex-shrink: 0; }
.storage-session-copy a { color: var(--ink); }
.storage-task { display: flex; align-items: center; gap: 12px; color: var(--muted); font-size: 12px; }
.storage-task > div { flex: 1; min-width: 0; }
.storage-task strong { font-size: 13px; font-weight: 500; color: var(--ink); }
.storage-task p { margin-top: 4px; overflow-wrap: anywhere; }
.storage-task-error, .storage-task-error strong, .storage-warning { color: var(--danger); }
.storage-cleanup-section .storage-task { padding: var(--settings-row-padding) var(--settings-group-padding); }
.storage-feedback { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.storage-cleanup-section .storage-feedback { margin: var(--settings-row-padding) var(--settings-group-padding); }
.storage-estimate-status { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.storage-warning { font-size: 12px; overflow-wrap: anywhere; }
.storage-settings :deep(.preference-copy p) { overflow-wrap: anywhere; }
@container settings (max-width: 480px) {
  .storage-session-row { flex-wrap: wrap; }
  .storage-session-copy { flex-basis: 100%; }
  .storage-session-actions { margin-left: auto; }
  .storage-heading { flex-wrap: wrap; }
  .storage-heading > .button { margin-left: auto; }
}
</style>
