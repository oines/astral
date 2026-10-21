<script setup lang="ts">
import { computed } from 'vue'
import { Server } from 'lucide-vue-next'
import anthropic from '@lobehub/icons-static-svg/icons/anthropic.svg'
import azure from '@lobehub/icons-static-svg/icons/azure-color.svg'
import cerebras from '@lobehub/icons-static-svg/icons/cerebras-color.svg'
import cloudflare from '@lobehub/icons-static-svg/icons/cloudflare-color.svg'
import openai from '@lobehub/icons-static-svg/icons/openai.svg'
import deepseek from '@lobehub/icons-static-svg/icons/deepseek-color.svg'
import fireworks from '@lobehub/icons-static-svg/icons/fireworks-color.svg'
import githubcopilot from '@lobehub/icons-static-svg/icons/githubcopilot.svg'
import google from '@lobehub/icons-static-svg/icons/google-color.svg'
import groq from '@lobehub/icons-static-svg/icons/groq.svg'
import huggingface from '@lobehub/icons-static-svg/icons/huggingface-color.svg'
import lmstudio from '@lobehub/icons-static-svg/icons/lmstudio.svg'
import minimax from '@lobehub/icons-static-svg/icons/minimax-color.svg'
import mistral from '@lobehub/icons-static-svg/icons/mistral-color.svg'
import moonshot from '@lobehub/icons-static-svg/icons/moonshot.svg'
import newapi from '@lobehub/icons-static-svg/icons/newapi-color.svg'
import ollama from '@lobehub/icons-static-svg/icons/ollama.svg'
import openrouter from '@lobehub/icons-static-svg/icons/openrouter.svg'
import perplexity from '@lobehub/icons-static-svg/icons/perplexity-color.svg'
import bailian from '@lobehub/icons-static-svg/icons/bailian-color.svg'
import together from '@lobehub/icons-static-svg/icons/together-color.svg'
import grok from '@lobehub/icons-static-svg/icons/xai.svg'
import zai from '@lobehub/icons-static-svg/icons/zai.svg'
import zhipu from '@lobehub/icons-static-svg/icons/zhipu-color.svg'
import commandcode from '@lobehub/icons-static-svg/icons/commandcode.svg'
import opencode from '@lobehub/icons-static-svg/icons/opencode.svg'
import xiaomimimo from '@lobehub/icons-static-svg/icons/xiaomimimo.svg'
import kimi from '@lobehub/icons-static-svg/icons/kimi-color.svg'
const props = defineProps<{ name?: string }>()
const logos: Record<string, string> = { anthropic, azure, cerebras, cloudflare, openai, deepseek, fireworks, githubcopilot, google, groq, huggingface, lmstudio, minimax, mistral, moonshot, newapi, ollama, openrouter, perplexity, bailian, together, grok, zai, zhipu, commandcode, opencode, xiaomimimo, kimi }
const monochrome = new Set(['anthropic', 'openai', 'githubcopilot', 'groq', 'lmstudio', 'moonshot', 'ollama', 'openrouter', 'grok', 'zai', 'commandcode', 'opencode', 'xiaomimimo'])
const source = computed(() => logos[props.name || ''])
</script>
<template>
  <span
    v-if="source"
    class="provider-logo"
    aria-hidden="true"
  >
    <span
      v-if="monochrome.has(name || '')"
      class="provider-logo-mark"
      :style="{ maskImage: `url(${JSON.stringify(source)})` }"
    />
    <img
      v-else
      :src="source"
      alt=""
    >
  </span>
  <Server
    v-else
    class="provider-logo"
    aria-hidden="true"
  />
</template>
<style scoped>
.provider-logo { display: inline-flex; flex: 0 0 auto; }
.provider-logo > img, .provider-logo-mark { display: block; width: 100%; height: 100%; object-fit: contain; }
.provider-logo-mark { background: currentColor; mask-size: contain; mask-position: center; mask-repeat: no-repeat; }
</style>
