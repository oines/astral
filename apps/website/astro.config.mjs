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
          page('Introduction', '认识 Astral', 'start/introduction'),
          page('Get Astral', '获取 Astral', 'start/get-astral'),
          page('First steps', '首次配置', 'start/first-steps'),
        ]),
        group('Your computers', '连接电脑', [
          page('Connect a computer', '连接电脑', 'computers/connect'),
          page('Containers', '使用容器', 'computers/containers'),
          page('Work across computers', '跨电脑工作', 'computers/across-computers'),
        ]),
        group('Using Astral', '日常使用', [
          page('Conversations', '会话', 'usage/conversations'),
          page('Files and results', '文件与结果', 'usage/files'),
          page('Models and search', '模型与搜索', 'usage/models'),
          page('Configure your Bot', '配置 Bot', 'usage/bots'),
          page('Skills and project guidance', '技能与项目说明', 'usage/skills'),
        ]),
        group('Chat platforms', '聊天平台', [
          page('Connect a chat', '接入聊天', 'chat/overview'),
          page('Work in group chats', '群聊协作', 'chat/groups'),
          page('Telegram', 'Telegram', 'chat/telegram'),
          page('QQ', 'QQ', 'chat/qq'),
          page('Weixin', '微信', 'chat/weixin'),
          page('Feishu', '飞书', 'chat/feishu'),
        ]),
        group('Self-hosting', '自行部署', [
          page('Choose how to run Astral', '选择运行方式', 'hosting/architecture'),
          page('Run a server', '运行 Server', 'hosting/server'),
          page('Sign-in and security', '登录与安全', 'hosting/security'),
          page('Command-line access', '命令行访问', 'hosting/command-line'),
          page('Updates and backups', '升级与备份', 'hosting/maintenance'),
          page('Troubleshooting', '排查问题', 'hosting/troubleshooting'),
        ]),
      ],
    }),
  ],
});
