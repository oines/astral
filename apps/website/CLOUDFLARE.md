# Cloudflare Pages 部署

官网和用户文档作为一个静态 Pages 项目发布，无需 Functions、数据库或运行 Astral Server。正式项目为 `astral-website`，域名为 [astral.oines.dev](https://astral.oines.dev/)，默认 Pages 地址为 [astral-website.pages.dev](https://astral-website.pages.dev/)。项目使用 `oines/astral` 仓库，生产分支为 `main`。

## Pages 项目配置

在 Cloudflare 的 Workers & Pages 中打开 `astral-website`。以下是项目的构建配置；重新创建项目时，使用 **Pages → Connect to Git** 并关联 `oines/astral`。

| 设置 | 值 |
| --- | --- |
| 项目名称 | `astral-website`；如改名，同步修改根目录 `wrangler.jsonc` |
| 正式分支 | `main` |
| 根目录 | 仓库根目录，留空 |
| 框架预设 | None，使用下方命令 |
| 构建命令 | `pnpm --filter astral-website... install --frozen-lockfile && pnpm check:website` |
| 输出目录 | `apps/website/dist` |
| 构建系统 | v3（或支持 monorepo 的 v2 以上版本） |

命令先按根锁文件安装网站依赖，再检查并构建网站，不执行产品的发行构建。根目录的 `wrangler.jsonc` 使用相同输出路径，无需再加 Astro Cloudflare SSR adapter。

在 **Git 存储库 → 管理** 中确认现有 **Cloudflare Workers and Pages** GitHub App 已获准访问 `oines/astral`。如果使用 Only select repositories，将 Astral 加入所选仓库即可。公开仓库能够被 Pages 拉取并构建，不代表 GitHub App 已有接收该仓库推送事件的权限；自动部署必须以一次实际推送产生的新部署记录验证。

## 构建环境变量

根目录的 `wrangler.jsonc` 是 Pages 配置的来源。Pages 检测到其中的 `pages_build_output_dir` 后，会从该文件读取构建变量，不能只在 Dashboard 中设置同名变量。以下公共变量维护在顶层 `vars`，用于本地 Pages 开发和 Preview：

| 变量 | 值 | 用途 |
| --- | --- | --- |
| `NODE_VERSION` | `26.7.0` | 与本地和 CI 一致 |
| `PNPM_VERSION` | `10.31.0` | 与根 `packageManager` 一致 |
| `SKIP_DEPENDENCY_INSTALL` | `1` | 禁用默认全仓安装，使用构建命令中的过滤安装 |
| `HUSKY` | `0` | 构建环境无需 Git hooks |

`env.production.vars` 包含上述完整公共变量，并额外设置 `SITE_URL=https://astral.oines.dev`，不带子路径。`vars` 按整组覆盖，不会把生产环境的值与顶层逐项合并，因此这里必须保留公共变量。没有单独定义 `env.preview`，Preview 使用顶层变量，不包含 `SITE_URL`。

预览产物的 `robots.txt` 禁止索引，正式产物生成 canonical、语言链接和 sitemap。更换正式域名后，修改 `env.production.vars.SITE_URL` 并重新构建；该变量不会替你配置 DNS 或绑定域名。正式域名仍需在 Pages 项目的 Custom domains 中绑定。

部署后核对实际构建日志中的变量和 Node/pnpm 版本，再检查正式站点的 canonical、`robots.txt` 和 sitemap；Dashboard 中存在变量不代表该次构建已使用它们。本地直接运行 `pnpm check:website` 不会读取 Wrangler 配置，正式构建仍需像下方命令一样显式传入 `SITE_URL`。

## 控制哪些修改触发构建

在 **Settings → Build → Build watch paths** 中将 Include paths 设置为：

```text
apps/website/*
package.json
pnpm-lock.yaml
pnpm-workspace.yaml
wrangler.jsonc
```

Cloudflare 的 `*` 会匹配嵌套目录。Exclude paths 留空。网站目前使用自身的静态品牌文件；以后若直接引用共享 package 或资源，应把对应路径加入监听范围。

生产自动部署跟随 `main`。开启 PR/分支 Preview deployments，可先预览页面再合并；预览不设置 `SITE_URL`，开发分支不会替换正式网站。PR 评论保持关闭，部署结果可在 Cloudflare 和 GitHub 检查中查看。

## 从本地重新部署

在仓库根目录执行：

```sh
SITE_URL=https://astral.oines.dev pnpm check:website
pnpm dlx wrangler@4.152.0 pages deploy apps/website/dist --project-name astral-website --branch main
```

首次使用先运行 `pnpm dlx wrangler@4.152.0 login`。如果本地 OAuth 回调等待超时，可以使用 `login --device`，在浏览器完成设备授权。CLI 直接发布适合手动恢复或重发已验证的构建；日常更新提交到 `main` 后由 Pages 自动构建。

## 路由与首次上线检查

- 首页 `/`，中文首页 `/zh-cn/`，文档位于 `/docs/` 与 `/zh-cn/docs/`。
- `public/_redirects` 提供文档入口和旧开发者栏目地址的 301 跳转。
- 构建产物包含 `404.html`，不要添加把全部未知路径重写到首页的 SPA 规则。
- Pagefind 的索引和脚本包含在 `dist/pagefind/`，不依赖外部搜索服务。

首次部署后检查首页、两种语言、搜索、一个旧地址的跳转和一个不存在的地址。检查正式页面 canonical、`robots.txt` 与 sitemap 使用真实域名；预览地址应保持禁止索引。

本地可运行 `pnpm check:website` 验证预览产物，或带上正式 `SITE_URL` 验证生产产物。部署后检查 Cloudflare 的实际部署状态和正式域名，不能仅凭构建通过判断上线成功。

## 官方参考

- [Monorepo 配置](https://developers.cloudflare.com/pages/configuration/monorepos/)
- [Wrangler 配置来源与生产、预览环境](https://developers.cloudflare.com/pages/functions/wrangler-configuration/)
- [构建工具版本与跳过默认安装](https://developers.cloudflare.com/pages/configuration/build-image/)
- [构建监听路径](https://developers.cloudflare.com/pages/configuration/build-watch-paths/)
- [静态重定向](https://developers.cloudflare.com/pages/configuration/redirects/)
