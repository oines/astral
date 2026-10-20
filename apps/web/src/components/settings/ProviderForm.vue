<script setup lang="ts">
import { watch } from 'vue'
import { useForm } from 'vee-validate'
import { toTypedSchema } from '@vee-validate/zod'
import { z } from 'zod'
import { saveProvider } from '@astral/sdk'
import { Button } from '@astral/ui'
import { useI18n } from 'vue-i18n'
import type { Provider } from '../../lib/types'
import { request, newMeta } from '../../lib/api'
import { useAction } from '../../composables/useAction'
const props = defineProps<{ provider?: Provider }>()
const emit = defineEmits<{ saved: [provider: Provider]; cancel: [] }>()
const { t } = useI18n()
const action = useAction()
const schema = toTypedSchema(z.object({
  name: z.string().trim().min(1, 'A name is required'),
  base_url: z.string().url('Enter a complete URL'),
  protocol: z.enum(['openai-completions', 'openai-responses']),
  dialect: z.enum(['openai', 'deepseek', 'qwen', 'openrouter']),
  api_key: z.string(),
  headers: z.string().refine(v => {
    if (!v.trim()) return true
    try { const data = JSON.parse(v); return data && !Array.isArray(data) && typeof data === 'object' && Object.values(data).every(x => typeof x === 'string') } catch { return false }
  }, 'Use a JSON object with string header values'),
}).refine(v => v.protocol !== 'openai-responses' || ['openai', 'deepseek'].includes(v.dialect), { message: 'Responses is available for OpenAI and DeepSeek', path: ['protocol'] }))
const { errors, defineField, handleSubmit, resetForm } = useForm({ validationSchema: schema })
const [name] = defineField('name')
const [baseURL] = defineField('base_url')
const [protocol] = defineField('protocol')
const [dialect] = defineField('dialect')
const [apiKey] = defineField('api_key')
const [headers] = defineField('headers')
watch(() => props.provider, provider => resetForm({ values: { name: provider?.name || '', base_url: provider?.base_url || '', protocol: provider?.protocol as 'openai-completions' || 'openai-completions', dialect: provider?.dialect as 'openai' || 'openai', api_key: '', headers: '' } }), { immediate: true })
const submit = handleSubmit(async values => {
  await action.run(async () => {
    const provider = await request(saveProvider({ body: { ...(props.provider || newMeta()), name: values.name, base_url: values.base_url, protocol: values.protocol, dialect: values.dialect, ...(values.api_key ? { api_key: values.api_key } : {}), ...(values.headers.trim() ? { headers: JSON.parse(values.headers) as Record<string, string> } : {}) } }))
    apiKey.value = ''
    headers.value = ''
    emit('saved', provider)
  })
})
</script>
<template>
  <form
    class="stack"
    @submit.prevent="submit"
  >
    <div class="field-grid">
      <label class="label">{{ t('name') }}<input
        v-model="name"
        autocomplete="off"
      ><span
        v-if="errors.name"
        class="field-error"
      >{{ errors.name }}</span></label><label class="label">{{ t('dialect') }}<select v-model="dialect"><option value="openai">OpenAI / Compatible</option><option value="deepseek">DeepSeek</option><option value="qwen">Qwen</option><option value="openrouter">OpenRouter</option></select></label>
    </div>
    <label class="label">{{ t('endpoint') }}<input
      v-model="baseURL"
      type="url"
      placeholder="https://api.example.com/v1"
    ><span
      v-if="errors.base_url"
      class="field-error"
    >{{ errors.base_url }}</span></label>
    <label class="label">{{ t('protocol') }}<select v-model="protocol"><option value="openai-completions">Chat Completions</option><option
      value="openai-responses"
      :disabled="dialect !== 'openai' && dialect !== 'deepseek'"
    >Responses</option></select><span
      v-if="errors.protocol"
      class="field-error"
    >{{ errors.protocol }}</span></label>
    <label class="label">{{ t('apiKey') }}<input
      v-model="apiKey"
      type="password"
      autocomplete="new-password"
      :placeholder="provider?.credential_id ? t('storedSecret') : ''"
    ></label>
    <details class="form-disclosure">
      <summary>{{ t('extraHeaders') }}</summary><label class="label"><textarea
        v-model="headers"
        placeholder="{&quot;X-Custom-Header&quot;: &quot;value&quot;}"
      /><span class="muted small">{{ t('extraHeadersHint') }}</span><span
        v-if="errors.headers"
        class="field-error"
      >{{ errors.headers }}</span></label>
    </details>
    <p
      v-if="action.error.value"
      class="error-box"
    >
      {{ action.error.value }}
    </p>
    <div class="form-footer">
      <Button @click="emit('cancel')">
        {{ t('cancel') }}
      </Button><Button
        type="submit"
        variant="primary"
        :busy="action.busy.value"
      >
        {{ t('save') }}
      </Button>
    </div>
  </form>
</template>
