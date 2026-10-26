<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { computerMaintenance, updateComputerDaemon, type AstralInternalDomainComputerMaintenance } from '@astral/sdk'
import { Button, ConfirmPopover, SettingsRow, SettingsSection, useDelayedPending } from '@astral/ui'
import { useI18n } from 'vue-i18n'
import type { Computer } from '../../lib/types'
import { errorText, request } from '../../lib/api'
import CopyValue from '../CopyValue.vue'
import { useAction } from '../../composables/useAction'

const props = defineProps<{ computer: Computer }>()
const { t } = useI18n()
const maintenance = ref<AstralInternalDomainComputerMaintenance>()
const commandPlatform = ref<'shell' | 'powershell'>('shell')
const local = computed(() => props.computer.local || props.computer.kind === 'local')
const command = computed(() => commandPlatform.value === 'powershell' ? maintenance.value?.powershell_command : maintenance.value?.command)
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
  if (local.value || props.computer.revoked) return
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
  await action.run(async () => {
    const previous = maintenance.value?.update.started_at
    try {
      const result = await request(updateComputerDaemon({ path: { id: props.computer.id } }))
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
    :description="t(local ? 'computerMaintenance.localHint' : computer.kind === 'container' ? 'computerMaintenance.containerHint' : 'computerMaintenance.remoteHint')"
  >
    <SettingsRow
      v-if="local"
      :label="t('computerMaintenance.updateMethod')"
    >
      <span>{{ t('computerMaintenance.withServer') }}</span>
    </SettingsRow>
    <template v-else>
      <SettingsRow :label="t('computerMaintenance.updateDaemon')">
        <ConfirmPopover
          v-model:open="confirming"
          :title="t('computerMaintenance.updateDaemon')"
          :description="t('computerMaintenance.confirmUpdate')"
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
              :disabled="!maintenance?.can_update || updating || action.busy.value"
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
      <SettingsRow :label="t(computer.kind === 'container' ? 'computerCommand.hostPlatform' : 'computerCommand.targetPlatform')">
        <select
          v-model="commandPlatform"
          :aria-label="t(computer.kind === 'container' ? 'computerCommand.hostPlatform' : 'computerCommand.targetPlatform')"
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
