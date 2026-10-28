<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, SettingsRow, SettingsSection } from '@astral/ui'
import type { DesktopUpdateState } from '../../../../desktop/src/shared/updates'
const { t } = useI18n()
type UpdateAction = 'check' | 'update' | 'downloads'
const bridge = window.astralDesktop
const state = ref<DesktopUpdateState>()
const error = ref('')
const pending = ref<UpdateAction>()
let unsubscribe: (() => void) | undefined
let changes = 0
let disposed = false
onMounted(async () => {
  if (!bridge) return
  unsubscribe = bridge.updates.onState(value => { changes++; state.value = value; error.value = '' })
  try {
    const initial = await bridge.updates.state()
    if (!disposed && changes === 0) state.value = initial
  } catch (value) { if (!disposed) error.value = value instanceof Error ? value.message : String(value) }
})
onBeforeUnmount(() => { disposed = true; unsubscribe?.() })
const phase = computed(() => state.value?.phase || 'idle')
const busy = computed(() => Boolean(pending.value) || ['checking', 'downloading', 'installing'].includes(phase.value))
const action = computed<UpdateAction>(() => {
  if (phase.value === 'unavailable') return 'downloads'
  if (pending.value === 'update' || phase.value === 'available' || phase.value === 'error' && state.value?.retry === 'update') return 'update'
  return 'check'
})
const actionLabel = computed(() => {
  if (phase.value === 'installing') return 'desktopUpdateInstalling'
  if (action.value === 'downloads') return 'desktopUpdateDownloads'
  if (phase.value === 'error') return action.value === 'update' ? 'desktopUpdateRetryUpdate' : 'desktopUpdateRetryCheck'
  return action.value === 'update' ? 'desktopUpdateUpdate' : 'desktopUpdateCheck'
})
const description = computed(() => {
  if (phase.value === 'unavailable') return t(`desktopUpdateUnavailable_${state.value?.unavailable}`)
  if (phase.value === 'downloading') return t('desktopUpdateDownloading', { version: state.value?.version, percent: Math.floor(state.value?.percent || 0) })
  if (phase.value === 'available') return t('desktopUpdateAvailable', { version: state.value?.version })
  if (phase.value === 'error' && state.value?.retry === 'update') return t('desktopUpdateRetryDescription')
  return t(`desktopUpdate_${phase.value}`)
})
async function act(intent: UpdateAction = action.value) {
  if (!bridge || busy.value) return
  error.value = ''
  pending.value = intent
  try {
    if (intent === 'update') await bridge.updates.update()
    else if (intent === 'downloads') await bridge.openExternal('https://github.com/oines/astral/releases/latest')
    else await bridge.updates.check()
  } catch (value) { error.value = value instanceof Error ? value.message : String(value) }
  finally { pending.value = undefined }
}
</script>
<template>
  <SettingsSection v-if="bridge">
    <SettingsRow
      :label="t('desktopUpdateTitle')"
      :description="description"
    >
      <progress
        v-if="phase === 'downloading'"
        :value="state?.percent || 0"
        max="100"
        :aria-label="t('desktopUpdateProgress')"
      />
      <div
        v-else
        class="row update-actions"
      >
        <Button
          v-if="action === 'update' && phase !== 'installing'"
          :disabled="busy"
          @click="act('check')"
        >
          {{ t('desktopUpdateCheck') }}
        </Button>
        <Button
          :busy="busy"
          @click="act()"
        >
          {{ t(actionLabel) }}
        </Button>
      </div>
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
.update-actions { flex-wrap: wrap; justify-content: flex-end; }
</style>
