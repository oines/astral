<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { cleanupStorage, reclaimStorage } from '@astral/sdk'
import { Button, ConfirmPopover, SettingsSection, SettingsRow } from '@astral/ui'
import { request } from '../../lib/api'
import { storageBytes, storageShare, type StorageSnapshot } from '../../lib/storage'
import { useStorageAction, type StorageActionScope } from '../../composables/useStorageAction'
import { useClipboard } from '../../composables/useClipboard'

const props = defineProps<StorageActionScope & { snapshot: StorageSnapshot; recentDeletion: boolean; readError: string }>()
const emit = defineEmits<{ changed: [] }>()
const { t } = useI18n()
const { copy } = useClipboard()
const { busy, locked, confirming, errors, run, confirm } = useStorageAction(props)
const disk = computed(() => props.snapshot.physical)
const astral = computed(() => Math.min(disk.value.total_bytes, disk.value.disk_used_bytes))
const other = computed(() => Math.max(0, disk.value.disk_used_bytes - astral.value))
const pendingRelease = computed(() => props.recentDeletion && ['queued', 'waiting', 'running'].includes(props.snapshot.reclaim.state))
function clean() {
  return run('clean', async () => {
    const result = await request(cleanupStorage())
    if (result.error) throw new Error(result.error)
    emit('changed')
  })
}
function retryReclaim() {
  return run('reclaim', async () => { await request(reclaimStorage()); emit('changed') })
}
</script>
<template>
  <SettingsSection :title="t('storageDisk')">
    <div class="storage-disk preference-row">
      <template v-if="disk.disk_total_bytes">
        <div class="disk-caption">
          <strong>{{ t('storageDiskUsed', { size: storageBytes(disk.disk_used_bytes) }) }}</strong><span>{{ t('storageDiskTotal', { size: storageBytes(disk.disk_total_bytes) }) }}</span>
        </div>
        <div
          class="disk-bar"
          aria-hidden="true"
        >
          <i
            class="disk-astral"
            :style="{ width: storageShare(astral, disk.disk_total_bytes) }"
          /><i
            class="disk-other"
            :style="{ width: storageShare(other, disk.disk_total_bytes) }"
          />
        </div>
        <dl class="disk-legend">
          <div><dt><i class="disk-astral" />Astral</dt><dd>{{ storageBytes(disk.total_bytes) }}</dd></div>
          <div><dt><i class="disk-other" />{{ t('storageDiskOther') }}</dt><dd>{{ storageBytes(other) }}</dd></div>
          <div><dt><i class="disk-free" />{{ t('storageDiskFree') }}</dt><dd>{{ storageBytes(disk.available_bytes) }}</dd></div>
        </dl>
      </template>
      <p
        v-else
        class="muted small"
      >
        {{ t('storageDiskUnavailable') }} · Astral {{ storageBytes(disk.total_bytes) }}
      </p>
      <p
        v-if="pendingRelease"
        class="muted small"
      >
        {{ t('storageSpacePending') }}
      </p>
      <div
        v-if="snapshot.reclaim.state === 'failed' || errors.reclaim"
        class="row error-box"
        role="alert"
      >
        <span>{{ t('storageReclaimFailed') }} {{ errors.reclaim || snapshot.reclaim.error }}</span>
        <Button
          size="small"
          :busy="busy === 'reclaim'"
          :disabled="locked"
          @click="retryReclaim"
        >
          {{ t('retry') }}
        </Button>
      </div>
      <div
        v-if="readError"
        class="row error-box"
        role="alert"
      >
        <span>{{ t('storageReclaimReadFailed') }} {{ readError }}</span>
        <Button
          size="small"
          @click="reload()"
        >
          {{ t('retry') }}
        </Button>
      </div>
      <div
        v-if="disk.errors?.length"
        class="row error-box"
        role="alert"
      >
        <span>{{ t('storagePhysicalWarnings') }} {{ disk.errors.join(' · ') }}</span><Button
          size="small"
          @click="reload()"
        >
          {{ t('retry') }}
        </Button>
      </div>
    </div>
    <SettingsRow
      :label="t('storageDirectory')"
      :description="snapshot.directory"
    >
      <Button
        size="small"
        @click="copy(snapshot.directory)"
      >
        {{ t('copy') }}
      </Button>
    </SettingsRow>
    <SettingsRow
      :label="t('storageCacheBackups')"
      :description="storageBytes(disk.cleanable_bytes)"
    >
      <ConfirmPopover
        v-if="disk.cleanable_bytes > 0"
        :disabled="!ready"
        :open="confirming === 'clean'"
        :title="t('storageCleanTitle')"
        :description="t('storageCleanHint', { size: storageBytes(disk.cleanable_bytes) })"
        :confirm-label="t('storageClean')"
        :cancel-label="t('cancel')"
        :busy="busy === 'clean'"
        @update:open="confirm('clean', $event)"
        @confirm="clean"
      >
        <template #trigger>
          <Button
            size="small"
            :disabled="locked"
          >
            {{ t('storageClean') }}
          </Button>
        </template>
      </ConfirmPopover>
      <small
        v-if="errors.clean"
        class="text-danger"
        role="alert"
      >{{ errors.clean }}</small>
      <Button
        v-if="errors.clean && !ready && !busy"
        size="small"
        @click="reload()"
      >
        {{ t('storageRecheck') }}
      </Button>
    </SettingsRow>
  </SettingsSection>
</template>
<style scoped>
.storage-disk { align-items: stretch; flex-direction: column; gap: 12px; }
.disk-caption { display: flex; justify-content: space-between; gap: 12px; font-size: 13px; color: var(--muted); }
.disk-caption strong { font-weight: 500; color: var(--ink); }
.disk-bar { display: flex; height: 18px; border: 1px solid var(--panel-line); border-radius: 5px; background: var(--surface); overflow: hidden; }
.disk-astral { background: var(--ink); }
.disk-other { background: var(--subtle); }
.disk-free { border: 1px solid var(--panel-line); background: var(--surface); }
.disk-legend { display: flex; flex-wrap: wrap; gap: 12px 24px; margin: 0; font-size: 12px; }
.disk-legend dt { display: flex; align-items: center; gap: 6px; color: var(--muted); }
.disk-legend dd { margin: 3px 0 0 14px; font-variant-numeric: tabular-nums; }
.disk-legend i { width: 8px; height: 8px; border-radius: 2px; }
</style>
