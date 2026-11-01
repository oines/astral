---
title: "连接 QQ"
description: "用普通 QQ 号或 QQ 官方 Bot 接入 Astral，在私聊和群聊里交代任务。"
---

先按你准备使用的账号选择接入方式：

- **想用一个普通 QQ 号与好友、群成员聊天**：使用下面的 [QQ NapCat](#qq-napcat)。NapCat 负责登录这个 QQ 号，Astral 负责处理消息和任务。
- **已经有 QQ 开放平台创建的机器人**：前往 [QQ 官方 Bot](#qq-官方-bot)，使用它的 App ID 和 App Secret 连接。

两种方式都需要 Astral Bot 已经能使用[模型和电脑](/zh-cn/docs/start/first-steps/)。第一次先接通一个自己能测试的聊天；公开群请使用专用 Bot 和[独立执行环境](/zh-cn/docs/start/safe-use/)。

## QQ NapCat

这条流程适用于普通 QQ 号，不需要 QQ 开放平台凭据。准备一个交给 Astral 使用的 QQ 账号，以及**另一个发测试消息的 QQ 账号**。

下面以 Astral 和 NapCat 直接运行在同一台电脑为例。使用 Docker、远程服务器或不同电脑时，先按 [NapCat 网络连接](/zh-cn/docs/chat/napcat-network/)准备地址，再回来继续。

### 1. 打开 NapCat，登录目标 QQ 号

先检查电脑上是否已经有 NapCat 在运行。已有实例就打开它的管理页面继续使用，避免同一账号重复登录或占用同一端口。只有尚未安装时，才按 [NapCat 官方安装指南](https://napneko.github.io/guide/boot/Shell)选择系统对应的安装方式。

NapCat 启动日志会给出 **WebUI 地址和登录 Token**，常见地址是 `http://127.0.0.1:6099/webui/`，实际端口以日志为准。在这个管理页面里登录准备交给 Astral 的 QQ 号；需要扫码时，用手机 QQ 扫码确认。按页面提示修改 WebUI 密码，确认账号在线，并保持 NapCat 运行。

**WebUI 是 NapCat 的管理页面。它的地址和登录 Token 不填入 Astral。** 接下来需要在里面启用供 Astral 连接的服务。

### 2. 启用两个独立服务

在 NapCat WebUI 的 **网络配置** 中，需要同时有：

- **HTTP 服务端**：让 Astral 调用接口，例如发送消息。
- **WebSocket 服务端（正向 WebSocket）**：把收到的 QQ 消息和通知交给 Astral。

已有对应服务时可以复用，缺少哪一项再新建哪一项。两者必须属于刚才登录的同一个 QQ 号。不要选 HTTP 客户端或反向 WebSocket 客户端。

如果是在同一电脑直接运行两个程序，可以使用下面的设置。已有端口可继续使用，不必改成示例值。

| NapCat 配置项 | HTTP 服务端 | WebSocket 服务端 |
| --- | --- | --- |
| 名称 | `astral-http` | `astral-ws` |
| Host（监听地址） | `127.0.0.1` | `127.0.0.1` |
| Port | `3000` | `3001` |
| 消息格式（`messagePostFormat`） | `Array` / `array` | `Array` / `array` |
| Token | 设置一个 Access Token | 复制左边的同一个 Token |
| 启用 / 保存后启用 | 开启 | 开启 |

NapCat 可能为新服务分别生成不同 Token。可以保留 HTTP 服务端的值，把它复制到 WebSocket 服务端，稍后再复制到 Astral。修改已有服务的 Token 时，其他正在使用它的程序也需要同步更新。

两个名称不能重复，保存后确认两项都已启用。WebSocket 心跳间隔保留默认值即可，例如 `30000` 毫秒；“上报自身消息”可以关闭，其他选项保留默认。HTTP 表单里的“启用 Websocket”不能替代上面的独立 WebSocket 服务端。外部配置字段可参阅 [NapCat WebUI 指南](https://napneko.github.io/config/basic)。

### 3. 允许读取附件

在 NapCat 的 **OneBot 配置**中开启 **启用本地文件到 URL**（`enableLocalFile2Url`），保存配置。不同版本的显示名称可能略有差异，以这个配置键为准。

这个选项让 NapCat 能把本地附件的内容交给 Astral。只有一个 NapCat 电脑上的磁盘路径，Astral 无法取得文件。某些图片本身有可访问的 URL，即使未开此项也能显示，所以后面还需要用一个小文件验证。

### 4. 把 NapCat 连接到 Astral

在 Astral 的 **设置 → Bot → 目标 Bot → 连接平台 → QQ** 中选择 **QQ NapCat**。将刚才配置的两个服务地址和共同 Token 填入连接配置：

| Astral 字段 | 同机示例 | 从哪里取得 |
| --- | --- | --- |
| NapCat HTTP 地址 | `http://127.0.0.1:3000` | HTTP 服务端的 Host 和 Port；不要加 `/get_login_info` 等接口路径 |
| NapCat 正向 WebSocket 地址 | `ws://127.0.0.1:3001` | WebSocket 服务端的 Host 和 Port；使用事件入口，不要加 `/api` |
| Access Token | 两个服务共同的 Token | 从 NapCat 的服务配置复制，只填值，不加 `Bearer ` |

连接时，Astral 会核对 QQ 在线状态，以及两个服务是否指向同一账号。以后修改地址或 Token，使用该区域的 **保存连接** 重新核对。无需设置 Astral 回调地址，CORS 保留默认即可。

这里的 `127.0.0.1` 指**运行 Astral 的电脑**。通过浏览器访问远程 Astral 时，它不是浏览器所在电脑；其他部署方式请使用[对应网络配置](/zh-cn/docs/chat/napcat-network/)。

### 5. 添加一个聊天，用另一个 QQ 号验证

先在 QQ 中让这个账号加入目标群，或与测试账号建立能正常收发消息的联系，例如互加好友。Astral 的“添加聊天”不会代替 QQ 加好友或入群。

新账号默认 **仅指定的聊天**，列表为空。在 Astral 账号的 **已接入聊天** 中添加测试聊天并选择会话。可选择已发现的聊天，找不到时通过 Chat ID 查找：

| 你要接入的聊天 | Chat ID 写法 |
| --- | --- |
| 与 QQ 号 `123456789` 的私聊 | `private:123456789`，数字是**对方的 QQ 号** |
| 群号为 `987654321` 的群聊 | `group:987654321`，数字是**群号** |

把数字替换成真实值，核对查找到的名称，再完成添加。第一次可选择新会话；选择已有会话会沿用那段工作上下文。

完成后，使用**另一个 QQ 账号**重新发送：

> 请在这个 QQ 聊天中回复“连接成功”，并告诉我你当前使用哪台电脑。

群里先用 QQ 的提及选择器真正 **@ 登录在 NapCat 的账号**，再发送任务。只输入昵称不算提及。登录在 NapCat 的账号自己发出的消息不会唤醒 Bot。

在 Astral 对应会话检查新任务，再到 QQ 确认实际回复。随后发一张图片或一个小文件，请 Bot 读取并发回来，确认附件也可用。群里默认被提及或被回复时才开始处理；需要时再开启[主动参与讨论](/zh-cn/docs/chat/groups/)。

## QQ 官方 Bot

这条流程使用 QQ 开放平台创建的机器人，不需要 NapCat。账号能用于哪些用户、群聊和消息类型，以 QQ 平台给该应用的实际权限为准。

1. 在 [QQ 开放平台](https://q.qq.com/)打开或创建机器人，在它的应用信息中取得 **App ID** 和 **App Secret**。
2. 在平台配置测试用户、测试群或正式发布范围，并申请所需的私聊、群消息及发送权限。先确认自己的测试账号或群在允许范围内。
3. 在 Astral 的 **设置 → Bot → 目标 Bot → 连接平台 → QQ** 中选择 **QQ 官方 Bot**，使用这两个值连接。默认 API 和 Token 地址通常不需更改，Astral 主动连接平台网关，无需公开 webhook。
4. 从获准的 QQ 用户或群给机器人发一条消息，让 Astral 发现聊天。到该账号的已接入聊天中添加它，并选择 Astral 会话。
5. 添加后重新发送“请在这个 QQ 聊天中回复：连接成功”；群里真正 @ 机器人。检查 Astral 的处理记录和原 QQ 聊天的回复。

优先选择发现的聊天，不需要先去找 openid。必须手工填写时，使用该应用实际收到的 `private:<user_openid>` 或 `group:<group_openid>`。**这里的 openid 不是 QQ 号或群号**，也不能从另一个应用复制过来。

当前接入私聊和获准群聊，不接入 QQ 频道。普通群消息、回复时机、Markdown、模板和文件发送均受平台权限与规则限制；官方 Bot 的这条群聊/私聊接入不支持消息表情回应。

## 图片、文件和其他 QQ 内容

接通后，可以让 Bot 阅读图片、文件、QQ 卡片和合并转发，再把结论或文件发回当前聊天。NapCat 发送普通文本，Markdown 标记会原样显示；Astral 中的工作过程文字不会逐条转发。

群管理、公告、群文件、精华消息，以及表情和戳一戳等进阶操作，见[平台 API](/zh-cn/docs/chat/platform-api/)。这些操作使用所连接账号的权限。

## 连接或回复失败时

| 现象 | 下一步 |
| --- | --- |
| NapCat WebUI 能打开，Astral 连接失败 | 检查两个 OneBot 服务是否都启用；WebUI 的端口不是它们的端口 |
| HTTP 超时或拒绝连接 | 核对监听地址、端口、Docker 映射或 SSH 转发，见[网络连接参考](/zh-cn/docs/chat/napcat-network/) |
| 返回 `401` / `403` 或 Token 错误 | HTTP、WebSocket 和 Astral 使用同一 OneBot Token，不是 WebUI Token；代理须保留 Authorization 头 |
| WebSocket 无法连接或没有报告事件账号 | 使用正向 WebSocket **服务端**及 `ws://` / `wss://` 地址，不用 `/api`；确认 `array` 格式和 QQ 在线 |
| 提示两个服务对应不同 QQ 账号 | 检查端口或代理路由，确保两条连接到同一 NapCat 实例和 QQ 号 |
| 已连接但不处理消息 | 核对账号启用、聊天已添加；NapCat 测试要用另一个 QQ 号，群里真正 @ Bot |
| Astral 已处理但 QQ 没有回复 | 明确要求发回 QQ，检查发送错误、QQ 禁言状态；官方 Bot 还要检查平台发送权限 |
| 文字正常，附件读取失败 | 启用 `enableLocalFile2Url`，检查文件 URL 从运行 Astral 的机器是否可达；语音还可能依赖 NapCat 转码支持 |

NapCat 不会补回断线期间的消息，恢复连接后请重新发送需要处理的内容。

发送超时或结果不确定时，先去 QQ 检查实际消息，再决定是否重发，避免重复发送。
