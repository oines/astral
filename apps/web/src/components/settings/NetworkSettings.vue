<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Button, SettingsRow, SettingsSection, Switch } from '@astral/ui'
import { Copy } from '@astral/icons'
import { useNetworkSettings } from '../../composables/useNetworkSettings'
import { useClipboard } from '../../composables/useClipboard'
import SettingsFeedback from './SettingsFeedback.vue'
const { t } = useI18n()
const { network, addresses, allowRemote, setAddress, addAddress, removeAddress, saveAddresses, setAllowRemote } = useNetworkSettings()
const { copy, copied } = useClipboard()
function source(value: { source: string; editable: boolean }) {
  return value.editable ? '' : t('networkSourceLocked', { source: t(`networkSources.${value.source}`) })
}
function description(hint: string, value: { source: string; editable: boolean }) {
  return [t(hint), source(value)].filter(Boolean).join(' ')
}
</script>
<template>
  <SettingsSection
    v-if="network"
    :title="t('networkAccess')"
    class="network-settings"
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
    <SettingsRow
      :label="t('networkAddress')"
      :description="description('networkAddressHint', network.public_urls)"
    >
      <Button
        v-if="network.public_urls.editable"
        @click="addAddress"
      >
        {{ t('networkAddAddress') }}
      </Button>
    </SettingsRow>
    <div
      v-for="(address, index) in addresses"
      :key="index"
      class="resource-row network-address-row"
    >
      <div class="network-address-field">
        <input
          v-if="network.public_urls.editable"
          :value="address"
          type="url"
          :aria-label="t('networkAddressNumber', { number: index + 1 })"
          autocomplete="url"
          spellcheck="false"
          @input="setAddress(index, ($event.target as HTMLInputElement).value)"
          @blur="saveAddresses"
          @keydown.enter.prevent="saveAddresses"
        >
        <span
          v-else
          class="muted mono break-word"
        >{{ address }}</span>
        <small
          v-if="index === 0"
          class="muted"
        >{{ t('networkPrimaryAddress') }}</small>
      </div>
      <div class="row">
        <Button
          v-if="address"
          variant="ghost"
          size="icon"
          :aria-label="t(copied ? 'copied' : 'copy')"
          :title="t(copied ? 'copied' : 'copy')"
          @click="copy(address)"
        >
          <Copy />
        </Button>
        <Button
          v-if="network.public_urls.editable"
          variant="ghost"
          size="small"
          @mousedown.prevent
          @click="removeAddress(index)"
        >
          {{ t('delete') }}
        </Button>
      </div>
    </div>
    <SettingsFeedback
      scope="network/public_urls"
      :retry="saveAddresses"
    />
    <SettingsRow
      v-if="!addresses.length"
      :label="t('networkAutomatic')"
      :description="t('networkAddressExistingHint')"
    >
      <span class="muted mono break-word">{{ network.public_urls.value[0] }}</span>
      <Button
        v-if="network.public_urls.value[0]"
        variant="ghost"
        size="icon"
        :aria-label="t(copied ? 'copied' : 'copy')"
        :title="t(copied ? 'copied' : 'copy')"
        @click="copy(network.public_urls.value[0])"
      >
        <Copy />
      </Button>
    </SettingsRow>
  </SettingsSection>
</template>
