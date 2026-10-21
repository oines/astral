<script setup lang="ts">
import { onDeactivated } from 'vue'
import { PopoverRoot, PopoverTrigger, PopoverPortal, PopoverContent } from 'reka-ui'
withDefaults(defineProps<{ align?: 'start' | 'center' | 'end'; side?: 'top' | 'right' | 'bottom' | 'left'; label?: string }>(), { align: 'start', side: 'bottom', label: undefined })
const open = defineModel<boolean>('open', { default: false })
onDeactivated(() => { open.value = false })
</script>
<template>
  <PopoverRoot v-model:open="open">
    <PopoverTrigger as-child>
      <slot name="trigger" />
    </PopoverTrigger>
    <PopoverPortal>
      <PopoverContent
        class="popover"
        :side="side"
        :align="align"
        :side-offset="8"
        :collision-padding="12"
        :aria-label="label"
      >
        <slot :close="() => { open = false }" />
      </PopoverContent>
    </PopoverPortal>
  </PopoverRoot>
</template>
