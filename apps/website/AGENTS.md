# Astral website

This is the public Astral website in the Astral monorepo. It owns the user documentation in both English and Simplified Chinese. Root `docs/`, `SPEC.md`, and `AGENTS.md` are engineering references, not a second public manual.

## Current user decisions — 2026-10-26

- Rebuild the previous site completely using Astro, Starlight and TypeScript. The prior React prototype, one-screen layout, illustration requirement and Sites Worker adapter are superseded.
- Build the product landing page and documentation together, with English and Simplified Chinese content and shared navigation.
- Keep the style restrained and close to Astral: warm off-white, graphite text, subtle inset surfaces, fine boundaries, generous but purposeful spacing. Use the original Astral logo and wordmark. Avoid decorative gradients, heavy shadows, nested cards and gratuitous motion.
- Lead with “One agent. All your computers.” / “一个 Agent，在你的所有电脑上工作。” Conversation continuity is context for working across computers, not a stand-alone differentiator.
- The landing page must show one persistent Session spanning multiple Computers: one task message, a continuous transcript, and a computer label on each execution. Do not split the transcript into computer tabs or replace earlier records. Command details may expand in place.
- Keep supporting section headings lighter and smaller than the hero. No decorative section numbers, repeated kickers, terminal-style path lists or redundant slogan endings.
- After cross-computer work, introduce IM and group chats with “把 Astral 带进你的聊天。” and “在 Telegram、QQ、微信或飞书里交代任务，让它使用你的电脑完成任务，再把结果发回聊天。” Show a responsive HTML group conversation with multiple participants and a returned file. Each chat has its own Session; Telegram, QQ and Feishu support groups, while Weixin currently supports private chats only. Proactive group participation is optional and off by default.
- The landing page must not use screenshots. Build the product/workflow demonstration in responsive HTML/CSS with working controls. Label illustrative workflow content; do not present examples as live activity or verified execution.
- Public documentation must reflect actual product behavior. Use Bot and 电脑 for product UI names. Models and computers require initial configuration; IM is optional. Computer access is granted per Bot. Directory changes and computer changes keep a session, but do not move files or existing processes.
- The source repository and first public installers are available. “Get Astral” must link only to assets verified on an actually published official `oines/astral` GitHub Release and describe that release’s real platform availability, signing and notarization status. Drafts and CI candidates do not establish public download availability. Keep source-build guidance accurate; never invent assets, release status, testimonials or metrics.
- The production domain is `https://astral.oines.dev`. The user authorized deployment to their Cloudflare Pages account, merging this work into `main`, and enabling automatic deployment on 2026-10-26.

## Development

- Run `pnpm check`, `pnpm build`, and `pnpm check:site` after meaningful changes. Preview the production build for search validation; Pagefind is generated at build time.
- Use the in-app browser to check the home page and docs, both locales, desktop and mobile, search, theme, language switching and navigation. Start and open the preview yourself.
- Preserve clear Chinese communication about decisions, limitations and results. Treat feedback on product direction as discussion unless it clearly requests implementation.
- Verify user instructions against current product implementation and UI. Engineering contracts help clarify intent but do not prove that a feature is shipped. Write complete task-oriented instructions here; do not append product-source footers or send users to internal specifications to complete a setup. Update both languages in the same change.
- Cloudflare Pages serves the static output. See `CLOUDFLARE.md`; the root `wrangler.jsonc` points to this app. Keep website checks independent from Go and Electron builds.
- Public behavior changes must update the corresponding website documentation in the same PR.
- `SITE_URL` optionally sets the production origin for canonical URLs, sitemap and robots. Without it, previews do not invent a production domain or publish a sitemap.
