<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed } from 'vue'
import { useSettingsSave } from '../../composables/useSettingsSave'

const props = defineProps<{ scope: string | string[]; saving?: string; saved?: string }>()
const { t } = useI18n()
const saves = useSettingsSave()
const active = computed(() => {
  const keys = typeof props.scope === 'string' ? [props.scope] : props.scope
  const order = ['uncertain', 'error', 'saving', 'saved', 'idle']
  return keys.map(key => ({ key, ...saves.state(key) })).sort((a, b) => order.indexOf(a.status) - order.indexOf(b.status))[0] || { key: '', status: 'idle', error: undefined }
})
</script>
<template>
  <div
    class="settings-feedback"
    :data-state="active.status"
    :class="{ 'settings-feedback-error': active.status === 'error' || active.status === 'uncertain' }"
    role="status"
    aria-live="polite"
    aria-atomic="true"
  >
    <template v-if="active.status === 'saving'">
      <span
        class="spinner"
        aria-hidden="true"
      /><span>{{ saving || t('settingsSaving') }}</span>
    </template>
    <span v-else-if="active.status === 'saved'">{{ saved || t('saved') }}</span>
    <template v-else-if="active.status === 'error' || active.status === 'uncertain'">
      <span>{{ active.status === 'uncertain' ? t('settingsUnconfirmed') : t('settingsNotApplied') }}<span v-if="active.error"> {{ active.error }}</span></span>
      <button
        type="button"
        class="text-button"
        @click="saves.retry(active.key)"
      >
        {{ t(active.status === 'uncertain' ? 'settingsCheckStatus' : 'retry') }}
      </button>
    </template>
  </div>
</template>
