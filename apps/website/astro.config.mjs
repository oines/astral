import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

const site = process.env.SITE_URL?.trim() || undefined;
if (site) {
  const url = new URL(site);
  if (url.protocol !== 'https:' || url.pathname !== '/' || url.search || url.hash || url.username || url.password) {
    throw new Error('SITE_URL must be an HTTPS origin without a path, query, fragment, or credentials.');
  }
}
const page = (label, zh, slug) => ({ label, translations: { 'zh-CN': zh }, slug: `docs/${slug}` });
const group = (label, zh, items) => ({ label, translations: { 'zh-CN': zh }, items });

export default defineConfig({
  ...(site ? { site } : {}),
  trailingSlash: 'always',
  redirects: {
    '/docs/': '/docs/start/introduction/',
    '/zh-cn/docs/': '/zh-cn/docs/start/introduction/',
    ...Object.fromEntries(['', '/zh-cn'].flatMap(prefix => [
      [`${prefix}/docs/developers/architecture/`, `${prefix}/docs/hosting/architecture/`],
      [`${prefix}/docs/developers/api/`, `${prefix}/docs/hosting/command-line/`],
      [`${prefix}/docs/developers/contributing/`, `${prefix}/docs/usage/skills/`],
    ])),
  },
  integrations: [
    starlight({
      title: 'Astral',
      description: 'One agent. All your computers. Documentation for the open-source personal agent.',
      favicon: '/brand/astral-mark.svg',
      head: [
        {
          tag: 'meta',
          attrs: {
            property: 'og:image',
            content: site ? new URL('/brand/og-en.png', site).href : '/brand/og-en.png',
          },
        },
        {
          tag: 'meta',
          attrs: { name: 'twitter:card', content: 'summary_large_image' },
        },
      ],
      locales: {
        root: { label: 'English', lang: 'en' },
        'zh-cn': { label: '简体中文', lang: 'zh-CN' },
      },
      social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/oines/astral' }],
      customCss: ['./src/styles/docs.css'],
      components: {
        Header: './src/components/DocsHeader.astro',
        MobileMenuFooter: './src/components/DocsMobileMenuFooter.astro',
      },
      pagination: true,
      sidebar: [
        group('Getting started', '开始使用', [
          page('What Astral can do', 'Astral 能帮你做什么', 'start/introduction'),
          page('Download and install', '下载与安装', 'start/get-astral'),
          page('Your first task', '完成第一个任务', 'start/first-steps'),
        ]),
        group('Using Astral', '日常使用', [
          page('Sessions and subagents', '使用会话与子代理', 'usage/conversations'),
          page('Files and results', '文件与结果', 'usage/files'),
          page('Schedules and events', '计划任务与事件唤醒', 'usage/schedules'),
          page('Configure Bots', '配置 Bot', 'usage/bots'),
          page('Models and web search', '模型与网页搜索', 'usage/models'),
          page('Skills and AGENTS.md', '技能与项目说明', 'usage/skills'),
          page('Permissions and privacy', '权限与隐私', 'start/safe-use'),
        ]),
        group('Chat platforms', '聊天平台', [
          page('Connect a chat', '接入聊天', 'chat/overview'),
          page('Group chats', '群聊协作', 'chat/groups'),
          page('Telegram', 'Telegram', 'chat/telegram'),
          page('QQ', 'QQ', 'chat/qq'),
          page('Weixin', '微信', 'chat/weixin'),
          page('Feishu', '飞书', 'chat/feishu'),
        ]),
        group('Computers', '电脑与运行环境', [
          page('Connect computers', '连接电脑', 'computers/connect'),
          page('Containers', '在容器中工作', 'computers/containers'),
          page('Work across computers', '跨电脑工作', 'computers/across-computers'),
        ]),
        group('Self-hosting', '自行部署', [
          page('Desktop or Server?', '选桌面版还是 Server', 'hosting/architecture'),
          page('Run a Server', '运行 Server', 'hosting/server'),
          page('Sign-in and security', '登录与安全', 'hosting/security'),
          page('Updates and backups', '升级与备份', 'hosting/maintenance'),
          page('Troubleshooting', '排查问题', 'hosting/troubleshooting'),
        ]),
        group('Advanced reference', '进阶参考', [
          page('Server options', '定制服务器部署', 'hosting/server-options'),
          page('NapCat networking', 'NapCat 地址与网络', 'chat/napcat-network'),
          page('Command-line access', '使用命令行', 'hosting/command-line'),
          page('Bot API', 'Bot API 接入', 'hosting/bot-api'),
          page('Remote sessions with ACP', '通过 ACP 接入会话', 'hosting/acp'),
          page('Platform APIs', '平台 API', 'chat/platform-api'),
          page('Build from source', '从源码构建', 'hosting/build-from-source'),
        ]),
      ],
    }),
  ],
});
