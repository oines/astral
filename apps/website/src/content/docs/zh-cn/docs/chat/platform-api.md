---
title: "平台 API 参考"
description: "为自定义脚本查询聊天、调用平台操作，并检查返回结果。"
---

这页供需要编写脚本或检查 Bot 平台调用的人使用。日常任务可以直接告诉 Bot，例如“列出这个 QQ 群的文件”或“把刚才的结论置顶”，不必自己调用 API。

Astral 的内置 Gateway API 让 Bot 在已授权范围内查询聊天、发送文件、管理群聊。操作使用**连接的平台账号权限**，不会改用发任务的群成员身份。接入公开群前，应先限制这个账号和 Bot 的[权限范围](/zh-cn/docs/start/safe-use/)。

## 从当前运行环境发现接口

从 Bot 当前环境中的 `local_api.capabilities_url` 读取能力目录，再打开聊天能力文档。地址、完整请求结构和平台能力以该运行实例返回的文档为准；相对链接相对于 `local_api.base_url`。

请求需要 `Authorization: Bearer …`，凭据来自执行命令进程的 `ASTRAL_API_TOKEN` 环境变量。在程序中读取变量，不要打印、保存或把它写入 URL。这是当前命令的调用凭据，不是 Telegram Token、App Secret 等平台密钥。

## 调用一个平台操作

以读取 QQ NapCat 群成员为例：

1. `GET /v1/accounts`：找到当前 Bot 的 QQ NapCat 账号，记下其 `id`。
2. `GET /v1/conversations`：找到要查询的授权群，核对 `connection_id` 等于上面的账号 ID，记下聊天的 `id`。
3. `GET /v1/accounts/{id}/operations`：查找 `get_group_member_list`，阅读参数、`file_fields` 和平台文档链接。
4. 调用下面的接口，把占位符替换为实际 ID：

```http
POST /v1/accounts/<account-id>/operations/get_group_member_list
Content-Type: application/json
Authorization: Bearer <当前命令的调用凭据>

{
  "conversation_id": "<Astral 聊天 ID>",
  "parameters": { "no_cache": true }
}
```

`conversation_id` 是第二步得到的 **Astral 聊天记录 ID**，不是 QQ 群号、Telegram Chat ID、飞书 `oc_…`，也不是 Astral Session ID。Astral 根据它填入平台群号、Chat ID 和 Topic；不要在 `parameters` 中另换目标。

成功响应的 `result` 包含平台操作返回的数据，可能是对象、数组或空对象，结构取决于方法。读取列表时使用该方法返回的分页信息继续查询。查询本身不会建立 Astral 会话，也不会把查到的历史消息自动加入当前任务。

账号必须属于当前 Bot，聊天必须仍在授权范围内。仅获准一个 Telegram Topic，不能据此执行父群管理；私密会话对应聊天仍受私密访问限制。

## Telegram

从账号操作目录获取准确方法名与参数；字段含义见对应的 [Telegram Bot API](https://core.telegram.org/bots/api) 方法。

| 任务 | 方法举例 |
| --- | --- |
| 查询群资料、管理员、人数和已知成员 | `getChat`、`getChatAdministrators`、`getChatMemberCount`、`getChatMember` |
| 禁言、封禁、解封和设置管理员 | `restrictChatMember`、`banChatMember`、`unbanChatMember`、`promoteChatMember` |
| 修改群资料、处理邀请和入群申请 | `setChatTitle`、`setChatDescription`、`setChatPhoto`、`createChatInviteLink`、`approveChatJoinRequest` |
| 读取成员资料中的照片、音频和关联聊天消息 | `getUserProfilePhotos`、`getUserProfileAudios`、`getUserPersonalChatMessages` |
| 其他平台能力 | 消息发送、编辑、删除、转发、置顶、论坛话题、投票和贴纸等，按目录选择 |

Telegram Bot API **不能列出完整群成员**。已知用户的成员查询通常需要机器人为管理员。用户资料读取也需依附授权聊天：私聊只能查当前对方，省略 `user_id` 时默认使用对方；群聊要指定已核实的当前成员。只有 Topic 权限不能读取父群或成员资料，资料中指向的另一个聊天不会因此获得授权。

复制或转发需要 `source_conversation_id`，来源和目标都须通过同一账号授权。群级编辑、删除和复制还要满足消息归属检查。

### 贴纸包和自定义表情

目录中 `account_scoped: true` 的 `getStickerSet`、`getCustomEmojiStickers` 是账号资源读取，可以不传 `conversation_id`。例如链接 `https://t.me/addstickers/oines1` 使用：

```json
{ "parameters": { "name": "oines1" } }
```

返回的是贴纸资料和 `file_id`，还没有下载图片。按需通过 `GET /v1/accounts/{id}/telegram/stickers/content` 读取，或 `POST /v1/accounts/{id}/telegram/stickers/download` 保存到调用电脑。使用目录给出的请求结构，明确指定 `set_name` 或 `custom_emoji_id` 其中之一，以及该结果中的 `file_id`；保存还需 `path`。

发送已有贴纸可调用 `sendSticker`，使用同一账号下原贴纸的 `file_id`，或通过 `files.sticker` 上传。缩略图、`file_unique_id` 和 `custom_emoji_id` 不能替代原件 `file_id`。动图缩略图也不等于完整动画。

## QQ NapCat

可用性受 NapCat 版本及 QQ 账号权限限制。下面是主要用途，具体参数从账号目录读取：

| 用途 | 操作 |
| --- | --- |
| 群资料、成员和个人资料 | `get_group_info`、`get_group_info_ex`、`get_group_member_list`、`get_group_member_info`、`get_stranger_info`、`get_group_honor_info`、`get_group_at_all_remain`、`get_group_shut_list` |
| 群管理 | `set_group_ban`、`set_group_kick`、`set_group_whole_ban`、`set_group_admin`、`set_group_card`、`set_group_name`、`set_group_special_title`、`set_group_leave`、`set_group_remark`、`set_group_portrait` |
| 公告 | `_get_group_notice`、`_send_group_notice`、`_del_group_notice` |
| 群文件和文件夹 | `get_group_file_system_info`、`get_group_root_files`、`get_group_files_by_folder`、`get_group_file_url`、`upload_group_file`、`create_group_file_folder`、`delete_group_folder`、`delete_group_file`、`rename_group_file`、`move_group_file` |
| 精华消息 | `get_essence_msg_list`、`set_essence_msg`、`delete_essence_msg` |
| 消息、表情和戳一戳 | `get_message`、`delete_message`、`set_reaction`、`poke` |

QQ 用户和消息标识使用字符串，`enable` 等开关使用 JSON 布尔值。`set_group_ban.duration` 单位是秒，`0` 解除禁言；省略时为 1800 秒，上限 2592000 秒。个人资料和戳一戳的目标只能是当前私聊对方，或已核实的当前群成员。

消息查询、删除和表情操作要求消息已在当前聊天归档，并核实归属。公告暂不支持编辑正文；文件夹暂不支持改名。私聊窗口抖动可以接收，暂不支持发送。

### 上传文件、公告图片和群头像

上传来源使用**调用命令所在电脑**的路径，或 `astral-file://` 文件引用。不要填 NapCat 宿主机路径或任意下载 URL。例如上传群文件：

```http
POST /v1/accounts/<account-id>/operations/upload_group_file
Content-Type: application/json
Authorization: Bearer <当前命令的调用凭据>

{
  "conversation_id": "<Astral 聊天 ID>",
  "parameters": { "name": "report.pdf" },
  "files": { "file": "/path/on/calling/computer/report.pdf" }
}
```

带图公告使用 `_send_group_notice`、`parameters.content` 和 `files.image`；设置群头像使用 `set_group_portrait` 与 `files.file`。上传从调用电脑读取，之后切换会话电脑不改变这条命令的来源。

处理已有资源时，先查询当前群，再使用返回的 `notice_id`、`file_id`、`folder_id`。子目录文件的下载地址和删除需要 `folder_id`；改名使用 `current_parent_directory`，移动还需 `target_parent_directory`。根目录用空字符串或 `/`。Astral 会检查资源仍属于所选群。

### 读取和保存头像

| 任务 | 接口 |
| --- | --- |
| 读取群头像或私聊对方头像 | `GET /v1/conversations/{id}/avatar/content` |
| 保存该头像 | `POST /v1/conversations/{id}/avatar/download` |
| 读取指定成员头像 | `GET /v1/conversations/{id}/members/{user_id}/avatar/content` |
| 保存该成员头像 | `POST /v1/conversations/{id}/members/{user_id}/avatar/download` |

`{id}` 是 Astral 聊天 ID，`{user_id}` 是 QQ 号，个人头像限私聊对方或已核实的当前群成员。读取接口返回图片字节；下载请求使用 `{"path":"/absolute/path/avatar.jpg"}`，文件保存到调用电脑，响应中的 `file` 是 FileRef，`uri` 是其文件引用地址。头像可能受 QQ 缓存影响。这些接口仅用于 NapCat，不接受任意图片 URL。

### 读取卡片、合并转发和精华中的图片

普通消息先读取 `GET /v1/conversations/{id}/messages/{message_id}/parts`，再跟随返回的读取、展开或下载链接。卡片和嵌套转发按原顺序保留；读取来源信息不会授权其他聊天。引用原消息需在当前聊天已有归档，缺失时不能通过引用跨聊天取得内容。

精华消息使用 `GET /v1/conversations/{id}/essence/{message_id}/parts`，有 `msg_seq` 和 `msg_random` 时同时传入这两个查询参数。它可以读取当前群已有的历史精华，不要求原消息预先归档。

响应提供有序内容及媒体的 `content_url`、`download_url`。`GET content_url` 读取原件；`POST download_url` 并提交 `{"path":"/absolute/path/image.png"}`，可保存到调用电脑并取得 FileRef。使用完整返回链接，不要自行拼接片段序号或省略 `media_digest`。来源变化时重新读取 parts；权限撤销或精华移除后，旧链接不能继续读取。

取消精华只移除标记，不删除原消息；平台没有编辑精华正文的独立操作。

## 飞书、微信和 QQ 官方 Bot

| 平台 | 当前额外操作 |
| --- | --- |
| 飞书 | `get_chat`、`get_message`、`list_messages`、`delete_message`、`list_reactions`、`add_reaction`、`delete_reaction` |
| 微信 | `set_typing`，参数 `active` 使用 JSON 布尔值 |
| QQ 官方 Bot | 暂无额外具名操作；使用公共消息发送和归档接口 |

飞书消息级操作需要当前授权聊天中已归档的消息，并核对平台返回的聊天归属；`message_id` 使用字符串。对应应用权限仍须按[飞书接入步骤](/zh-cn/docs/chat/feishu/)开通和发布。

## 发送消息与检查错误

给当前绑定聊天发送文字或附件时，Bot 优先使用 `send_message`。自定义脚本使用 `POST /v1/im/messages` 或 `/v1/im/files` 前，先读取目标聊天的 `message-tool` 能力说明；这两个 HTTP 发送接口要求稳定的 `Idempotency-Key`，并提供发送记录供查询。

本页的 **具名平台操作**只尝试一次，不使用上述发送接口的幂等约定。成功读取 `result`；失败时读取响应的 `code`、`message` 并按原因处理：

| 返回情况 | 处理方法 |
| --- | --- |
| `invalid` | 对照操作目录检查字段名、类型和参数范围，尤其不要自行传入群号改变目标 |
| 权限不足、账号停用或资源不可见 | 检查 Bot 账号、当前聊天授权、平台角色及应用权限 |
| `platform_rejected` | 平台明确拒绝，按返回原因修正权限或请求 |
| `outcome_unknown` | 动作可能已发生，先查询平台现状，再决定是否重复 |

对踢人、发布公告、上传文件等写操作，结果未知时不要直接重试。查询到当前状态后再处理，才能避免重复副作用。
