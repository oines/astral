<script setup lang="ts">
import { computed, provide } from 'vue'
import MarkdownRender, { setCustomComponents, enableKatex, enableMermaid } from 'markstream-vue'
import { useAppStore } from '../stores/app'
import MarkdownLink from './MarkdownLink.vue'
const props = defineProps<{ content: string; sessionID: string; streaming?: boolean }>()
const app = useAppStore()
provide('markdownSession', computed(() => props.sessionID))
setCustomComponents('astral', { link: MarkdownLink })
enableKatex()
enableMermaid()
</script>
<template>
  <MarkdownRender
    custom-id="astral"
    html-policy="escape"
    :content="content"
    :is-dark="app.dark"
    :typewriter="false"
    :fade="false"
    :show-tooltips="false"
    :final="!streaming"
    class="markdown"
  />
</template>
