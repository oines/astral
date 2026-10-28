<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, SettingsRow, SettingsSection } from '@astral/ui'
import { getServerUpdate, checkServerUpdate, applyServerUpdate } from '@astral/sdk'
import '../../lib/api'
import { SoftwareUpdates, updateResponse, type SoftwareUpdateState } from '../../lib/software-updates'
import type { DesktopUpdateState } from '../../../../desktop/src/shared/updates'
const { t } = useI18n()
type UpdateAction = 'check' | 'update' | 'downloads'
const bridge = window.astralDesktop
const state = ref<SoftwareUpdateState>()
const error = ref('')
const pending = ref<UpdateAction>()
const waiting = ref(false)
const controlled = ref(false)
function map(value: { phase: string; current_version: string; version?: string; percent?: number; error?: string; retry?: string; unavailable?: string; manager: string; operation_id?: string; operation_version?: string; operation_state?: string }): SoftwareUpdateState {
  return { phase: value.phase as DesktopUpdateState['phase'], currentVersion: value.current_version, version: value.version, percent: value.percent,
    error: value.error, retry: value.retry as DesktopUpdateState['retry'], unavailable: value.unavailable, manager: value.manager,
    operationId: value.operation_id, operationVersion: value.operation_version, operationState: value.operation_state as DesktopUpdateState['operationState'] }
}
let storage: Storage | undefined
try { storage = window.sessionStorage } catch { /* Storage is optional. */ }
const monitor = new SoftwareUpdates({
  state: async () => map(await updateResponse(getServerUpdate({ signal: AbortSignal.timeout(10_000), throwOnError: false }))),
  check: async () => map(await updateResponse(checkServerUpdate({ signal: AbortSignal.timeout(60_000), throwOnError: false }))),
  apply: async () => map(await updateResponse(applyServerUpdate({ signal: AbortSignal.timeout(10_000), throwOnError: false }))),
}, storage)
monitor.onChange = () => { state.value = monitor.state; error.value = monitor.error; waiting.value = monitor.waiting; controlled.value = monitor.busy }
monitor.onUpdated = () => { window.location.reload() }
onMounted(() => { void monitor.start() })
onBeforeUnmount(() => { monitor.stop() })
const phase = computed(() => state.value?.phase || 'idle')
const busy = computed(() => controlled.value || Boolean(pending.value) || ['checking', 'downloading', 'installing'].includes(phase.value))
const hasProgress = computed(() => phase.value === 'downloading' && !waiting.value && state.value?.percent !== undefined && Number.isFinite(state.value.percent))
const action = computed<UpdateAction>(() => {
  if (phase.value === 'unavailable') return 'downloads'
  if (pending.value === 'update' || phase.value === 'available' || phase.value === 'error' && state.value?.retry === 'update') return 'update'
  return 'check'
})
const actionLabel = computed(() => {
  if (waiting.value) return 'desktopUpdateWaiting'
  if (phase.value === 'downloading') return 'desktopUpdatePreparing'
  if (phase.value === 'installing') return 'desktopUpdateInstalling'
  if (action.value === 'downloads') return 'desktopUpdateDownloads'
  if (phase.value === 'error') return action.value === 'update' ? 'desktopUpdateRetryUpdate' : 'desktopUpdateRetryCheck'
  return action.value === 'update' ? 'desktopUpdateUpdate' : 'desktopUpdateCheck'
})
const description = computed(() => {
  if (waiting.value) return t('desktopUpdateWaitingDescription')
  if (phase.value === 'unavailable') {
    const reason = state.value?.unavailable
    return t(`desktopUpdateUnavailable_${['development', 'package', 'windows-zip', 'signature'].includes(reason || '') ? reason : 'unmanaged'}`)
  }
  if (phase.value === 'downloading') return hasProgress.value ? t('desktopUpdateDownloading', { version: state.value?.version, percent: Math.floor(state.value!.percent!) }) : t('desktopUpdatePreparingDescription')
  if (phase.value === 'available') return t('desktopUpdateAvailable', { version: state.value?.version })
  if (phase.value === 'error' && state.value?.retry === 'update') return t('desktopUpdateRetryDescription')
  return t(`desktopUpdate_${phase.value}`)
})
async function act(intent: UpdateAction = action.value) {
  if (busy.value) return
  error.value = ''
  pending.value = intent
  try {
    if (intent === 'downloads') {
      if (bridge) await bridge.openExternal('https://github.com/oines/astral/releases/latest')
      else window.open('https://github.com/oines/astral/releases/latest', '_blank', 'noopener,noreferrer')
    } else await monitor.act(intent)
  } catch (value) { error.value = value instanceof Error ? value.message : String(value) }
  finally { pending.value = undefined }
}
</script>
<template>
  <SettingsSection>
    <SettingsRow
      :label="t('desktopUpdateTitle')"
      :description="description"
    >
      <progress
        v-if="hasProgress"
        :value="state?.percent || 0"
        max="100"
        :aria-label="t('desktopUpdateProgress')"
      />
      <Button
        v-else
        :busy="busy"
        @click="act()"
      >
        {{ t(actionLabel) }}
      </Button>
    </SettingsRow>
    <p
      v-if="error || state?.error"
      class="error-box"
      role="alert"
    >
      {{ error || state?.error }}
    </p>
  </SettingsSection>
</template>
<style scoped>
progress { width: var(--settings-control-width); max-width: 100%; accent-color: var(--control-active); }
</style>
