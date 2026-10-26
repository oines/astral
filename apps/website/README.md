# Astral 官网与用户文档

这是 Astral monorepo 中的静态网站应用，使用 Astro、Starlight 和 TypeScript。官网与英文、简体中文使用文档一起构建，发布到同一个 Cloudflare Pages 项目，正式地址为 [astral.oines.dev](https://astral.oines.dev/)。

## 开发与检查

在仓库根目录使用固定的 Node.js 26.7.0、pnpm 10.31.0：

```sh
pnpm install --frozen-lockfile
pnpm dev:website
```

开发地址为 `http://127.0.0.1:4321`。验证生产产物和搜索时：

```sh
pnpm check:website
pnpm preview:website
```

`check:website` 运行 Astro 类型检查、静态构建，以及本地链接、锚点、双语路由、Cloudflare 重定向和搜索产物检查。Pagefind 搜索索引只在正式构建时生成。网站构建不需要 Go、Docker 或 Electron。

## 内容归属

- `src/content/docs/docs/`：英文用户文档，路径 `/docs/…/`。
- `src/content/docs/zh-cn/docs/`：简体中文用户文档，路径 `/zh-cn/docs/…/`。
- `src/content/i18n/`：文档界面翻译。
- `src/components/Landing.astro`：首页内容。
- `WorkflowDemo.astro` 和 `GroupChatDemo.astro`：响应式 HTML 工作示例，不使用产品截图。
- `astro.config.mjs`：五组文档导航、语言和旧地址跳转。
- `public/_redirects`：Cloudflare Pages 的 HTTP 重定向。

当前有 23 篇文章的中英文版本。新增或移动页面时，同步更新导航、另一语言、站内链接和重定向。不要让旧地址无故失效。

这里是唯一公开使用文档。根目录 `docs/`、`SPEC.md`、`AGENTS.md` 是工程参考，不是另一份用户手册。涉及用户可见行为的产品改动，应在同一 PR 更新这里的步骤；核对当前 UI、命令和实现，不照抄旧规格，不追加“产品依据”页脚。

## 部署

Cloudflare Pages 的完整设置见 [CLOUDFLARE.md](CLOUDFLARE.md)。仓库根目录 `wrangler.jsonc` 指向 `apps/website/dist`，公共站点部署在域名根路径。

正式构建设置 `SITE_URL=https://astral.oines.dev`。本地与 PR 预览不填该值，禁用搜索引擎索引，也不虚构 sitemap 域名。预览构建可能提示缺少 `site` 而跳过 sitemap，这是预期行为。

`.github/workflows/website.yml` 独立检查网站；普通网站内容变更不会触发产品的 Go / Electron 构建。共享依赖或根配置变更仍会运行相应产品检查。

## 许可

采用仓库的 AGPL-3.0-only 许可。字体许可位于 `public/fonts/OFL.txt`；品牌资源沿用 Astral 原有 SVG。
