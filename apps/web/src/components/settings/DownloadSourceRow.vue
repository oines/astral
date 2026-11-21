<script setup lang="ts">
import { computed } from 'vue'
import { SettingsRow } from '@astral/ui'
import { useI18n } from 'vue-i18n'
import { DOWNLOAD_SOURCE_PRESETS, useDownloadSource } from '../../lib/download-source'

defineProps<{ disabled?: boolean }>()
const { t } = useI18n()
const { preset, customURL, validation, isMirror } = useDownloadSource()

const errorMessage = computed(() => {
  if (validation.value.ok) return ''
  if (validation.value.reason === 'empty') return ''
  if (validation.value.reason === 'scheme') return t('downloadSource.invalidScheme')
  return t('downloadSource.invalidURL')
})
</script>
<template>
  <SettingsRow
    :label="t('downloadSource.label')"
    :description="isMirror ? t('downloadSource.mirrorHint') : undefined"
  >
    <select
      v-model="preset"
      :aria-label="t('downloadSource.label')"
      :disabled="disabled"
    >
      <option
        v-for="item in DOWNLOAD_SOURCE_PRESETS"
        :key="item.id"
        :value="item.id"
      >
        {{ item.id === 'direct' ? t('downloadSource.direct') : item.label }}
      </option>
      <option value="custom">
        {{ t('downloadSource.custom') }}
      </option>
    </select>
  </SettingsRow>
  <SettingsRow
    v-if="preset === 'custom'"
    :label="t('downloadSource.customLabel')"
  >
    <input
      v-model="customURL"
      type="url"
      :aria-label="t('downloadSource.customLabel')"
      :placeholder="t('downloadSource.customPlaceholder')"
      :disabled="disabled"
      autocomplete="url"
      spellcheck="false"
    >
  </SettingsRow>
  <p
    v-if="errorMessage"
    class="error-box"
    role="status"
  >
    {{ errorMessage }}
  </p>
</template>
