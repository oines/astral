<script setup lang="ts">
import Markdown from '../../Markdown.vue'
import type { FilePreviewResource } from '../../../composables/useFileService'
defineProps<{ preview: FilePreviewResource; sessionID: string }>()
</script>
<template>
  <Markdown
    :content="preview.content || ''"
    :session-i-d="sessionID"
  />
</template>
