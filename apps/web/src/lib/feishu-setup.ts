export function feishuAppSettingsURL(value?: string): string {
  const appID = value?.trim() || ''
  return /^cli_[a-zA-Z0-9]+$/.test(appID)
    ? `https://open.feishu.cn/app/${encodeURIComponent(appID)}/auth`
    : 'https://open.feishu.cn/app'
}

export function feishuSetupGuideURL(locale: string): string {
  return locale.startsWith('zh')
    ? 'https://astral.oines.dev/zh-cn/docs/chat/feishu/'
    : 'https://astral.oines.dev/docs/chat/feishu/'
}
