<script setup lang="ts">
import { SettingsRow, Switch } from '@astral/ui'
import { useI18n } from 'vue-i18n'

defineProps<{ disabled?: boolean; description?: string }>()
const enabled = defineModel<boolean>({ required: true })
const { t } = useI18n()
</script>
<template>
  <SettingsRow
    :label="t('computerAPI.enable')"
    :description="description || t('computerAPI.hint')"
  >
    <Switch
      v-model="enabled"
      :label="t('computerAPI.enable')"
      :disabled="disabled"
    />
  </SettingsRow>
</template>
