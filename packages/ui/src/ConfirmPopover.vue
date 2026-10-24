<script setup lang="ts">
import { onDeactivated, ref, useId } from 'vue'
import { PopoverRoot, PopoverTrigger, PopoverAnchor, PopoverPortal, PopoverContent, PopoverArrow } from 'reka-ui'
import Button from './Button.vue'
const props = withDefaults(defineProps<{
  title: string
  description?: string
  confirmLabel: string
  cancelLabel: string
  busy?: boolean
  disabled?: boolean
  error?: string
  variant?: 'danger' | 'primary'
  align?: 'start' | 'center' | 'end'
  side?: 'top' | 'right' | 'bottom' | 'left'
  anchor?: HTMLElement
}>(), { description: undefined, busy: false, disabled: false, error: undefined, variant: 'danger', align: 'end', side: 'bottom', anchor: undefined })
const open = defineModel<boolean>('open', { default: false })
defineEmits<{ confirm: [] }>()
const titleID = useId()
const descriptionID = useId()
const content = ref<HTMLElement>()
function setOpen(value: boolean) { if (!props.busy) open.value = value }
function focusCancel(event: Event) {
  event.preventDefault()
  content.value?.querySelector<HTMLButtonElement>('[data-confirm-cancel]')?.focus({ preventScroll: true })
}
function dismiss(event: Event) { if (props.busy) event.preventDefault() }
function restoreFocus(event: Event) {
  if (props.anchor?.isConnected) { event.preventDefault(); props.anchor.focus({ preventScroll: true }) }
}
onDeactivated(() => { open.value = false })
</script>
<template>
  <PopoverRoot
    :open="open"
    @update:open="setOpen"
  >
    <PopoverAnchor
      v-if="anchor"
      :reference="anchor"
      as="span"
    />
    <PopoverTrigger
      v-else-if="$slots.trigger"
      as-child
    >
      <slot name="trigger" />
    </PopoverTrigger>
    <PopoverPortal>
      <PopoverContent
        as-child
        :side="side"
        :align="align"
        :side-offset="8"
        :collision-padding="12"
        @open-auto-focus="focusCancel"
        @close-auto-focus="restoreFocus"
        @interact-outside="dismiss"
        @escape-key-down="dismiss"
      >
        <div
          ref="content"
          class="popover confirm-popover"
          :aria-labelledby="titleID"
          :aria-describedby="description ? descriptionID : undefined"
        >
          <h3
            :id="titleID"
            class="confirm-popover-title"
          >
            {{ title }}
          </h3>
          <p
            v-if="description"
            :id="descriptionID"
            class="confirm-popover-description"
          >
            {{ description }}
          </p>
          <slot />
          <p
            v-if="error"
            class="confirm-popover-error"
            role="alert"
          >
            {{ error }}
          </p>
          <div class="confirm-popover-actions">
            <Button
              data-confirm-cancel
              size="small"
              variant="ghost"
              :disabled="busy"
              @click="setOpen(false)"
            >
              {{ cancelLabel }}
            </Button>
            <Button
              size="small"
              :variant="variant"
              :busy="busy"
              :disabled="disabled"
              @click="$emit('confirm')"
            >
              {{ confirmLabel }}
            </Button>
          </div>
          <PopoverArrow class="confirm-popover-arrow" />
        </div>
      </PopoverContent>
    </PopoverPortal>
  </PopoverRoot>
</template>
