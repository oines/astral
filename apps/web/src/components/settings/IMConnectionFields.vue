<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { SettingsRow } from '@astral/ui'
import { adapterInfo } from '../../lib/im-platforms'
const props = defineProps<{ adapter: string; values: Record<string, string>; token: string; creating?: boolean }>()
const emit = defineEmits<{ 'update:token': [value: string]; field: [key: string, value: string] }>()
const { t } = useI18n()
const info = computed(() => adapterInfo(props.adapter))
</script>
<template>
  <template v-if="info">
    <p
      v-if="creating && info.hint"
      class="muted small"
    >
      {{ t(info.hint) }}
    </p>
    <SettingsRow
      v-if="info.secret"
      :label="t(info.secret)"
    >
      <input
        :value="token"
        :aria-label="t(info.secret)"
        type="password"
        autocomplete="new-password"
        :required="creating && adapter !== 'qq.napcat'"
        :placeholder="creating ? '' : t('storedSecret')"
        @input="emit('update:token', ($event.target as HTMLInputElement).value)"
      >
    </SettingsRow>
    <SettingsRow
      v-for="field in info.fields"
      :key="field.key"
      :label="t(field.label)"
    >
      <input
        :value="values[field.key] || ''"
        :aria-label="t(field.label)"
        :required="'required' in field && field.required"
        :placeholder="field.placeholder"
        @input="emit('field', field.key, ($event.target as HTMLInputElement).value)"
      >
    </SettingsRow>
  </template>
</template>
