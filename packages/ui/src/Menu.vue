<script setup lang="ts">
import { onDeactivated } from 'vue'
import { DropdownMenuRoot, DropdownMenuTrigger, DropdownMenuPortal, DropdownMenuContent } from 'reka-ui'
defineOptions({ inheritAttrs: false })
withDefaults(defineProps<{ align?: 'start' | 'center' | 'end'; side?: 'top' | 'right' | 'bottom' | 'left'; sideOffset?: number; label: string }>(), { align: 'start', side: 'bottom', sideOffset: 8 })
const open = defineModel<boolean>('open', { default: false })
onDeactivated(() => { open.value = false })
</script>
<template>
  <DropdownMenuRoot v-model:open="open">
    <DropdownMenuTrigger as-child>
      <slot name="trigger" />
    </DropdownMenuTrigger>
    <DropdownMenuPortal>
      <DropdownMenuContent
        v-bind="$attrs"
        class="popover"
        :side="side"
        :align="align"
        :side-offset="sideOffset"
        :collision-padding="8"
        :aria-label="label"
      >
        <slot />
      </DropdownMenuContent>
    </DropdownMenuPortal>
  </DropdownMenuRoot>
</template>
