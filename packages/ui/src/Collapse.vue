<script setup lang="ts">
import { CollapsibleRoot, CollapsibleContent } from 'reka-ui'
defineProps<{ open: boolean }>()
</script>
<template>
  <CollapsibleRoot :open="open">
    <CollapsibleContent
      class="collapse-content"
      :inert="!open"
    >
      <slot />
    </CollapsibleContent>
  </CollapsibleRoot>
</template>
