<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Button, SettingsRow, SettingsSection, Switch } from '@astral/ui'
import { useNetworkSettings } from '../../composables/useNetworkSettings'
import SettingsFeedback from './SettingsFeedback.vue'
const { t } = useI18n()
const { network, hostnames, allowRemote, restrictHostnames, showHostnames, hasHostnameDraft, hostnameSaveState, canEditHostnames, setHostname, addHostname, removeHostname, saveHostnameSettings, setRestrictHostnames, setAllowRemote } = useNetworkSettings((key, values) => t(key, values || {}))
function source(value: { source: string; editable: boolean }) {
  return value.editable ? '' : t('networkSourceLocked', { source: t(`networkSources.${value.source}`) })
}
function description(hint: string, value: { source: string; editable: boolean }) {
  return [t(hint), source(value)].filter(Boolean).join(' ')
}
</script>
<template>
  <template v-if="network">
    <SettingsSection
      v-if="network.allow_remote_management.source !== 'server'"
      :title="t('networkAccess')"
    >
      <SettingsRow
        :label="t('allowRemoteManagement')"
        :description="description('allowRemoteManagementHint', network.allow_remote_management)"
      >
        <Switch
          :model-value="allowRemote"
          :disabled="!network.allow_remote_management.editable"
          :label="t('allowRemoteManagement')"
          @update:model-value="setAllowRemote"
        />
      </SettingsRow>
      <SettingsFeedback scope="network/allow_remote_management" />
    </SettingsSection>
    <SettingsSection
      :title="t('networkHostnameSettings')"
      class="network-settings"
    >
      <SettingsRow
        :label="t('networkRestrictHostnames')"
        :description="description('networkRestrictHostnamesHint', network.restrict_hostnames)"
      >
        <Switch
          :model-value="restrictHostnames"
          :disabled="!network.restrict_hostnames.editable"
          :label="t('networkRestrictHostnames')"
          @update:model-value="setRestrictHostnames"
        />
      </SettingsRow>
      <template v-if="showHostnames">
        <SettingsRow
          :label="t('networkAllowedHostnames')"
          :description="description('networkAllowedHostnamesHint', network.allowed_hostnames)"
        >
          <Button
            v-if="network.allowed_hostnames.editable"
            @click="addHostname"
          >
            {{ t('networkAddHostname') }}
          </Button>
        </SettingsRow>
        <div
          v-for="(hostname, index) in hostnames"
          :key="index"
          class="resource-row network-hostname-row"
        >
          <div class="network-hostname-field">
            <input
              v-if="network.allowed_hostnames.editable"
              :value="hostname"
              type="text"
              :aria-label="t('networkHostnameNumber', { number: index + 1 })"
              :placeholder="t('networkHostnameExample')"
              autocomplete="off"
              autocapitalize="none"
              spellcheck="false"
              @input="setHostname(index, ($event.target as HTMLInputElement).value)"
            >
            <span
              v-else
              class="muted mono break-word"
            >{{ hostname }}</span>
          </div>
          <div class="row">
            <Button
              v-if="network.allowed_hostnames.editable"
              variant="ghost"
              size="small"
              :aria-label="t('networkRemoveHostname', { hostname: hostname || index + 1 })"
              @click="removeHostname(index)"
            >
              {{ t('delete') }}
            </Button>
          </div>
        </div>
      </template>
      <div
        v-if="canEditHostnames"
        class="settings-submit-row"
      >
        <p>{{ t('networkHostnameDraftHint') }}</p>
        <Button
          :disabled="!hasHostnameDraft || hostnameSaveState.status === 'saving' || hostnameSaveState.status === 'uncertain'"
          @click="saveHostnameSettings"
        >
          {{ t('networkSaveHostnameSettings') }}
        </Button>
      </div>
      <SettingsFeedback
        scope="network/hostnames"
        :retry="saveHostnameSettings"
      />
      <SettingsRow
        v-if="showHostnames"
        :label="t('networkHostnameRecovery')"
        :description="t('networkHostnameRecoveryHint')"
      >
        <code class="small break-word">astral config hostnames disable</code>
      </SettingsRow>
    </SettingsSection>
  </template>
</template>
