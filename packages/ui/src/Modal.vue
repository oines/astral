<script setup lang="ts">
import { onActivated, onDeactivated, ref } from 'vue'
import { X } from '@astral/icons'
import { DialogRoot, DialogPortal, DialogOverlay, DialogContent, DialogTitle, DialogDescription, DialogClose } from 'reka-ui'
defineOptions({ inheritAttrs: false })
defineProps<{ open: boolean; title: string; description?: string }>()
defineEmits<{ 'update:open': [value: boolean] }>()
const active = ref(true)
onActivated(() => { active.value = true })
onDeactivated(() => { active.value = false })
function focusFirstField(event: Event) {
  if (!(event.target instanceof HTMLElement)) return
  const field = event.target.querySelector<HTMLElement>('input:not([type="hidden"]):not([type="checkbox"]):not([type="radio"]):not([type="file"]):not(:disabled), textarea:not(:disabled), [role="combobox"]:not([disabled])')
  if (!field) return
  event.preventDefault()
  field.focus({ preventScroll: true })
}
</script>
<template>
  <DialogRoot
    :open="open && active"
    @update:open="active && $emit('update:open', $event)"
  >
    <DialogPortal>
      <DialogOverlay class="modal-overlay" />
      <DialogContent
        v-bind="$attrs"
        class="modal-content"
        :aria-describedby="description ? undefined : ''"
        @open-auto-focus="focusFirstField"
      >
        <div class="row between">
          <DialogTitle class="modal-title">
            {{ title }}
          </DialogTitle>
          <DialogClose
            class="button button-ghost button-icon"
            aria-label="Close"
          >
            <X />
          </DialogClose>
        </div>
        <DialogDescription
          v-if="description"
          class="muted"
        >
          {{ description }}
        </DialogDescription>
        <slot />
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
