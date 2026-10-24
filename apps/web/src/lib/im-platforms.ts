// Presentation metadata for the concrete adapters implemented by the server.
// Credentials remain in the existing Connection secret, never in config.
export const imAdapters = [
  { id: 'telegram.bot', platform: 'telegram', name: 'Telegram Bot', hint: '', secret: 'botToken', fields: [{ key: 'base_url', label: 'botServerAddress', placeholder: 'https://api.telegram.org' }] },
  { id: 'qq.bot', platform: 'qq', name: 'QQ 官方 Bot', hint: 'qqOfficialHint', secret: 'appSecret', fields: [{ key: 'app_id', label: 'appID', placeholder: 'App ID', required: true }, { key: 'base_url', label: 'botServerAddress', placeholder: 'https://api.sgroup.qq.com' }, { key: 'token_url', label: 'tokenEndpoint', placeholder: 'https://bots.qq.com/app/getAppAccessToken' }] },
  { id: 'qq.napcat', platform: 'qq', name: 'QQ NapCat', hint: 'napcatHint', secret: 'accessToken', fields: [{ key: 'base_url', label: 'napcatHTTP', placeholder: 'http://127.0.0.1:3000', required: true }, { key: 'websocket_url', label: 'napcatWebSocket', placeholder: 'ws://127.0.0.1:3001', required: true }] },
  { id: 'weixin.ilink', platform: 'weixin', name: 'ClawBot / iLink', hint: 'weixinPlatformHint', secret: '', fields: [] },
  { id: 'feishu.bot', platform: 'feishu', name: '飞书 Bot', hint: 'feishuPlatformHint', secret: 'appSecret', fields: [{ key: 'app_id', label: 'appID', placeholder: 'cli_…', required: true }, { key: 'base_url', label: 'botServerAddress', placeholder: 'https://open.feishu.cn' }] },
]
export function adapterInfo(id: string) { return imAdapters.find(a => a.id === id) }
export function adapterLabel(id: string, locale = 'zh') {
  if (id === 'qq.bot') return locale.startsWith('zh') ? 'QQ 官方 Bot' : 'QQ Official Bot'
  if (id === 'feishu.bot') return locale.startsWith('zh') ? '飞书 Bot' : 'Feishu Bot'
  return adapterInfo(id)?.name || id
}
export function platformLabel(platform: string, locale = 'zh') {
  return ({ telegram: 'Telegram', qq: 'QQ', weixin: locale.startsWith('zh') ? '微信' : 'Weixin', feishu: locale.startsWith('zh') ? '飞书' : 'Feishu' })[platform] || platform
}
export function chatIDHint(adapter: string) {
  return ({ 'telegram.bot': '-100… / user ID', 'qq.bot': 'private:openid / group:openid', 'qq.napcat': 'private:123456 / group:123456', 'weixin.ilink': '…@im.wechat', 'feishu.bot': 'oc_…' })[adapter] || ''
}
