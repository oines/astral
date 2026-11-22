<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { cleanConversationArchive, resetStorageConversation, setConversationRetention } from '@astral/sdk'
import { Button, ConfirmPopover, Select, SettingsRow, SettingsSection, useDelayedPending } from '@astral/ui'
import { request } from '../../lib/api'
import { retentionNeedsConfirmation, storageSize, type StorageChat } from '../../lib/storage'
import { useStorageAction, type StorageActionScope } from '../../composables/useStorageAction'
import { useAppStore } from '../../stores/app'

const props = defineProps<StorageActionScope & { chat: StorageChat }>()
const emit = defineEmits<{ changed: [deleted?: boolean] }>()
const app = useAppStore()
const { t } = useI18n()
const { busy, locked, confirming, errors, confirm, run } = useStorageAction(props)
const nextDays = ref<number>()
const retentionControl = ref<HTMLElement>()
const retentionAnchor = computed(() => retentionControl.value?.querySelector<HTMLButtonElement>('[role="combobox"]') || undefined)
const savingRetention = useDelayedPending(() => busy.value === 'retention')
const options = computed(() => [{ value: '0', label: t('storageRetentionForever') }, ...[30, 90, 180].map(days => ({ value: String(days), label: t('storageRetentionDays', { days }) }))])
function choose(days: string) {
  const value = Number(days)
  if (value === props.chat.retention_days) return
  nextDays.value = value
  if (!retentionNeedsConfirmation(props.chat.retention_days, value)) void saveRetention()
}
function focusReturned() {
  if (nextDays.value === undefined || !retentionNeedsConfirmation(props.chat.retention_days, nextDays.value)) return
  void nextTick(() => confirm('retention', true))
}
function closeRetention(open: boolean) {
  confirm('retention', open)
  if (!open && !busy.value) nextDays.value = undefined
}
async function saveRetention() {
  await run('retention', async () => {
    const days = nextDays.value
    if (days === undefined) return
    const result = await request(setConversationRetention({ path: { id: props.chat.conversation_id }, body: { retention_days: days } }))
    emit('changed', result.removed_count > 0)
  })
  nextDays.value = undefined
}
function clearArchive() {
  return run('archive', async () => {
    const result = await request(cleanConversationArchive({ path: { id: props.chat.conversation_id } }))
    emit('changed', result.removed_count > 0)
  })
}
function reset() {
  return run('reset', async () => {
    const oldID = props.chat.session_id
    await request(resetStorageConversation({ path: { id: props.chat.conversation_id }, body: { session_id: oldID, revision: props.chat.binding_revision } }))
    app.forgetSession(oldID)
    emit('changed', true)
    void app.refresh().catch(() => {})
  })
}
</script>
<template>
  <SettingsSection :title="t('storageIMArchive')">
    <SettingsRow :label="t('storageArchiveSize')">
      <span>{{ storageSize(chat.im_bytes, chat.estimate_state, t) }}</span>
    </SettingsRow>
    <SettingsRow :label="t('storageRetention')">
      <span
        v-if="savingRetention"
        class="spinner"
        role="status"
        :aria-label="t('loading')"
      />
      <div ref="retentionControl">
        <Select
          :options="options"
          :label="t('storageRetention')"
          :model-value="String(chat.retention_days)"
          :disabled="locked"
          @update:model-value="choose"
          @close-auto-focus="focusReturned"
        />
      </div>
      <ConfirmPopover
        :disabled="!ready"
        :open="confirming === 'retention'"
        :anchor="retentionAnchor"
        :title="t('storageRetentionConfirm', { days: nextDays })"
        :description="t('storageRetentionConfirmHint', { days: nextDays })"
        :confirm-label="t('storageRetentionApply')"
        :cancel-label="t('cancel')"
        :busy="busy === 'retention'"
        @update:open="closeRetention"
        @confirm="saveRetention"
      />
      <small
        v-if="errors.retention && confirming !== 'retention'"
        class="text-danger"
        role="alert"
      >{{ errors.retention }}</small>
      <Button
        v-if="errors.retention && !ready && !busy"
        size="small"
        @click="reload()"
      >
        {{ t('storageRecheck') }}
      </Button>
    </SettingsRow>
    <SettingsRow
      :label="t('storageClearArchive')"
      :description="t('storageArchiveHint')"
    >
      <ConfirmPopover
        :disabled="!ready"
        :open="confirming === 'archive'"
        :title="t('storageArchiveConfirm')"
        :description="t('storageArchiveConfirmAll')"
        :confirm-label="t('storageClearArchive')"
        :cancel-label="t('cancel')"
        :busy="busy === 'archive'"
        @update:open="confirm('archive', $event)"
        @confirm="clearArchive"
      >
        <template #trigger>
          <Button
            size="small"
            variant="danger"
            :disabled="locked || chat.archive_count === 0"
          >
            {{ t('storageClear') }}
          </Button>
        </template>
      </ConfirmPopover>
      <small
        v-if="errors.archive"
        class="text-danger"
        role="alert"
      >{{ errors.archive }}</small>
      <Button
        v-if="errors.archive && !ready && !busy"
        size="small"
        @click="reload()"
      >
        {{ t('storageRecheck') }}
      </Button>
    </SettingsRow>
  </SettingsSection>
  <SettingsSection :title="t('storageCurrentSession')">
    <SettingsRow
      :label="t('storageSessionRecords')"
      :description="storageSize(chat.rollout_bytes, chat.estimate_state, t)"
    >
      <RouterLink
        v-if="chat.session_id"
        class="button button-secondary button-small"
        :to="`/sessions/${encodeURIComponent(chat.session_id)}`"
      >
        {{ t('storageOpenSession') }}
      </RouterLink>
    </SettingsRow>
    <SettingsRow
      :label="t('storageResetSession')"
      :description="t('storageResetDescription')"
    >
      <ConfirmPopover
        :disabled="!ready"
        :open="confirming === 'reset'"
        :title="t('storageResetConfirm', { name: chat.title })"
        :description="t('storageResetHint')"
        :confirm-label="t('storageResetSession')"
        :cancel-label="t('cancel')"
        :busy="busy === 'reset'"
        @update:open="confirm('reset', $event)"
        @confirm="reset"
      >
        <template #trigger>
          <Button
            variant="danger"
            size="small"
            :disabled="locked || !chat.binding_id || !chat.session_id"
          >
            {{ t('storageReset') }}
          </Button>
        </template>
      </ConfirmPopover>
      <small
        v-if="errors.reset"
        class="text-danger"
        role="alert"
      >{{ errors.reset }}</small>
      <Button
        v-if="errors.reset && !ready && !busy"
        size="small"
        @click="reload()"
      >
        {{ t('storageRecheck') }}
      </Button>
    </SettingsRow>
  </SettingsSection>
  <p
    v-if="chat.error"
    class="error-box"
    role="alert"
  >
    {{ chat.error }} <Button
      size="small"
      @click="reload()"
    >
      {{ t('retry') }}
    </Button>
  </p>
</template>
