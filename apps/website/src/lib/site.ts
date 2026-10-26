export type Locale = 'en' | 'zh-cn';
export const repository = 'https://github.com/oines/astral'
export const coreRepository = 'https://github.com/oines/astral-core'
export const homePath = (locale: Locale) => locale === 'en' ? '/' : '/zh-cn/'
export const docPath = (locale: Locale, page = 'start/introduction') =>
  `${locale === 'en' ? '' : '/zh-cn'}/docs/${page}/`
export function alternatePath(pathname: string): string {
  if (/^\/(?:zh-cn\/)?404(?:\.html)?\/?$/.test(pathname)) return pathname.startsWith('/zh-cn') ? '/' : '/zh-cn/'
  return pathname.startsWith('/zh-cn')
    ? pathname.replace(/^\/zh-cn/, '') || '/'
    : `/zh-cn${pathname}`
}
export const navigation = {
  en: { product: 'Product', docs: 'Documentation', get: 'Get Astral', github: 'GitHub', menu: 'Open navigation', close: 'Close navigation', language: 'Switch to Simplified Chinese', skip: 'Skip to content', theme: 'Toggle color theme' },
  'zh-cn': { product: '产品', docs: '文档', get: '获取 Astral', github: 'GitHub', menu: '打开导航', close: '关闭导航', language: 'Switch to English', skip: '跳转到正文', theme: '切换配色主题' },
}
