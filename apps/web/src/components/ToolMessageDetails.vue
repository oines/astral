<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Entry } from '../lib/types'
import { object } from '../lib/api'
import { sentMessageDetails } from '../lib/tool-presentation'

const props = defineProps<{ entry: Entry }>()
const { t } = useI18n()
const detail = computed(() => sentMessageDetails(props.entry))
const targetName = computed(() => String(detail.value.target.title || detail.value.target.conversation_title || detail.value.target.conversation_id || t('toolMessageTargetUnavailable')))
const templateParams = computed(() => Array.isArray(detail.value.template.params) ? detail.value.template.params.map(object) : [])
</script>
<template>
  <div class="tool-semantic-detail">
    <dl class="tool-facts">
      <dt>{{ t('toolMessageTarget') }}</dt><dd>{{ targetName }}</dd>
      <template v-if="detail.target.connection_name">
        <dt>{{ t('toolMessageAccount') }}</dt><dd>{{ detail.target.connection_name }}</dd>
      </template>
      <template v-if="detail.replyTo">
        <dt>{{ t('toolMessageReply') }}</dt><dd>{{ detail.replyTo }}</dd>
      </template>
      <template v-if="detail.title">
        <dt>{{ t('toolMessageTitle') }}</dt><dd>{{ detail.title }}</dd>
      </template>
      <template v-if="detail.format">
        <dt>{{ t('toolMessageFormat') }}</dt><dd>{{ detail.format }}</dd>
      </template>
      <template v-if="detail.silent">
        <dt>{{ t('toolMessageNotification') }}</dt><dd>{{ t('toolMessageSilent') }}</dd>
      </template>
      <template v-if="detail.asDocument">
        <dt>{{ t('toolMessageAttachmentMode') }}</dt><dd>{{ t('toolMessageAsDocument') }}</dd>
      </template>
    </dl>
    <div v-if="detail.text">
      <p class="tool-detail-label">
        {{ t('toolMessageBody') }}
      </p>
      <p class="tool-detail-text">
        {{ detail.text }}
      </p>
    </div>
    <div v-if="detail.parts.length">
      <p class="tool-detail-label">
        {{ t('toolMessageBody') }}
      </p>
      <div class="tool-message-parts">
        <template
          v-for="(part, index) in detail.parts"
          :key="index"
        >
          <span
            v-if="part.type === 'text'"
            class="tool-detail-text"
          >{{ part.text }}</span>
          <span
            v-else-if="part.type === 'mention'"
            class="tool-message-mention"
          >@{{ part.name || part.user_id }}<span
            v-if="part.name"
            class="tool-detail-secondary"
          > ({{ part.user_id }})</span></span>
          <span
            v-else
            class="tool-message-file"
          >{{ t(part.type === 'image' ? 'toolMessageImage' : 'toolMessageFile') }}: <code>{{ part.path || part.file_uri }}</code></span>
        </template>
      </div>
    </div>
    <div v-if="detail.attachments.length">
      <p class="tool-detail-label">
        {{ t('toolMessageAttachments') }}
      </p>
      <ul class="tool-attachment-list">
        <li
          v-for="(attachment, index) in detail.attachments"
          :key="index"
        >
          <code>{{ attachment }}</code>
        </li>
      </ul>
    </div>
    <div v-if="detail.template.custom_template_id">
      <p class="tool-detail-label">
        {{ t('toolMessageTemplate') }} {{ detail.template.custom_template_id }}
      </p>
      <dl class="tool-facts">
        <template
          v-for="(param, index) in templateParams"
          :key="index"
        >
          <dt>{{ param.key }}</dt><dd class="tool-detail-text">
            {{ Array.isArray(param.values) ? param.values.join('\n') : '' }}
          </dd>
        </template>
      </dl>
    </div>
    <p
      v-if="detail.status"
      :class="{ 'text-danger': detail.error }"
    >
      {{ t(detail.status) }}
    </p>
    <div v-if="detail.fallback && !detail.error">
      <p class="tool-detail-label">
        {{ t('result') }}
      </p>
      <p class="tool-detail-text">
        {{ detail.fallback }}
      </p>
    </div>
    <p
      v-if="detail.error"
      class="tool-detail-text text-danger"
    >
      {{ detail.error }}
    </p>
    <details
      v-if="detail.target.binding_id || detail.messageIDs.length"
      class="tool-identifiers"
    >
      <summary>{{ t('toolIdentifiers') }}</summary>
      <dl class="tool-facts">
        <template v-if="detail.target.conversation_id && targetName !== detail.target.conversation_id">
          <dt>{{ t('toolMessageConversationID') }}</dt><dd>{{ detail.target.conversation_id }}</dd>
        </template>
        <template v-if="detail.target.binding_id">
          <dt>{{ t('toolMessageBindingID') }}</dt><dd>{{ detail.target.binding_id }}</dd>
        </template>
        <template v-if="detail.messageIDs.length">
          <dt>{{ t('toolMessageReceipts') }}</dt><dd>{{ detail.messageIDs.join(', ') }}</dd>
        </template>
      </dl>
    </details>
  </div>
</template>
