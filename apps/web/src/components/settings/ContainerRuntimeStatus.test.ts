import { describe, expect, it } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import type { AstralInternalDomainHostInfo as HostInfo } from '@astral/sdk'
import { computersEn, computersZh } from '../../i18n/computers'
import ContainerRuntimeStatus from './ContainerRuntimeStatus.vue'

function render(status: HostInfo['container_status'], locale = 'zh', error = '') {
  const host: HostInfo = { hostname: 'server', os: 'linux', arch: 'amd64', container_available: status === 'available', container_status: status, container_user: 'actual-service-account', container_error: status === 'available' ? undefined : 'original Docker diagnostic: /run/user/1000/docker.sock' }
  const app = createSSRApp(ContainerRuntimeStatus, { host, checking: false, error })
  app.use(createI18n({ legacy: false, locale, messages: { en: computersEn, zh: computersZh } }))
  return renderToString(app)
}

describe('container runtime explanations', () => {
  it('identifies the actual Server account for a permission error and retains the raw diagnostic', async () => {
    const html = await render('permission_denied')
    expect(html).toContain('Astral Server 使用「actual-service-account」账号运行')
    expect(html).toContain('请为该账号配置 Docker 访问权限')
    expect(html).toContain('错误详情')
    expect(html).toContain('original Docker diagnostic: /run/user/1000/docker.sock')
  })

  it.each([
    ['missing_cli', 'Server 找不到 Docker 命令'],
    ['not_running', '所选 Docker 引擎未运行或无法连接'],
    ['error', '无法检测 Docker'],
  ] as const)('explains %s with a recheck and setup guide', async (status, explanation) => {
    const html = await render(status)
    expect(html).toContain(explanation)
    expect(html).toContain('重新检测')
    expect(html).toContain('href="https://astral.oines.dev/zh-cn/docs/computers/containers/"')
    expect(html).toContain('<details>')
    expect(html).toContain('original Docker diagnostic: /run/user/1000/docker.sock')
  })

  it('offers the English explanation and guide when using English', async () => {
    const html = await render('permission_denied', 'en')
    expect(html).toContain('Astral Server runs as “actual-service-account”')
    expect(html).toContain('href="https://astral.oines.dev/docs/computers/containers/"')
  })

  it('removes failure and success chatter after a check finds an existing usable Docker engine', async () => {
    const html = await render('available')
    expect(html).not.toContain('Docker')
    expect(html).not.toContain('重新检测')
    expect(html).not.toContain('错误详情')
  })

  it('distinguishes a failed Server request from a Docker access error', async () => {
    const html = await render('permission_denied', 'zh', 'Connection interrupted')
    expect(html).toContain('请检查与 Server 的连接后重试')
    expect(html).toContain('Connection interrupted')
    expect(html).not.toContain('请为该账号配置 Docker 访问权限')
  })
})
