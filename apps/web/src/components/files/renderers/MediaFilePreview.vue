<script setup lang="ts">
import type { FilePreviewResource } from '../../../composables/useFileService'
defineProps<{ preview: FilePreviewResource }>()
</script>
<template>
  <video
    v-if="preview.mediaType.startsWith('video/')"
    :src="preview.url"
    controls
    preload="metadata"
  />
  <audio
    v-else
    :src="preview.url"
    controls
    preload="metadata"
  />
</template>
