<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { SettingsRow } from '@astral/ui'
import { adapterInfo } from '../../lib/im-platforms'
import { feishuAppSettingsURL, feishuSetupGuideURL } from '../../lib/feishu-setup'
const props = defineProps<{ adapter: string; values: Record<string, string>; token: string; creating?: boolean }>()
const emit = defineEmits<{ 'update:token': [value: string]; field: [key: string, value: string] }>()
const { t, locale } = useI18n()
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
    <div
      v-if="adapter === 'feishu.bot'"
      class="row feishu-setup-links"
      :class="{ 'feishu-setup-links-editing': !creating }"
    >
      <a
        :href="feishuAppSettingsURL(values.app_id)"
        target="_blank"
        rel="noopener noreferrer"
        class="small"
      >{{ t('feishuConfigureApp') }}</a>
      <a
        :href="feishuSetupGuideURL(locale)"
        target="_blank"
        rel="noopener noreferrer"
        class="small"
      >{{ t('feishuSetupGuide') }}</a>
    </div>
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
<style scoped>
.feishu-setup-links { flex-wrap: wrap; }
.feishu-setup-links-editing { padding-top: var(--settings-row-padding); }
</style>
