<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { deleteSession } from '@astral/sdk'
import { ChevronRight } from '@astral/icons'
import { Button, ConfirmPopover, Select, SettingsSection } from '@astral/ui'
import { platformLabel } from '../../lib/im-platforms'
import { request } from '../../lib/api'
import { storageSources, storageSize, storageShare, type StorageBot, type StorageSession, type StorageSource } from '../../lib/storage'
import { useAppStore } from '../../stores/app'
import { useStorageAction, type StorageActionScope } from '../../composables/useStorageAction'

const props = defineProps<StorageActionScope & { bots: StorageBot[]; estimateError?: string }>()
const emit = defineEmits<{ changed: [deleted?: boolean] }>()
const filter = defineModel<string>('filter', { required: true })
const search = defineModel<string>('search', { required: true })
const { t, locale } = useI18n()
const app = useAppStore()
const { busy, locked, confirming, errors, confirm, run } = useStorageAction(props)
const rows = computed(() => storageSources(props.bots, filter.value, search.value))
const maximum = computed(() => rows.value[0]?.value.logical_bytes || 0)
const options = computed(() => [{ value: '', label: t('storageAllBots') }, ...props.bots.map(bot => ({ value: bot.agent_id, label: bot.name }))])
function id(row: StorageSource) { return row.kind === 'chat' ? row.value.conversation_id : row.value.session_id }
function subtitle(row: StorageSource) {
  const kind = row.kind === 'chat' ? platformLabel(row.value.platform, locale.value) : t(row.value.archived ? 'storageSessionArchived' : 'storageSession')
  return filter.value ? kind : `${kind} · ${row.bot.name}`
}
function archive(value: StorageSession) {
  return run(value.session_id, async () => {
    await app.presentation(value.session_id, { archived: !value.archived })
    emit('changed')
  })
}
function remove(value: StorageSession) {
  return run(`delete:${value.session_id}`, async () => {
    await request(deleteSession({ path: { id: value.session_id }, query: { revision: value.revision } }))
    app.forgetSession(value.session_id)
    emit('changed', true)
  })
}
</script>
<template>
  <SettingsSection :title="t('storageSources')">
    <template #actions>
      <div class="storage-filter">
        <input
          v-model="search"
          type="search"
          :aria-label="t('storageSearch')"
          :placeholder="t('storageSearch')"
        >
        <Select
          v-if="bots.length > 1"
          v-model="filter"
          :label="t('storageFilterBot')"
          :options="options"
        />
      </div>
    </template>
    <div
      v-if="estimateError"
      class="row error-box"
      role="alert"
    >
      <span>{{ estimateError }}</span>
      <Button
        size="small"
        @click="reload()"
      >
        {{ t('retry') }}
      </Button>
    </div>
    <component
      :is="row.kind === 'chat' ? 'RouterLink' : 'div'"
      v-for="row in rows"
      :key="id(row)"
      v-bind="row.kind === 'chat' ? { to: { path: '/settings/storage', query: { chat: row.value.conversation_id } } } : {}"
      class="resource-row storage-source"
    >
      <div class="source-copy">
        <strong>{{ row.value.title }}</strong>
        <small>{{ subtitle(row) }} · {{ storageSize(row.value.logical_bytes, row.value.estimate_state, t) }}</small>
        <span
          v-if="row.value.logical_bytes > 0"
          class="source-size"
          aria-hidden="true"
        ><i :style="{ width: storageShare(row.value.logical_bytes, maximum) }" /></span>
        <small
          v-if="row.value.error || errors[id(row)] || errors[`delete:${id(row)}`]"
          class="text-danger"
          role="alert"
        >{{ errors[id(row)] || errors[`delete:${id(row)}`] || row.value.error }}</small>
      </div>
      <ChevronRight
        v-if="row.kind === 'chat'"
        class="source-chevron"
        aria-hidden="true"
      />
      <div
        v-else
        class="row source-actions"
      >
        <Button
          v-if="(errors[id(row)] || errors[`delete:${id(row)}`]) && !ready && !busy"
          size="small"
          @click="reload()"
        >
          {{ t('storageRecheck') }}
        </Button>
        <Button
          variant="ghost"
          size="small"
          :busy="busy === id(row)"
          :disabled="locked"
          @click="archive(row.value)"
        >
          {{ t(row.value.archived ? 'restoreConversation' : 'storageArchiveAction') }}
        </Button>
        <ConfirmPopover
          v-if="row.value.archived"
          :disabled="!ready"
          :open="confirming === `delete:${id(row)}`"
          :title="t('deleteConversationTitle', { name: row.value.title })"
          :description="t('deleteConversationHint')"
          :confirm-label="t('permanentlyDelete')"
          :cancel-label="t('cancel')"
          :busy="busy === `delete:${id(row)}`"
          @update:open="confirm(`delete:${id(row)}`, $event)"
          @confirm="remove(row.value)"
        >
          <template #trigger>
            <Button
              variant="ghost"
              size="small"
              class="danger-text"
              :disabled="locked"
            >
              {{ t('permanentlyDelete') }}
            </Button>
          </template>
        </ConfirmPopover>
      </div>
    </component>
    <p
      v-if="!rows.length"
      class="empty"
    >
      {{ t(filter || search ? 'storageNoMatches' : 'storageNoSources') }}
    </p>
  </SettingsSection>
</template>
<style scoped>
.storage-filter { display: flex; justify-content: flex-end; align-items: center; gap: 8px; min-width: 0; }
.storage-filter input { flex: 1 1 11rem; max-width: 11rem; min-width: 0; }
.storage-filter :deep(.select-trigger) { width: auto; min-width: 100px; }
.source-copy { flex: 1; min-width: 0; display: grid; gap: 3px; overflow-wrap: anywhere; }
.source-size { display: block; height: 3px; width: min(100%, 100px); background: var(--subtle); border-radius: 2px; overflow: hidden; }
.source-size i { display: block; min-width: 1px; height: 100%; background: var(--muted); }
.source-chevron { color: var(--muted); width: 15px; height: 15px; }
.source-actions { flex-shrink: 0; flex-wrap: wrap; justify-content: end; gap: 2px; }
@media (max-width: 520px) {
  .storage-filter input { max-width: 98px; }
  .source-actions { max-width: 110px; }
}
</style>
