<script setup lang="ts">
import type { FilePreviewResource } from '../../../composables/useFileService'
defineProps<{ preview: FilePreviewResource }>()
</script>
<template>
  <iframe
    :src="preview.url"
    class="pdf-preview"
    :title="preview.name"
  />
</template>
