# 多平台 IM 验证记录

日期：2026-10-23，Asia/Singapore。对应 [平台接入指南](im-platforms.md)、[IM 契约](im.md)与 [UI UX 规范](ui-ux-spec.md)。本记录描述本轮实际执行的检查，不将协议夹具测试等同于真实外部账号验收。

## 验证环境与边界

浏览器使用独立 Astral 数据目录与本机模型、IM 协议服务；未修改已有用户部署或使用真实 QQ、NapCat、飞书凭据。界面截图为 548 × 756 窄屏，覆盖英文浅色、中文深色。桌面未签名 .app 已构建，但未启动它读取用户现有桌面数据。

微信另外通过真实腾讯 iLink 服务取得并显示了登录二维码，并完成取消展示。没有扫码授权或绑定真实微信账号；有效二维码不写入截图附件或 Git。扫码确认、验证码、区域跳转、凭据恢复与后续收发由协议夹具验证。

## 自动验证

| 检查 | 结果 |
| --- | --- |
| `go test -race -timeout=4m ./...` | 全部通过，包含 Telegram 回归与新增平台协议、权限、持续 Session 和回执测试 |
| `go test -race -tags=integration -count=1 -timeout=8m ./internal/platform` | 通过，使用本轮发行二进制验证安装、Server/Daemon 与 Computer Gateway |
| `pnpm typecheck` | Web、Desktop 均通过 |
| `pnpm lint` | 通过 |
| `pnpm test` | 6 个文件、38 个测试通过 |
| `pnpm build` | Web、许可证声明和 macOS ARM64 Server 构建通过 |
| `pnpm build:distribution` | macOS / Linux 的 ARM64、AMD64 四种发行二进制与校验和全部生成 |
| `pnpm --filter @astral/desktop build` | Electron 主进程、preload、Web 与本地未签名 .app 目录打包通过 |
| `git diff --check` | 通过 |

构建仍有原有前端 chunk 大小与静态/动态导入提示；不影响产物生成。最初许可证生成受沙箱内 pnpm 存储位置影响，已通过按锁文件恢复依赖并在正式存储环境构建解决，没有绕过许可证收集。

关键协议与服务测试覆盖：

- QQ 官方 Gateway identify、心跳、断线 resume、未提交批次重放；普通消息与 @ 消息分别处理；持久入站 `msg_id` 用于新建发送适配器的被动回复。
- NapCat HTTP 与正向 WebSocket 验证同一 QQ 身份；消息段保留纯文本，处理撤回、自发消息与临时群私聊；下载不读取 NapCat 主机任意路径。
- NapCat 全局消息 ID 在读取、删除与引用发送前核验当前平台聊天归属。通用执行超时保持 unknown，不自动重复执行。
- 飞书使用官方 SDK 的二进制长连接协议；事件归档 cursor 提交后才成功 ACK，未提交事件可以重放；群、私聊、引用回复、图片和文件均覆盖。
- 微信持久化 cursor、加密 context token、媒体密钥和下载能力；AES 媒体上传下载；扫码凭据只在 Server 保存；重连保留并行修改的账号名称、启停和访问范围。
- QQ、微信和飞书的服务级端到端测试经过 IMStore、真实 Core 与 Outbox。第二条消息继续原 Session，被动上下文不丢失，Web assistant 正文不外发，自身消息回显不会再次唤醒。
- 文件不支持附带文字的平台使用有序独立回执；发送意图重放不重复文件。所有平台操作受 `all | specified` 约束；查询、解析和跨聊天显式发送不创建 Session。

## 浏览器实际操作

| 操作 | 观察到的结果 |
| --- | --- |
| 设置 → Bot → 连接平台 | 展示 Telegram、QQ、微信、飞书；只有已连接的平台出现在 Bot 导航 |
| QQ 官方与 NapCat 各创建一个账号 | 同一个 QQ 平台保留账号列表，两种接入方式具有独立配置和身份 |
| QQ 官方连接 | App ID、App Secret、API 与 Token 地址验证成功，进入账号详情 |
| NapCat 连接 | HTTP 与正向 WebSocket 字段独立，实际握手成功后进入详情 |
| 飞书连接 | App ID / App Secret、Bot 身份与长连接发现验证成功，进入详情 |
| Telegram 回归 | Token、Endpoint、账号用户名、`/chat_id` 指引和 Topic ID 字段保留；Topic 22 可解析并绑定 |
| QQ 添加聊天 | 使用 `group:qa_group`，不出现 Telegram Topic 字段；绑定后出现 Astral 会话入口和回复方式 |
| 名称自动保存与连接草稿 | 填入错误连接地址时，独立改名仍成功，连接草稿没有被改名提交 |
| 连接失败和重试 | 原连接继续可用；输入保留，错误持续显示；修正地址后“重试”使用当前草稿 |
| 微信扫码入口 | 获取并显示真实腾讯二维码，显示等待状态，取消后返回未连接账号列表 |
| 中英文、浅色深色与窄屏 | 平台、账号、配置分组与操作可辨认，输入和按钮正常换行 |

另经代码审查修复：创建与扫码响应核对当前表单、Bot 和平台；KeepAlive 停用时暂停扫码轮询；刷新二维码废弃旧 UI 状态；验证码等待期间暂停自动提交，按钮和 Enter 均验证当前完整输入；聊天选择器按账号与弹窗打开轮次隔离迟到响应。这些保护没有改变服务端的聊天授权或 Session 语义。

## 截图

- [英文浅色平台选择](../artifacts/im-platforms/platform-picker-narrow.png)
- [英文浅色飞书账号](../artifacts/im-platforms/feishu-account-narrow.png)
- [Telegram Topic 22 与聊天权限](../artifacts/im-platforms/telegram-topic-narrow.png)
- [中文深色 QQ 多账号列表](../artifacts/im-platforms/qq-accounts-dark-zh.png)
- [中文深色 NapCat 账号](../artifacts/im-platforms/napcat-account-dark-zh.png)
- [中文深色平台选择](../artifacts/im-platforms/platform-picker-dark-zh.png)
- [中文深色微信授权入口，不含有效二维码](../artifacts/im-platforms/weixin-authorization-dark-zh.png)

## 仍需真实账号完成的现场验收

QQ 官方 Bot 的应用审核、群推送权限及发送额度；实际 NapCat 部署的 QQ 登录与远端文件配置；飞书应用发布和权限生效；微信手机扫码确认、账号实际可用范围与真实媒体消息，都取决于外部账号和平台状态。本轮没有这些账号凭据，也未宣称已验证它们。首次接入应按指南使用真实账号验证文本、回复、附件和权限收回后的行为。
