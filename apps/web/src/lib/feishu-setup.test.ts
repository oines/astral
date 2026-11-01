import { describe, expect, it } from 'vitest'
import { feishuAppSettingsURL, feishuSetupGuideURL } from './feishu-setup'

describe('Feishu setup links', () => {
  it('opens the permission page for a complete App ID', () => {
    expect(feishuAppSettingsURL(' cli_a5ca35a685b0123e ')).toBe('https://open.feishu.cn/app/cli_a5ca35a685b0123e/auth')
  })

  it.each([undefined, '', 'cli_', 'app_secret_value', 'https://example.com', 'cli_app/auth', 'cli_app?secret=value', 'cli_app#fragment', 'cli_app%2fescape', 'cli_app\nsecret'])('keeps an invalid App ID %j out of the URL', value => {
    expect(feishuAppSettingsURL(value)).toBe('https://open.feishu.cn/app')
  })

  it('links to the setup guide in the current language', () => {
    expect(feishuSetupGuideURL('zh-CN')).toBe('https://astral.oines.dev/zh-cn/docs/chat/feishu/')
    expect(feishuSetupGuideURL('en')).toBe('https://astral.oines.dev/docs/chat/feishu/')
  })
})
