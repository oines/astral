<script setup lang="ts">
defineProps<{ label: string; description?: string }>()
</script>
<template>
  <div class="preference-row">
    <div class="preference-copy">
      <strong>{{ label }}</strong><p v-if="description">
        {{ description }}
      </p>
    </div>
    <div class="preference-control">
      <slot />
    </div>
  </div>
</template>
