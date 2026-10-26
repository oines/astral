<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, SettingsRow, SettingsSection } from '@astral/ui'
import type { DesktopUpdateState } from '../../../../desktop/src/shared/updates'
const { t } = useI18n()
const bridge = window.astralDesktop
const state = ref<DesktopUpdateState>()
const error = ref('')
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
const busy = computed(() => ['checking', 'installing'].includes(phase.value))
const description = computed(() => {
  if (phase.value === 'unavailable') return t(`desktopUpdateUnavailable_${state.value?.unavailable}`)
  if (phase.value === 'downloading') return t('desktopUpdateDownloading', { version: state.value?.version, percent: Math.floor(state.value?.percent || 0) })
  if (phase.value === 'ready') return t('desktopUpdateReady', { version: state.value?.version })
  return t(`desktopUpdate_${phase.value}`)
})
async function act() {
  if (!bridge) return
  error.value = ''
  try {
    if (phase.value === 'ready') await bridge.updates.install()
    else if (phase.value === 'unavailable') await bridge.openExternal('https://github.com/oines/astral/releases/latest')
    else await bridge.updates.check()
  } catch (value) { error.value = value instanceof Error ? value.message : String(value) }
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
      <Button
        v-else
        :busy="busy"
        @click="act"
      >
        {{ t(phase === 'ready' ? 'desktopUpdateInstall' : phase === 'unavailable' ? 'desktopUpdateDownloads' : phase === 'error' ? 'retry' : phase === 'installing' ? 'desktopUpdateInstalling' : 'desktopUpdateCheck') }}
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
