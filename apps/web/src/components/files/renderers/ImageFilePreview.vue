<script setup lang="ts">
import type { FilePreviewResource } from '../../../composables/useFileService'
defineProps<{ preview: FilePreviewResource }>()
</script>
<template>
  <img
    :src="preview.url"
    class="file-preview-image"
    :alt="preview.name"
  >
</template>
