# IM 平台接入指南

本文说明当前实现的 Telegram Bot、QQ 官方 Bot、QQ NapCat、微信 ClawBot / iLink 和飞书 Bot 如何接入 Astral。消息存储、Session 绑定、持续上下文、Probe、显式发送和回执规则见 [IM 契约](im.md)；平台权限和服务端限制仍由各平台决定。

本轮实际测试、构建与浏览器证据见 [验证记录](im-platforms-qa.md)。

## 先连接账号，再选择聊天范围

在 **设置 → Bot** 中选择目标 Bot，通过 **连接平台** 添加账号。平台下面列出各自的账号；QQ 平台可以选择 **QQ 官方 Bot** 或 **QQ NapCat**。每个账号独立保存凭据、连接参数、访问范围和聊天。

新账号默认使用 `specified`（仅指定聊天），初始列表为空。连接成功后，将目标聊天加入列表并绑定到新 Session 或已有的未绑定 Session，Bot 才能通过该账号访问它。账号页面的已发现聊天和手动输入 Chat ID 都可用于添加；发现聊天本身不授权，也不创建 Session。

选择 `all`（账号可访问的所有聊天）后，Computer API 可以访问该账号实际可达的聊天。私聊的首条触发消息、群聊的首次 @ Bot 或回复 Bot 才会自动激活 Session；API 查询、解析聊天、平台操作和显式发送均不创建 Session。切回 `specified` 保留已有绑定和聊天设置；需要移除单个聊天时，先切回 `specified`。

已绑定聊天继续使用同一套 IMStore 和 Core 持续上下文。普通消息作为被动观察进入上下文；直接触发绕过 Probe，主动模式仍默认关闭。Bot 的普通助手文本留在 Web，只有 `send_message` 或显式 Computer HTTP API 调用向 IM 发送。新增平台没有增加其他 IM 专用模型工具，也没有改变 Core 执行、压缩或重试语义。

下面的 JSON 是账号的公开 `config`，不是完整 API 请求。App Secret、Bot Token、Access Token 和微信登录凭据均保存在服务器凭据存储中，不放入 `config`，也不返回给 Computer API。远程 HTTP API 地址使用 HTTPS，WebSocket 地址使用 WSS；明文 HTTP / WS 仅允许回环地址。容器中的 `127.0.0.1` 指向容器自身，地址必须从 **Astral 服务器**实际可达。

## Telegram Bot

1. 使用 Telegram 的 BotFather 创建 Bot，取得 Bot Token。
2. 在 Astral 中选择 Telegram，填写账号名称与 Bot Token；通常不需要填写服务器地址。
3. 确认该 Bot 没有正在使用的 webhook。Astral 使用 `getUpdates` 长轮询；验证发现活动 webhook 时会拒绝连接。
4. 将 Bot 加入群或频道，授予需要的权限。希望观察普通群消息时，在 BotFather 关闭隐私模式，或授予 Telegram 要求的群管理员权限。

```json
{
  "base_url": "https://api.telegram.org"
}
```

`base_url` 可省略。私聊、群、论坛 Topic 和频道沿用现有实现；Topic ID 与父 Chat ID 分开填写。发送 `/chat_id` 或 `/chat_id@bot_username` 可发现 Chat ID，论坛回复同时给出 Topic ID。该 Host 工具不调用模型、不创建 Session，也不将命令文本加入普通模型观察；详情见 [Telegram Chat ID 说明](im.md#find-a-telegram-chat-id)。

Telegram 编辑事件、引用回复、文本与文件收发继续可用。Bot API 不提供任意消息删除事件；通过 Astral 成功执行的显式删除会入档。[Telegram Bot API](https://core.telegram.org/bots/api) 是平台协议依据。

## QQ 官方 Bot

### 开放平台设置

1. 在 [QQ 开放平台](https://q.qq.com/) 创建并配置 Bot，取得 **App ID** 和 **App Secret**。
2. 按开放平台要求配置测试用户、测试群或发布范围，并开放需要的 C2C、群 @ 消息和发送能力。普通群消息是否推送取决于腾讯实际授予的能力，不能通过 Astral 的开关扩大。
3. 在 Astral 中选择 **QQ → QQ 官方 Bot**，填写 App ID 与 App Secret。默认 API 和 Token 地址通常不需要修改。
4. 在平台允许的私聊或群里给 Bot 发送消息。Astral 会从网关事件发现聊天；也可以在群里发送 `@Bot /chat_id`，或在私聊发送 `/chat_id`。

```json
{
  "app_id": "123456789",
  "base_url": "https://api.sgroup.qq.com",
  "token_url": "https://bots.qq.com/app/getAppAccessToken"
}
```

`app_id` 必填，另外两个字段可以省略。App Secret 单独填写。Astral 用 App Secret 获取 Access Token，验证 `/users/@me` 返回的真实 Bot 身份，再发现平台 WebSocket 网关；不需要在 Astral 配置公开回调服务器。

### Chat ID 和实际限制

QQ 官方 Bot 的聊天标识是 `private:<user_openid>` 或 `group:<group_openid>`，**不是 QQ 号或群号**。这些 openid 属于当前应用，不应跨应用复用。该适配器只接入 C2C 和群聊，不接入 QQ 频道或 Telegram 式 Topic，也不枚举所有联系人和群。

Astral 接收平台实际推送的 C2C、群 @ 和获准的普通群消息。仅有群 @ 权限时，Bot 看不到完整群上下文，持续上下文和 Probe 也只能使用收到的消息。

发送支持文本和平台允许的媒体/文件。未显式指定 `reply_to` 时，会从该聊天最近的已归档入站消息取得 `msg_id` 作为被动回复上下文。回复窗口、次数、主动发送额度、群普通文件类型和语音编码要求仍由 QQ 决定；本文不承诺固定窗口或所有群都可发送任意文件。超时或服务器失败可能留下 `unknown` 回执，需要查看实际聊天后再决定是否重发。

本实现单次上传上限为 **20 MiB**，使用 Base64 上传，没有实现大文件分块；下载上限为 **50 MiB**。QQ 官方 Bot 当前没有额外 Computer 平台操作目录项，文本/文件继续走已有显式发送接口。

## QQ NapCat

### NapCat 服务配置

先按 [NapCat 官方指南](https://napneko.github.io/)安装并登录目标 QQ 账号，然后在其 OneBot 11 网络配置中：

1. 启用 **HTTP 服务端**，记录 Astral 可访问的 HTTP 地址。
2. 启用 **正向 WebSocket 服务端**，选择能上报事件的端点。不要使用只处理 API 请求的 `/api` 端点，也不使用反向 WebSocket。
3. 两个服务使用同一 Access Token，消息上报格式设为数组，即 `messagePostFormat: "array"`。
4. 需要通过远程 NapCat 读取其本地文件时，启用 `enableLocalFile2Url`，使 `get_file` 能返回 Base64 内容。Astral 不会把 NapCat 主机上的路径当成服务器本地路径读取。

在 Astral 中选择 **QQ → QQ NapCat**，填写两个地址及对应 Access Token：

```json
{
  "base_url": "http://127.0.0.1:3000",
  "websocket_url": "ws://127.0.0.1:3001"
}
```

两个字段均必填；端口只是示例，应以 NapCat 实际配置为准。连接远程主机时应使用实际 HTTPS / WSS 入口，例如 `https://napcat.example.com` 和 `wss://napcat.example.com`。Astral 会同时验证 HTTP 登录状态和 WebSocket 首个事件的账号身份；两端必须连接同一个 QQ 账号。

### 聊天、文件和断线行为

NapCat 使用 `private:<QQ号>` 或 `group:<群号>`。这是普通 QQ 数字标识，和 QQ 官方 Bot 的 openid 不同。支持私聊、群聊、@ Bot、引用、文本/文件收发，以及 NapCat 上报的撤回事件；不支持 Topic 或消息编辑事件。

正向 WebSocket **不提供断线期间的历史重放**。Astral 保留尚未完成 IMStore 提交的当前批次，但这不能补回 NapCat 在断线期间没有交付的消息。需要补查已知消息或管理群时，使用下方明确列出的 Computer 操作。

上传上限为 **20 MiB**，下载上限为 **50 MiB**，均为 Astral 当前边界。附件优先从有效 URL 获取，再尝试 `get_file` 返回的 Base64；只返回 NapCat 主机本地路径时会明确失败。NapCat 的通用异常 `retcode=200` / `1200` 可能包括已提交 QQ 后才超时的情况，不能一概当作“没有发送”，Astral 会保留不确定结果。

## 微信 Bot：腾讯官方 ClawBot / iLink

### 扫码连接

微信使用 [Tencent/openclaw-weixin](https://github.com/Tencent/openclaw-weixin) 公布的 iLink 客户端协议，不需要在 Astral 服务器安装 OpenClaw。当前接入范围是协议支持的个人聊天，实际账号是否获准使用由腾讯服务决定。

1. 在 Astral 中选择 **微信 → ClawBot / iLink**，填写账号名称并获取登录二维码。
2. 使用微信扫描，在手机中确认授权；如果出现验证码要求，输入 **微信中显示的数字**并继续。
3. 等待授权完成。二维码过期、验证被限制或平台要求重新绑定时，获取新二维码后重试。
4. 授权后让目标用户先给 Bot 发一条私聊消息。服务器收到该用户的聊天上下文后，才支持解析聊天、回复和请求输入状态；在 `specified` 模式下仍需将聊天加入授权列表。

二维码内容由前端在本地编码显示；凭据由 Owner API 留在服务器，浏览器只接收二维码内容、授权状态和账号视图。需要重新授权时，在现有微信账号中选择 **重新扫码授权**。iLink `-14` 表示登录会话过期，应重新扫码；更换成另一个 Bot 身份需要创建新账号。

微信公开 `config` 由授权流程生成；如下只是结构示例，不是手工添加凭据的流程：

```json
{
  "base_url": "https://ilinkai.weixin.qq.com"
}
```

成功授权可能返回其他受信任的 `*.weixin.qq.com` API 主机，Astral 绑定到该授权返回的地址。适配器内部支持 `cdn_base_url`，默认为 `https://novac2c.cdn.weixin.qq.com/c2c`；正常 UI 不需要编辑它。登录凭据包含 `bot_token`、`bot_id`、扫码用户标识和 API 地址，整体留在凭据存储中，不能用可编辑的 `config` 自报账号身份。

服务器通过 HTTPS 长轮询接收消息，并使用协议要求的加密 CDN 流程传输媒体。每个用户的 `context_token` 和附件密钥进入已有的加密凭据存储，在提交轮询 cursor 前持久化，不进入模型可见的观察或公开配置。

### 实际支持范围

支持私聊文本、图片、文件、视频，以及平台提供的语音附件和转写文本；原始语音按实际编码保留，不自动转码。消息类型中出现 `group_id` 不代表本适配器支持群，当前会忽略群消息。没有联系人枚举、群聊、Topic、向外发送引用、消息编辑或已读回执。

入站引用附带的信息会保留，但不能据此承诺向外发送引用。发送依赖目标用户最近提供的聊天上下文；手工编造一个用户标识不会使其可达。文字和文件说明分别排成独立消息，每次外部发送拥有自己的持久化回执；传输上限为 **50 MiB**，这是 Astral 的内存与传输边界，不是腾讯服务的文件额度声明。

iLink 成功响应可以没有服务端消息 ID。此时 Astral 保存真实发送请求的 `client_id`，以 `client:` 前缀区分，不能把它当成入站消息的服务端引用 ID。HTTP 成功但缺少明确接受状态会标为 `unknown`，不会虚构送达确认。

微信额外提供 `set_typing` Computer 操作。它先为当前授权私聊取得 typing ticket，再请求开启或取消输入状态；响应只表示请求完成，不保证对方已经看见输入指示。

## 飞书 Bot

### 飞书开发者设置

使用开启 Bot 能力的**企业自建应用**。群自定义 webhook Bot 只能推送通知，不满足接收消息的要求；应用需要在租户内启用、发布，并授予实际用户可见范围。平台背景见 [飞书 Bot 概述](https://open.feishu.cn/document/client-docs/bot-v3/bot-overview)。

1. 创建应用、开启 Bot 能力，取得 **App ID** 与 **App Secret**。
2. 事件订阅选择**使用长连接接收事件**，订阅 `im.message.receive_v1`。Astral 主动建立官方 SDK WebSocket，不需要公开 webhook。
3. 按需要开通以下应用权限，并发布权限变更：

| 目的 | 权限 |
| --- | --- |
| 接收用户发给 Bot 的私聊 | `im:message.p2p_msg:readonly` |
| 接收群内 @ Bot 消息 | `im:message.group_at_msg:readonly` |
| 观察普通群消息 | `im:message.group_msg`，以平台审批结果为准 |
| 以 Bot 身份发送消息 | `im:message:send_as_bot` |
| 上传和读取消息资源 | `im:resource` |
| 读取聊天资料/显示群名 | `im:chat:readonly` |

回复对象核对、远端历史、反应和删除等操作还需要飞书对应 API 要求的消息权限。应根据实际需要选择平台可授予的只读或读写范围；连接成功不代表所有操作都已授权。

4. 在 Astral 中选择飞书，填写 App ID 与 App Secret。
5. 将应用 Bot 加入目标群，或从应用可用范围内的用户发起私聊。群内可发送 `@Bot /chat_id` 获取聊天标识；只有 @ 消息权限时，这仍然可用。

```json
{
  "app_id": "cli_example",
  "base_url": "https://open.feishu.cn"
}
```

`app_id` 必填，`base_url` 可省略。App Secret 单独填写。保存时验证实际 Bot 的 `open_id`、激活状态和长连接端点发现；运行时的事件订阅或权限错误会显示在账号状态中。

### 聊天、引用和文件

私聊和群聊都使用 `oc_…` 形式的 `chat_id`。本实现将飞书话题/线程消息归入父聊天，不能创建独立线程授权；`get_message` 返回的原始消息保留 `thread_id` 等飞书字段。不要把线程 ID 填入 Telegram 的 Topic 字段。

支持文本、常见富文本入站内容、图片/文件/音视频附件下载，以及文本和文件发送。@ Bot、私聊和对已知 Bot 消息的回复沿用直接触发；引用发送会先核对消息实际属于选定聊天。群名在首次读取时查询并缓存；没有资料权限时显示聊天 ID，不阻塞消息入档。

飞书文件消息没有 Telegram 式 caption。Astral 将文字与文件排成独立发送与独立回执。上传使用文件消息类型，不冒充语音或视频的专用发送能力；非空文件上限为 **30 MB（30,000,000 字节）**。下载也限制为 30 MB。文本按每片最多 16,000 个 Unicode 字符拆分，为飞书文本请求体的 150 KB 上限保留两层 JSON 转义空间；这不是平台原生字符上限。[发送消息](https://open.feishu.cn/document/server-docs/im-v1/message/create)和[上传文件](https://open.feishu.cn/document/server-docs/im-v1/file/create)说明平台原生约束。

不接收消息编辑/删除事件，不提供已读或最终送达事件。显式成功删除会由 Astral 入档。官方 SDK 的事件回调在 IMStore 和 cursor 完成提交后才返回成功 ACK；持久化失败时保留事件重投，不在进入内存队列时提前确认。

## 能力差异速查

下表描述 Astral 当前实现，不代表平台所有 API。容量均是本实现接受的上限，平台仍可能按账号权限或文件类型进一步限制；MiB 为 1,048,576 字节，MB 为 1,000,000 字节。

| 适配器 | 聊天范围 | 连接/接收 | 向外引用 | 编辑/撤回事件 | 上传 / 下载 |
| --- | --- | --- | --- | --- | --- |
| `telegram.bot` | 私聊、群、论坛 Topic、频道 | HTTPS 长轮询；需无活动 webhook | 支持 | 编辑；无通用删除推送 | 50 MiB / 20 MiB |
| `qq.bot` | C2C、获准的群 | HTTPS + 平台发现的 WSS 网关 | 使用平台 `msg_id` 回复上下文 | 不接入 | 20 MiB / 50 MiB |
| `qq.napcat` | 私聊、群 | HTTP API + 事件正向 WS | 支持 | 撤回；无编辑 | 20 MiB / 50 MiB |
| `weixin.ilink` | iLink 支持的个人聊天 | HTTPS 长轮询 + 加密 CDN | 不支持 | 不接入 | 50 MiB / 50 MiB |
| `feishu.bot` | 私聊、父群聊 | HTTPS + 官方 SDK 长连接 | 支持，核对实际聊天归属 | 不接入 | 30 MB / 30 MB |

这些适配器均不提供任意聊天枚举，也不把 API 接受当作对方已读或最终送达。平台没有推送、账号无权读取、服务断线期间没有重放的消息，不会凭空成为持续上下文。

## Computer HTTP API：先读目录，再调用明确操作

从当前环境快照读取 `local_api.base_url`，再追加路径；它由当前电脑的本地监听器提供 Bot、Session 和来源电脑身份。不要把这些路径与服务器 Owner API 的 `/api/v1` 路径混用。

| 路径 | 用途 |
| --- | --- |
| `GET /v1/accounts` | 当前 Bot 的账号及访问范围 |
| `GET /v1/accounts/{id}/operations` | 该账号实际支持的操作、允许参数和协议文档 |
| `POST /v1/accounts/{id}/operations/{operation}` | 对明确选定聊天调用目录中的操作 |
| `POST /v1/accounts/{id}/conversations/resolve` | 用 `{external_id,topic_id?}` 解析已授权聊天，不创建 Session |
| `POST /v1/im/messages`、`POST /v1/im/files` | 通过持久化 Outbox 显式发送，要求稳定的 `Idempotency-Key` |
| `GET /v1/im/receipts/{id}` | 查询发送状态和平台接受凭据 |

操作请求示例：

```http
POST /v1/accounts/connection_feishu/operations/add_reaction
Content-Type: application/json

{
  "conversation_id": "conversation_authorized",
  "parameters": {
    "message_id": "om_observed_message",
    "emoji_type": "OK"
  }
}
```

`conversation_id` 是 Astral 的会话记录 ID；不是 `oc_…`、QQ 号或 openid。账号和聊天必须属于当前 Bot，且仍在该账号 `all | specified` 范围内。飞书和 NapCat 的消息级操作还要求消息已归档在选定聊天，并核对平台当前返回的聊天归属；只知道全局消息 ID 不构成授权。这两个适配器的 `message_id` 要求使用字符串，避免大整数经过 JSON 数值时丢失精度；Telegram 参数继续遵循既有方法契约。

目录是明确允许列表，没有任意 URL、任意方法名或路径代理。`chat_id`、`receive_id`、`group_id`、凭据等替代寻址参数不能绕过选定聊天；未列出的参数会失败。非 Telegram 操作不接受 `source_conversation_id` 或文件上传。成功的显式删除会追加 `message.deleted` 事实。管理操作没有自动重试；结果不确定时先查看实际状态，正常文本和文件发送使用有持久化回执的发送端点。

### 各平台操作目录

**QQ 官方 Bot** 当前为空。发送文本、媒体和文件使用既有 `send_message` 或 `/v1/im/messages`、`/v1/im/files`。

**NapCat** 只开放以下 6 项，不开放 OneBot 任意 action：

| 操作 | `parameters` | 限制 |
| --- | --- | --- |
| `get_chat` | `{}` | 选定私聊或群 |
| `get_message` | `message_id` | 需本地归档与平台聊天归属证明 |
| `delete_message` | `message_id` | 同上；成功后入档 |
| `get_members` | `{}` | 仅群聊 |
| `get_member` | `user_id` | 仅群聊；QQ 号建议用字符串 |
| `set_group_ban` | `user_id`、可选 `duration` | 仅群聊；秒数 0–2,592,000，0 解除禁言，省略为 1,800；需要平台群管理权限 |

NapCat 对自己发出的私聊消息可能不返回 `target_id`；此时仅在该用户最近 100 条平台历史能证明消息归属时允许消息操作。证明失败会拒绝，不靠猜测选择聊天。

**微信 iLink** 只开放 `set_typing`，参数必须且只能是 `{"active":true}` 或 `{"active":false}`。要求已授权私聊已有入站上下文；typing ticket 和 context token 不出现在返回值中。

**飞书** 开放以下 7 项，仍受应用实际权限限制：

| 操作 | `parameters` |
| --- | --- |
| `get_chat` | `{}` |
| `get_message`、`delete_message` | `message_id` |
| `list_messages` | 可选 `page_size`、`page_token`、`start_time`、`end_time`、`sort_type` |
| `list_reactions` | `message_id`，可选 `page_size`、`page_token` |
| `add_reaction` | `message_id`、`emoji_type` |
| `delete_reaction` | `message_id`、`reaction_id` |

飞书 `page_size` 为 1–50；时间边界使用 Unix 秒数字字符串；`sort_type` 为 `ByCreateTimeAsc` 或 `ByCreateTimeDesc`。`list_messages` 是对选定聊天的远端查询，不自动将返回历史导入 IMStore 或激活 Session；消息级操作仍需相应的已归档身份。`get_message` 保留平台原始消息对象，不把线程和反应等信息压成 Telegram 数据格式。

**Telegram** 保留现有目录和路径，同时可从通用目录访问：

- `GET /v1/accounts/{id}/telegram/operations`
- `POST /v1/accounts/{id}/telegram/{operation}`

其 copy/forward 来源、嵌套聊天引用、Topic、文件和命令 scope 规则继续遵守 [现有 Telegram 操作契约](im.md#named-telegram-operations)。新增通用入口没有扩大原有授权范围。

## 常见接入问题

| 现象 | 核对方式 |
| --- | --- |
| 账号已连上，但添加后 Bot 没有反应 | 确认聊天仍在账号访问范围内，已绑定正确 Session，Bot 已获准使用模型及电脑；接收权限与聊天授权是不同边界 |
| QQ 群只收到 @ 消息 | 核对开放平台实际群消息权限；Astral 无法补出平台没有推送的普通消息 |
| QQ 已收到消息但发送被拒绝 | 查看实际回执，核对平台回复窗口、额度、媒体类型和发布范围；让用户再次发消息可提供新的入站上下文，但不保证平台额度恢复 |
| NapCat HTTP 正常，连接验证失败 | 检查事件正向 WS 端点、相同账号和 Token、数组上报格式；不要使用 `/api` |
| NapCat 图片或文件下载失败 | 检查 URL 是否可达；远程主机本地文件应启用 `enableLocalFile2Url`，并核对 50 MiB 上限 |
| 微信授权失效或返回 `-14` | 在现有账号中重新扫码；二维码过期或验证码被限制时先获取新二维码 |
| 微信用户无法解析、回复或显示输入状态 | 先让该用户向 Bot 发私聊，再确认聊天已授权；不能手工制造 `context_token` |
| 飞书 App Secret 验证通过但没有消息 | 检查 Bot 是否激活、事件模式是否为长连接、是否订阅 `im.message.receive_v1`、权限变更是否已发布、Bot 是否在群内 |
| 飞书回复/反应/删除被拒绝 | 核对相应消息权限、本地归档和选定聊天归属；不能用别的聊天中取得的 `message_id` |
| 回执显示 `unknown` | 先查看实际聊天。它表示服务器无法确认外部结果，不表示消息必然没发出；不要通过自动重试制造重复发送 |

## 验证范围与可审计来源

本轮接入通过本地 HTTP / WebSocket 协议夹具和真实 Astral Service / IMStore 路径验证，包括文本与文件收发、凭据脱敏、重连/重投、飞书持久化后 ACK、微信扫码状态和媒体加解密、消息/账号权限隔离、API 不创建 Session、显式删除入档，以及已有 Telegram 回归。夹具不依赖真实外部账号。

**尚未使用真实 QQ、NapCat 登录账号、微信或飞书租户完成平台现场验收。** 上述测试不证明实际账号已获准接入、平台配额足够、所有媒体编码可发送，也不替代部署后的真实收发检查。接入后应先在测试私聊/群中确认事件到达、正确绑定、显式发送及平台实际展示；未知发送结果先查聊天，不自动重发。

协议审计基线记录如下。固定提交用于复核实现依据，后续上游变化应重新核对，不将当前 `main` 当作已验证的发行版：

| 来源 | 本轮核对版本 |
| --- | --- |
| [腾讯 qqbot-nodejs](https://github.com/tencent-connect/qqbot-nodejs/tree/429a0f1b798f3b0fcef3679e207dbc0469a1df2d) | `429a0f1b798f3b0fcef3679e207dbc0469a1df2d` |
| [腾讯 openclaw-qqbot](https://github.com/tencent-connect/openclaw-qqbot/tree/89988812442583bf47c34b89f9abf5fd4b85df0b) | `89988812442583bf47c34b89f9abf5fd4b85df0b` |
| [NapCatQQ](https://github.com/NapNeko/NapCatQQ/tree/3420f0b76d7dc0ac92cde1663f21ea27f61e13bb) | `3420f0b76d7dc0ac92cde1663f21ea27f61e13bb`；源码审计，不宣称对应某个已实测发行版本 |
| [腾讯 iLink 协议参考](https://github.com/Tencent/openclaw-weixin/blob/874637a0dbc24050f72d1385add4fc6170a2d726/docs/protocol.md) | `874637a0dbc24050f72d1385add4fc6170a2d726`，客户端版本 `2.4.10`；客户端类型不等于完整服务端契约 |
| [飞书官方 Go SDK](https://github.com/larksuite/oapi-sdk-go/tree/v3.12.2) | `github.com/larksuite/oapi-sdk-go/v3 v3.12.2`，固定于 `go.mod` / `go.sum` |

运行 `go test -race ./internal/channels ./internal/gateway ./internal/httpapi` 可复跑相关后端回归；完整构建和项目检查见 [README](../README.md#development-and-verification)。依赖许可和发布物 notice 说明见 [第三方致谢](../THIRD_PARTY.md)。
