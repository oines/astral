<script setup lang="ts">
import { DialogRoot, DialogPortal, DialogOverlay, DialogContent, DialogTitle, DialogDescription, DialogClose } from 'reka-ui'
defineProps<{ open: boolean; title: string; description?: string }>()
defineEmits<{ 'update:open': [value: boolean] }>()
</script>
<template>
  <DialogRoot
    :open="open"
    @update:open="$emit('update:open', $event)"
  >
    <DialogPortal>
      <DialogOverlay class="modal-overlay" />
      <DialogContent
        class="modal-content"
        :aria-describedby="description ? undefined : ''"
      >
        <div class="row between">
          <DialogTitle class="modal-title">
            {{ title }}
          </DialogTitle>
          <DialogClose
            class="button button-ghost button-icon"
            aria-label="Close"
          >
            ×
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
