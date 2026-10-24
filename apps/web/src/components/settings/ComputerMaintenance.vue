<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { computerMaintenance, type AstralInternalDomainComputerMaintenance } from '@astral/sdk'
import { Button, SettingsRow, SettingsSection } from '@astral/ui'
import { useI18n } from 'vue-i18n'
import type { Computer } from '../../lib/types'
import { errorText, request } from '../../lib/api'
import CopyValue from '../CopyValue.vue'

const props = defineProps<{ computer: Computer }>()
const { t } = useI18n()
const maintenance = ref<AstralInternalDomainComputerMaintenance>()
const commandPlatform = ref<'shell' | 'powershell'>('shell')
const local = computed(() => props.computer.local || props.computer.kind === 'local')
const command = computed(() => commandPlatform.value === 'powershell' ? maintenance.value?.powershell_command : maintenance.value?.command)
const error = ref('')
const loading = ref(false)
let generation = 0
async function load() {
  const current = ++generation
  maintenance.value = undefined
  error.value = ''
  loading.value = false
  if (local.value || props.computer.revoked) return
  loading.value = true
  try {
    const result = await request(computerMaintenance({ path: { id: props.computer.id } }))
    if (current === generation) maintenance.value = result
  } catch (e) { if (current === generation) error.value = errorText(e) }
  finally { if (current === generation) loading.value = false }
}
watch(() => props.computer.id, () => { commandPlatform.value = props.computer.os === 'windows' ? 'powershell' : 'shell' }, { immediate: true })
watch([() => props.computer.id, () => props.computer.root, () => props.computer.container?.name, () => props.computer.revoked], () => { void load() }, { immediate: true })
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
        v-if="loading"
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
