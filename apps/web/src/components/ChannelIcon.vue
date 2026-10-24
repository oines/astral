<script setup lang="ts">
import { PlatformIcon } from '@astral/icons'
defineProps<{ platform: string }>()
</script>
<template>
  <PlatformIcon :name="platform" />
</template>
