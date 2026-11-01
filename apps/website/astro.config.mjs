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
          page('What Astral can do', 'Astral 能帮你做什么', 'start/introduction'),
          page('Download and install', '下载与安装', 'start/get-astral'),
          page('Before your first task', '开始前准备什么', 'start/first-steps'),
        ]),
        group('Using Astral', '日常使用', [
          page('Work in conversations', '交代任务与继续工作', 'usage/conversations'),
          page('Work with files', '上传文件并取回结果', 'usage/files'),
          page('Schedule a task', '安排定时任务', 'usage/schedules'),
          page('Configure your assistant', '配置自己的 Bot', 'usage/bots'),
          page('Models and web search', '连接模型与网页搜索', 'usage/models'),
          page('Reusable instructions', '技能与项目说明', 'usage/skills'),
          page('Permissions and privacy', '权限与隐私', 'start/safe-use'),
        ]),
        group('Chat platforms', '聊天平台', [
          page('Connect a chat', '接入聊天', 'chat/overview'),
          page('Work in group chats', '群聊协作', 'chat/groups'),
          page('Telegram', 'Telegram', 'chat/telegram'),
          page('QQ', 'QQ', 'chat/qq'),
          page('Weixin', '微信', 'chat/weixin'),
          page('Feishu', '飞书', 'chat/feishu'),
        ]),
        group('Computers', '电脑与运行环境', [
          page('Connect another computer', '连接另一台电脑', 'computers/connect'),
          page('Use a container', '在容器中工作', 'computers/containers'),
          page('Work across computers', '跨电脑完成任务', 'computers/across-computers'),
        ]),
        group('Self-hosting', '自行部署', [
          page('Choose how to run Astral', '选择运行方式', 'hosting/architecture'),
          page('Run a server', '运行 Server', 'hosting/server'),
          page('Sign-in and security', '登录与安全', 'hosting/security'),
          page('Updates and backups', '升级与备份', 'hosting/maintenance'),
          page('Troubleshooting', '排查问题', 'hosting/troubleshooting'),
        ]),
        group('Advanced reference', '进阶参考', [
          page('Server options', '定制服务器部署', 'hosting/server-options'),
          page('NapCat networking', 'NapCat 地址与网络', 'chat/napcat-network'),
          page('Command-line access', '使用命令行', 'hosting/command-line'),
          page('Platform APIs', '平台 API', 'chat/platform-api'),
          page('Build from source', '从源码构建', 'hosting/build-from-source'),
        ]),
      ],
    }),
  ],
});
