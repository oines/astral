<script setup lang="ts">
import { computed, onActivated, onDeactivated, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { manageStorage, refreshStorage } from '@astral/sdk'
import { ArrowLeft } from '@astral/icons'
import { Button, useDelayedPending } from '@astral/ui'
import { errorText, request } from '../../lib/api'
import type { StorageSnapshot } from '../../lib/storage'
import { createStorageLoader } from '../../lib/storage-refresh'
import { platformLabel } from '../../lib/im-platforms'
import StorageDisk from './StorageDisk.vue'
import StorageSources from './StorageSources.vue'
import StorageChatDetails from './StorageChatDetails.vue'

const { t, locale } = useI18n()
const route = useRoute()
const snapshot = ref<StorageSnapshot>()
const loading = ref(false)
const error = ref('')
const waiting = useDelayedPending(loading)
const recentDeletion = ref(false)
const filter = ref('')
const search = ref('')
const chatID = computed(() => typeof route.query.chat === 'string' ? route.query.chat : '')
const bot = computed(() => snapshot.value?.agents.find(item => item.chats.some(chat => chat.conversation_id === chatID.value)))
const chat = computed(() => bot.value?.chats.find(item => item.conversation_id === chatID.value))
const load = createStorageLoader(async fresh => {
  loading.value = true
  error.value = ''
  reclaimReadError.value = ''
  try {
    snapshot.value = fresh ? await request(refreshStorage()) : await request(manageStorage())
    if (!snapshot.value.agents.some(item => item.agent_id === filter.value)) filter.value = ''
    if (['idle', 'complete'].includes(snapshot.value.reclaim.state)) recentDeletion.value = false
  } catch (cause) { error.value = errorText(cause) }
  finally { loading.value = false }
})
function reload() { return load(true) }
const ready = computed(() => !!snapshot.value && !error.value && !loading.value)
function changed(recordsDeleted = false) {
  if (recordsDeleted) recentDeletion.value = true
  return load(true)
}
const visible = ref(false)
const reclaimReadError = ref('')
watch(() => {
  const reclaim = snapshot.value?.reclaim
  if (!visible.value || loading.value || error.value || !reclaim || !['queued', 'waiting', 'running'].includes(reclaim.state)) return ''
  return reclaim.requested_at || 'pending'
}, async (batch, _, cleanup) => {
  if (!batch) return
  const controller = new AbortController()
  cleanup(() => controller.abort())
  reclaimReadError.value = ''
  try {
    const latest = await request(manageStorage({ query: { wait_for_reclaim: true }, signal: controller.signal }))
    if (controller.signal.aborted || !snapshot.value) return
    snapshot.value.physical = latest.physical
    snapshot.value.reclaim = latest.reclaim
    if (['complete', 'idle'].includes(latest.reclaim.state)) recentDeletion.value = false
  } catch (cause) { if (!controller.signal.aborted) reclaimReadError.value = errorText(cause) }
}, { flush: 'sync' })
onDeactivated(() => { visible.value = false })
onActivated(async () => {
  visible.value = true
  if (!snapshot.value) await load()
  if (visible.value) await load(true)
})
</script>
<template>
  <div class="settings-sections">
    <header class="storage-title">
      <RouterLink
        v-if="chatID"
        class="storage-back"
        to="/settings/storage"
      >
        <ArrowLeft />{{ t('storageOverview') }}
      </RouterLink>
      <div class="settings-title">
        <h1>{{ chatID ? chat?.title || t('storageChat') : t('storageOverview') }}</h1>
      </div>
      <p
        v-if="chat && bot"
        class="small muted"
      >
        {{ platformLabel(chat.platform, locale) }} · {{ bot.name }}
      </p>
    </header>
    <div
      v-if="error"
      class="row error-box"
      role="alert"
    >
      <span>{{ t('storageRefreshFailed') }} {{ error }}</span>
      <Button
        size="small"
        :busy="loading"
        @click="load(true)"
      >
        {{ t('retry') }}
      </Button>
    </div>
    <span
      v-else-if="waiting && !snapshot"
      class="spinner"
      role="status"
      :aria-label="t('loading')"
    />
    <template v-if="snapshot && !chatID">
      <StorageDisk
        :snapshot="snapshot"
        :ready="ready"
        :reload="reload"
        :recent-deletion="recentDeletion"
        :read-error="reclaimReadError"
        @changed="changed()"
      />
      <StorageSources
        v-model:filter="filter"
        v-model:search="search"
        :bots="snapshot.agents"
        :ready="ready"
        :reload="reload"
        :estimate-error="snapshot.estimate.error"
        @changed="changed"
      />
    </template>
    <StorageChatDetails
      v-else-if="chat"
      :key="chat.conversation_id + ':' + chat.session_id"
      :chat="chat"
      :ready="ready"
      :reload="reload"
      @changed="changed"
    />
    <p
      v-else-if="snapshot && chatID"
      role="alert"
      class="error-box"
    >
      {{ t('storageChatMissing') }}
    </p>
  </div>
</template>
<style scoped>
.storage-title { display: grid; gap: 5px; }
.storage-title h1 { margin: 0; }
.storage-back { display: inline-flex; align-items: center; justify-self: start; gap: 5px; margin-bottom: 8px; font-size: 13px; color: var(--muted); }
.storage-back svg { width: 15px; height: 15px; }
</style>
