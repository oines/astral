import { describe, expect, it } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import { channelsEn, channelsZh } from '../../i18n/channels'
import IMConnectionFields from './IMConnectionFields.vue'

function render(creating: boolean, adapter = 'feishu.bot', locale = 'zh-CN') {
  const app = createSSRApp(IMConnectionFields, {
    adapter, creating, values: { app_id: 'cli_example' }, token: 'secret-kept-in-draft',
  })
  app.use(createI18n({ legacy: false, locale, messages: { en: channelsEn, 'zh-CN': channelsZh } }))
  return renderToString(app)
}

describe('platform connection setup links', () => {
  it.each([true, false])('offers both links in the shared Feishu form when creating=%s', async creating => {
    const html = await render(creating)
    expect(html).toContain('配置飞书应用')
    expect(html).toContain('查看配置步骤')
    const links = [...html.matchAll(/<a\b[^>]*>/g)].map(match => match[0])
    expect(links).toHaveLength(2)
    expect(links[0]).toContain('href="https://open.feishu.cn/app/cli_example/auth"')
    expect(links[1]).toContain('href="https://astral.oines.dev/zh-cn/docs/chat/feishu/"')
    for (const link of links) {
      expect(link).toContain('target="_blank"')
      expect(link).toContain('rel="noopener noreferrer"')
      expect(link).not.toContain('secret-kept-in-draft')
    }
    expect(html).toContain('value="cli_example"')
    expect(html).toContain('value="secret-kept-in-draft"')
  })

  it('uses the English guide with the English form', async () => {
    expect(await render(true, 'feishu.bot', 'en')).toContain('href="https://astral.oines.dev/docs/chat/feishu/"')
  })

  it('keeps Feishu links out of other platform forms', async () => {
    expect(await render(true, 'telegram.bot')).not.toContain('open.feishu.cn')
  })
})
