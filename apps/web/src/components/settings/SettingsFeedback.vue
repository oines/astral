<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed } from 'vue'
import { useSettingsSave } from '../../composables/useSettingsSave'
import { useDelayedPending } from '@astral/ui'

const props = defineProps<{ scope: string | string[]; saving?: string; retry?: () => unknown }>()
const { t } = useI18n()
const saves = useSettingsSave()
const active = computed(() => {
  const keys = typeof props.scope === 'string' ? [props.scope] : props.scope
  const order = ['uncertain', 'error', 'saving', 'saved', 'idle']
  return keys.map(key => ({ key, ...saves.state(key) })).sort((a, b) => order.indexOf(a.status) - order.indexOf(b.status))[0] || { key: '', status: 'idle', error: undefined }
})
const waiting = useDelayedPending(() => active.value.status === 'saving' ? active.value.key : '')
const failed = computed(() => active.value.status === 'error' || active.value.status === 'uncertain')
</script>
<template>
  <div
    v-if="waiting || failed"
    class="settings-feedback"
    :data-state="active.status"
    :class="{ 'settings-feedback-error': active.status === 'error' || active.status === 'uncertain' }"
    role="status"
    aria-live="polite"
    aria-atomic="true"
  >
    <template v-if="waiting && !failed">
      <span
        class="spinner"
        aria-hidden="true"
      /><span class="sr-only">{{ saving || t('settingsSaving') }}</span>
    </template>
    <template v-else-if="active.status === 'error' || active.status === 'uncertain'">
      <span>{{ active.status === 'uncertain' ? t('settingsUnconfirmed') : t('settingsNotApplied') }}<span v-if="active.error"> {{ active.error }}</span></span>
      <button
        type="button"
        class="text-button"
        @click="active.status === 'error' && props.retry ? props.retry() : saves.retry(active.key)"
      >
        {{ t(active.status === 'uncertain' ? 'settingsCheckStatus' : 'retry') }}
      </button>
    </template>
  </div>
</template>
