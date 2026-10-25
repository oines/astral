<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Button, SettingsRow, SettingsSection, Switch } from '@astral/ui'
import { Copy } from '@astral/icons'
import { useNetworkSettings } from '../../composables/useNetworkSettings'
import { useClipboard } from '../../composables/useClipboard'
import SettingsFeedback from './SettingsFeedback.vue'
const { t } = useI18n()
const { network, address, allowRemote, saveAddress, setAllowRemote } = useNetworkSettings()
const { copy, copied } = useClipboard()
function source(value: { source: string; editable: boolean }) {
  return t(value.editable ? 'networkSource' : 'networkSourceLocked', { source: t(`networkSources.${value.source}`) })
}
</script>
<template>
  <SettingsSection
    v-if="network"
    :title="t('networkAccess')"
  >
    <SettingsRow
      :label="t('allowRemoteManagement')"
      :description="`${t('allowRemoteManagementHint')} ${source(network.allow_remote_management)}`"
    >
      <Switch
        :model-value="allowRemote"
        :disabled="!network.allow_remote_management.editable"
        :label="t('allowRemoteManagement')"
        @update:model-value="setAllowRemote"
      />
    </SettingsRow>
    <SettingsFeedback scope="network/allow_remote_management" />
    <SettingsRow
      :label="t('networkAddress')"
      :description="`${t('networkAddressHint')} ${source(network.public_url)}`"
    >
      <input
        v-model="address"
        type="url"
        :readonly="!network.public_url.editable"
        :aria-label="t('networkAddress')"
        :placeholder="network.public_url.value"
        autocomplete="url"
        spellcheck="false"
        @blur="saveAddress"
        @keydown.enter.prevent="saveAddress"
      >
    </SettingsRow>
    <SettingsFeedback
      scope="network/public_url"
      :retry="saveAddress"
    />
    <SettingsRow
      :label="t('networkEffectiveAddress')"
      :description="t('networkAddressExistingHint')"
    >
      <span class="muted mono break-word">{{ network.public_url.value }}</span>
      <Button
        variant="ghost"
        size="icon"
        :aria-label="t(copied ? 'copied' : 'copy')"
        :title="t(copied ? 'copied' : 'copy')"
        @click="copy(network.public_url.value)"
      >
        <Copy />
      </Button>
    </SettingsRow>
  </SettingsSection>
</template>
