<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
const props = defineProps<{ open: boolean }>()
const rendered = ref(props.open)
const expanded = ref(props.open)
let frame = 0
let frame2 = 0
let timer: ReturnType<typeof setTimeout> | undefined

function clearPending() {
  if (frame) { cancelAnimationFrame(frame); frame = 0 }
  if (frame2) { cancelAnimationFrame(frame2); frame2 = 0 }
  if (timer) { clearTimeout(timer); timer = undefined }
}

function reducedMotion() {
  return typeof window === 'undefined' || window.matchMedia?.('(prefers-reduced-motion: reduce)').matches === true
}

function finishClose() {
  clearPending()
  if (!props.open) rendered.value = false
}

watch(() => props.open, async open => {
  clearPending()
  if (open) {
    if (!rendered.value) {
      rendered.value = true
      if (reducedMotion()) { expanded.value = true; return }
      await nextTick()
      if (!props.open) { rendered.value = false; return }
      // Let the browser paint 0fr before transitioning to 1fr so the very first
      // disclosure animates smoothly instead of snapping open.
      frame = requestAnimationFrame(() => {
        frame = 0
        frame2 = requestAnimationFrame(() => {
          frame2 = 0
          if (props.open) expanded.value = true
        })
      })
    } else {
      expanded.value = true
    }
  } else {
    expanded.value = false
    if (reducedMotion()) { rendered.value = false; return }
    // Fallback in case transitionend does not fire (e.g. hidden tab or zero height).
    timer = setTimeout(finishClose, 260)
  }
})

function onTransitionEnd(event: TransitionEvent) {
  if (event.target === event.currentTarget && event.propertyName === 'grid-template-rows' && !props.open) finishClose()
}

onBeforeUnmount(clearPending)
</script>
<template>
  <div
    class="collapse-content"
    :data-state="expanded ? 'open' : 'closed'"
    :inert="!open"
    :aria-hidden="!open"
    @transitionend="onTransitionEnd"
  >
    <div class="collapse-inner">
      <slot v-if="rendered" />
    </div>
  </div>
</template>
