<script setup lang="ts">
import { computed, provide } from 'vue'
import MarkdownRender, { setCustomComponents, enableKatex, enableMermaid } from 'markstream-vue'
import 'katex/dist/katex.min.css'
import { configureMarkdown } from '../lib/markdown'
import { useAppStore } from '../stores/app'
import MarkdownLink from './MarkdownLink.vue'
import MarkdownImage from './MarkdownImage.vue'
const props = defineProps<{ content: string; sessionID: string; streaming?: boolean }>()
const app = useAppStore()
provide('markdownSession', computed(() => props.sessionID))
setCustomComponents('astral', { link: MarkdownLink, image: MarkdownImage })
enableKatex()
enableMermaid()
</script>
<template>
  <MarkdownRender
    custom-id="astral"
    html-policy="escape"
    mode="chat"
    :content="content"
    :custom-markdown-it="configureMarkdown"
    :is-dark="app.dark"
    :typewriter="false"
    :smooth-streaming="false"
    :fade="false"
    :batch-rendering="false"
    :viewport-priority="false"
    :defer-nodes-until-visible="false"
    :node-virtual="false"
    :show-tooltips="false"
    :final="!streaming"
    class="markdown"
  />
</template>
