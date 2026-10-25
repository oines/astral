<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { resourceUsage, type AstralInternalHttpapiUsage } from '@astral/sdk'
import { Button, Modal } from '@astral/ui'
import { useI18n } from 'vue-i18n'
import { request, errorText } from '../../lib/api'
const props = defineProps<{ open: boolean; kind: string; id: string; name: string; busy?: boolean; actionLabel?: string; description?: string; errorMessage?: string }>()
defineEmits<{ 'update:open': [value: boolean]; confirm: [] }>()
const { t } = useI18n()
const usage = ref<AstralInternalHttpapiUsage>()
const groups = computed(() => [
  { name: t('agents'), items: usage.value?.agents },
  { name: t('profiles'), items: usage.value?.profiles },
  { name: t('recent'), items: usage.value?.sessions },
].filter(group => group.items?.length))
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
      {{ description || t(kind === 'computers' ? 'revokeComputerHint' : 'resourceDeleteHint') }}
    </p>
    <p
      v-if="error || errorMessage"
      class="error-box"
      role="alert"
    >
      {{ error || errorMessage }}
    </p>
    <p
      v-if="!usage && !error"
      class="muted"
    >
      {{ t('loading') }}
    </p>
    <template v-if="usage">
      <div
        v-for="group in groups"
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
      <p
        v-if="!groups.length"
        class="muted"
      >
        {{ t('noResourceUsage') }}
      </p>
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
