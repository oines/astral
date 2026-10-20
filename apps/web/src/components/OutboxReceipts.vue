<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { useIntervalFn } from '@vueuse/core'
import { listOutboxQuery } from '@astral/sdk/colada'
import { resolveSend, retrySend, type AstralInternalDomainOutbox } from '@astral/sdk'
import { Button, Modal } from '@astral/ui'
import { CircleAlert } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import { useAction } from '../composables/useAction'
import { request } from '../lib/api'
const props = defineProps<{ sessionID?: string; all?: boolean }>()
const { t, te } = useI18n()
const action = useAction()
const query = useQuery(() => ({ ...listOutboxQuery({ query: props.sessionID ? { session_id: props.sessionID } : {} }), staleTime: 5000 }))
const entries = computed(() => (query.data.value ?? []).filter(e => props.all || ['unknown', 'blocked', 'failed'].includes(e.status)))
const selected = ref<AstralInternalDomainOutbox>()
const note = ref('')
const ids = ref('')
let polling = false
useIntervalFn(async () => { if (polling || document.visibilityState === 'hidden') return; polling = true; try { await query.refetch() } finally { polling = false } }, 5000)
async function resolve(status: 'accepted' | 'failed') {
  await action.run(async () => {
    if (!selected.value) return
    await request(resolveSend({ path: { id: selected.value.id }, body: { status, note: note.value, message_ids: ids.value.split(',').map(s => s.trim()).filter(Boolean) } }))
    selected.value = undefined
    await query.refetch()
  })
}
async function retry(id: string) { await action.run(async () => { await request(retrySend({ path: { id } })); await query.refetch() }) }
</script>
<template>
  <div
    v-if="entries.length"
    class="outbox-list stack"
  >
    <p
      v-if="action.error.value && !selected"
      class="error-box"
    >
      {{ action.error.value }}
    </p>
    <div
      v-for="entry in entries"
      :key="entry.id"
      class="outbox-receipt"
      :class="{ attention: ['unknown', 'blocked', 'failed'].includes(entry.status) }"
    >
      <div class="row between">
        <span class="row"><CircleAlert v-if="['unknown', 'blocked', 'failed'].includes(entry.status)" /><strong>Telegram · {{ te('receipt_' + entry.status) ? t('receipt_' + entry.status) : entry.status }}</strong></span><span class="muted small">{{ new Date(entry.created_at).toLocaleTimeString() }}</span>
      </div>
      <p
        v-if="entry.error"
        class="small"
      >
        {{ entry.error }}
      </p>
      <p
        v-if="all"
        class="outbox-text"
      >
        {{ entry.text || entry.file_ref_id }}
      </p>
      <div
        v-if="entry.status === 'failed' && entry.retryable"
        class="row"
      >
        <Button
          size="small"
          :busy="action.busy.value"
          @click="retry(entry.id)"
        >
          {{ t('retry') }}
        </Button>
      </div>
      <Button
        v-else-if="entry.status === 'unknown' || entry.status === 'blocked'"
        class="align-start"
        size="small"
        @click="selected = entry; note = ''; ids = ''"
      >
        {{ t('inspect') }}
      </Button>
      <span
        v-if="entry.platform_message_ids?.length"
        class="muted small"
      >{{ t('messageID') }} {{ entry.platform_message_ids.join(', ') }}</span>
    </div>
    <Modal
      :open="Boolean(selected)"
      :title="t('unknown')"
      @update:open="v => { if (!v) selected = undefined }"
    >
      <p class="muted">
        {{ t('inspectSendHint') }}
      </p>
      <pre class="connection-command">{{ selected?.text || selected?.file_ref_id }}</pre>
      <p
        v-if="action.error.value"
        class="error-box"
      >
        {{ action.error.value }}
      </p>
      <label class="label">{{ t('verificationNote') }}<textarea v-model="note" /></label><label class="label">{{ t('messageIDs') }}<input
        v-model="ids"
        placeholder="123, 124"
      ></label>
      <div class="row">
        <Button
          :disabled="!note.trim()"
          :busy="action.busy.value"
          @click="resolve('accepted')"
        >
          {{ t('confirmedSent') }}
        </Button><Button
          :disabled="!note.trim()"
          :busy="action.busy.value"
          @click="resolve('failed')"
        >
          {{ t('confirmedNotSent') }}
        </Button>
      </div>
    </Modal>
  </div>
</template>
