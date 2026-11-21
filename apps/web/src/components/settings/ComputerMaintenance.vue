<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { computerMaintenance, updateComputerDaemon, type AstralInternalDomainComputerMaintenance } from '@astral/sdk'
import { Button, ConfirmPopover, SettingsRow, SettingsSection, useDelayedPending } from '@astral/ui'
import { useI18n } from 'vue-i18n'
import type { Computer } from '../../lib/types'
import { errorText, request } from '../../lib/api'
import { applyDownloadSource, useDownloadSource } from '../../lib/download-source'
import CopyValue from '../CopyValue.vue'
import DownloadSourceRow from './DownloadSourceRow.vue'
import { useAction } from '../../composables/useAction'

const props = defineProps<{ computer: Computer }>()
const { t } = useI18n()
const maintenance = ref<AstralInternalDomainComputerMaintenance>()
const commandPlatform = ref<'shell' | 'powershell'>('shell')
const downloadSource = useDownloadSource()
const local = computed(() => props.computer.local || props.computer.kind === 'local')
const isContainer = computed(() => props.computer.kind === 'container')
const effectivePlatform = computed<'shell' | 'powershell'>(() => isContainer.value ? commandPlatform.value : (props.computer.os === 'windows' ? 'powershell' : 'shell'))
const command = computed(() => {
  if (!downloadSource.valid.value) return ''
  const raw = effectivePlatform.value === 'powershell' ? maintenance.value?.powershell_command || '' : maintenance.value?.command || ''
  return applyDownloadSource(raw, effectivePlatform.value, downloadSource.proxy.value)
})
const confirmDescription = computed(() => downloadSource.isMirror.value
  ? t('computerMaintenance.confirmUpdateWithSource', { source: downloadSource.displayLabel(t) })
  : t('computerMaintenance.confirmUpdate'))
const error = ref('')
const loading = ref(false)
const waiting = useDelayedPending(loading)
const action = useAction()
const confirming = ref(false)
const updating = computed(() => ['checking', 'downloading', 'restarting'].includes(maintenance.value?.update.state || ''))
let poll: ReturnType<typeof setTimeout> | undefined
let generation = 0
async function load() {
  const current = ++generation
  clearTimeout(poll)
  error.value = ''
  loading.value = false
  if (local.value || props.computer.revoked || props.computer.temporary) return
  loading.value = true
  try {
    const result = await request(computerMaintenance({ path: { id: props.computer.id } }))
    if (current === generation) maintenance.value = result
  } catch (e) { if (current === generation) error.value = errorText(e) }
  finally {
    if (current === generation) {
      loading.value = false
      if (updating.value) poll = setTimeout(() => { void load() }, 1000)
    }
  }
}
async function update() {
  if (!downloadSource.valid.value) return
  await action.run(async () => {
    const previous = maintenance.value?.update.started_at
    try {
      const result = await request(updateComputerDaemon({
        path: { id: props.computer.id },
        body: { github_proxy: downloadSource.proxy.value },
      }))
      if (maintenance.value) maintenance.value.update = result
    } catch (e) {
      await load()
      if (!updating.value && (!maintenance.value?.update.started_at || maintenance.value.update.started_at === previous)) throw e
    }
    confirming.value = false
    await load()
  })
}
onUnmounted(() => { generation++; clearTimeout(poll) })
watch(() => props.computer.id, () => { commandPlatform.value = props.computer.os === 'windows' ? 'powershell' : 'shell' }, { immediate: true })
watch([() => props.computer.id, () => props.computer.root, () => props.computer.container?.name, () => props.computer.revoked, () => props.computer.daemon_version, () => props.computer.online], () => { maintenance.value = undefined; confirming.value = false; void load() }, { immediate: true })
</script>
<template>
  <SettingsSection
    :title="t('computerMaintenance.title')"
    :description="t(local ? 'computerMaintenance.localHint' : isContainer ? 'computerMaintenance.containerHint' : 'computerMaintenance.remoteHint')"
  >
    <SettingsRow
      v-if="local"
      :label="t('computerMaintenance.updateMethod')"
    >
      <span>{{ t('computerMaintenance.withServer') }}</span>
    </SettingsRow>
    <template v-else>
      <DownloadSourceRow :disabled="updating || action.busy.value" />
      <SettingsRow :label="t('computerMaintenance.updateDaemon')">
        <ConfirmPopover
          v-model:open="confirming"
          :title="t('computerMaintenance.updateDaemon')"
          :description="confirmDescription"
          :confirm-label="t('computerMaintenance.updateDaemon')"
          :cancel-label="t('cancel')"
          :busy="action.busy.value"
          :error="action.error.value"
          variant="primary"
          @confirm="update"
        >
          <template #trigger>
            <Button
              size="small"
              :disabled="!maintenance?.can_update || updating || action.busy.value || !downloadSource.valid.value"
              :busy="updating"
            >
              {{ t('computerMaintenance.updateDaemon') }}
            </Button>
          </template>
        </ConfirmPopover>
      </SettingsRow>
      <p
        v-if="maintenance?.daemon_newer"
        class="settings-context-note"
        role="status"
      >
        {{ t('computerMaintenance.updateServerHint') }}
      </p>
      <p
        v-if="maintenance?.update.state"
        :class="maintenance.update.state === 'failed' ? 'error-box' : 'settings-context-note'"
        role="status"
      >
        {{ maintenance.update.error || t(`computerMaintenance.states.${maintenance.update.state}`) }}
      </p>
      <SettingsRow
        v-if="isContainer"
        :label="t('computerCommand.hostPlatform')"
      >
        <select
          v-model="commandPlatform"
          :aria-label="t('computerCommand.hostPlatform')"
        >
          <option value="shell">
            {{ t('computerCommand.shell') }}
          </option>
          <option value="powershell">
            {{ t('computerCommand.powershell') }}
          </option>
        </select>
      </SettingsRow>
      <SettingsRow
        v-if="command"
        :label="t('computerMaintenance.updateCommand')"
      >
        <CopyValue
          :value="command"
          :label="t('computerMaintenance.updateCommand')"
        />
      </SettingsRow>
      <p
        v-if="maintenance"
        class="settings-context-note"
      >
        {{ t(`computerMaintenance.managers.${maintenance.manager}`) }}
      </p>
      <p
        v-if="waiting && !updating"
        class="settings-context-note"
        role="status"
      >
        {{ t('loading') }}
      </p>
      <template v-if="error">
        <p
          class="error-box"
          role="status"
        >
          {{ error }}
        </p>
        <Button
          size="small"
          @click="load"
        >
          {{ t('retry') }}
        </Button>
      </template>
    </template>
  </SettingsSection>
</template>
