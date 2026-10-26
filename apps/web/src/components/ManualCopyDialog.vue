<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, Modal } from '@astral/ui'
import { manualCopyText } from '../composables/useClipboard'
const { t } = useI18n()
const text = ref<HTMLTextAreaElement>()
const open = computed({ get: () => manualCopyText.value !== undefined, set: value => { if (!value) manualCopyText.value = undefined } })
watch(manualCopyText, async value => {
  if (value === undefined) return
  await nextTick()
  text.value?.focus()
  text.value?.select()
})
</script>
<template>
  <Modal
    v-model:open="open"
    :title="t('clipboardManualTitle')"
  >
    <p class="muted">
      {{ t('clipboardManualHint') }}
    </p>
    <textarea
      ref="text"
      :value="manualCopyText"
      :aria-label="t('clipboardManualText')"
      class="mono"
      rows="8"
      readonly
      @focus="text?.select()"
    />
    <Button @click="open = false">
      {{ t('close') }}
    </Button>
  </Modal>
</template>
