<script setup lang="ts">
import { ref, watch } from 'vue'
const props = defineProps<{ open: boolean }>()
const rendered = ref(props.open)
watch(() => props.open, open => { if (open) rendered.value = true })
</script>
<template>
  <div
    class="collapse-content"
    :data-state="open ? 'open' : 'closed'"
    :inert="!open"
    :aria-hidden="!open"
  >
    <div class="collapse-inner">
      <slot v-if="rendered" />
    </div>
  </div>
</template>
