<script setup lang="ts">
import { computed } from 'vue'
import { MessageCircle } from 'lucide-vue-next'
import telegram from './assets/telegram.svg'
import qq from './assets/qq.svg'
import weixin from './assets/wechat.svg'
import feishu from './assets/feishu.svg'

const props = defineProps<{ name: string }>()
const platforms: Record<string, { source: string; label: string }> = {
  telegram: { source: telegram, label: 'Telegram' },
  qq: { source: qq, label: 'QQ' },
  weixin: { source: weixin, label: 'Weixin' },
  feishu: { source: feishu, label: 'Feishu' },
}
const platform = computed(() => platforms[props.name])
</script>
<template>
  <img
    v-if="platform"
    class="platform-icon"
    :src="platform.source"
    :alt="platform.label"
  >
  <MessageCircle
    v-else
    class="platform-icon"
    :aria-label="name"
  />
</template>
<style scoped>
.platform-icon { display: block; flex: 0 0 auto; width: 16px; height: 16px; object-fit: contain; }
</style>
