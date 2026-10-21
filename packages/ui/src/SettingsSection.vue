<script setup lang="ts">
defineProps<{ title?: string; description?: string }>()
</script>
<template>
  <section class="settings-section">
    <div
      v-if="title || description || $slots.actions"
      class="settings-section-heading"
    >
      <div>
        <h2 v-if="title">
          {{ title }}
        </h2><p
          v-if="description"
          class="muted"
        >
          {{ description }}
        </p>
      </div>
      <slot name="actions" />
    </div>
    <div class="settings-group">
      <slot />
    </div>
  </section>
</template>
