<script setup lang="ts">
import { ref, watch } from 'vue'
import { resourceUsage, type AstralInternalHttpapiUsage } from '@astral/sdk'
import { Button, Modal } from '@astral/ui'
import { useI18n } from 'vue-i18n'
import { request, errorText } from '../../lib/api'
const props = defineProps<{ open: boolean; kind: string; id: string; name: string; busy?: boolean; actionLabel?: string }>()
defineEmits<{ 'update:open': [value: boolean]; confirm: [] }>()
const { t } = useI18n()
const usage = ref<AstralInternalHttpapiUsage>()
const error = ref('')
watch(() => [props.open, props.id], async () => {
  if (!props.open || !props.id) return
  usage.value = undefined
  error.value = ''
  try { usage.value = await request(resourceUsage({ path: { kind: props.kind, id: props.id } })) }
  catch (e) { error.value = errorText(e) }
}, { immediate: true })
</script>
<template>
  <Modal
    :open="open"
    :title="`${actionLabel || t('delete')} ${name}`"
    @update:open="$emit('update:open', $event)"
  >
    <p class="muted">
      {{ t('resourceDeleteHint') }}
    </p>
    <p
      v-if="error"
      class="error-box"
    >
      {{ error }}
    </p>
    <p
      v-if="!usage && !error"
      class="muted"
    >
      {{ t('loading') }}
    </p>
    <template v-if="usage">
      <div
        v-for="group in [{ name: t('agents'), items: usage.agents }, { name: t('profiles'), items: usage.profiles }, { name: t('recent'), items: usage.sessions }]"
        :key="group.name"
        class="stack usage-group"
      >
        <h3>{{ group.name }} · {{ group.items?.length || 0 }}</h3><p
          v-for="item in group.items"
          :key="item.id"
          class="muted"
        >
          {{ 'name' in item ? item.name : item.title }}
        </p>
      </div>
    </template>
    <div class="row">
      <Button @click="$emit('update:open', false)">
        {{ t('cancel') }}
      </Button><Button
        variant="danger"
        :busy="busy"
        :disabled="!usage"
        @click="$emit('confirm')"
      >
        {{ actionLabel || t('delete') }}
      </Button>
    </div>
  </Modal>
</template>
