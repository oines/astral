---
title: "平台 API"
description: "通过 Gateway 查询成员与资料、管理群聊，并操作公告、群文件和精华消息。"
---

Bot 可以通过内置 Gateway API 查询群成员、管理群聊、发布公告和处理群文件等。操作使用所连接平台账号的权限，不会再按任务请求者的群角色授权。接入公开群前，先按[安全建议](/zh-cn/docs/start/safe-use/)限制账号和电脑权限。

## 发现与调用

Bot 从当前环境的 `local_api.capabilities_url` 读取能力入口，再阅读聊天能力文档。所有请求都需要使用当前命令环境中的 `ASTRAL_API_TOKEN` 设置 `Authorization: Bearer …`；不要打印 Token，也不要将它放进 URL。

1. 用 `GET /v1/accounts` 找到当前 Bot 的账号。
2. 用 `GET /v1/conversations` 查找授权聊天，按 `connection_id` 选择该账号的聊天，取得 Astral 的 `conversation_id`。
3. 用 `GET /v1/accounts/{id}/operations` 读取该账号的操作目录，其中包含操作名称、参数、可上传的 `file_fields` 和平台文档链接。
4. 调用 `POST /v1/accounts/{id}/operations/{operation}`。请求的 `parameters` 使用目录中的字段；`conversation_id` 确定授权聊天。平台原始结果保留在响应的 `result` 中。

例如，NapCat 查询当前群成员时，选择 `get_group_member_list`：

```json
{
  "conversation_id": "<Astral 聊天 ID>",
  "parameters": { "no_cache": true }
}
```

群号、Telegram Chat ID 和 Topic 由 Astral 根据已授权聊天填入。不能在参数中换成另一个群号，也不能用一个账号操作另一个账号的聊天。查询不会自动创建会话或导入旧消息。

## Telegram

现有具名操作覆盖成员与群资料查询、禁言与封禁、管理员权限、群名称与说明、头像、邀请链接、入群申请、置顶、话题，以及消息发送和编辑。读取账号目录中的准确方法名与参数，平台说明见 [Telegram Bot API](https://core.telegram.org/bots/api)。

| 任务 | 方法举例 |
| --- | --- |
| 群资料、管理员、人数、指定成员 | `getChat`、`getChatAdministrators`、`getChatMemberCount`、`getChatMember` |
| 禁言、封禁、解封、设置管理员 | `restrictChatMember`、`banChatMember`、`unbanChatMember`、`promoteChatMember` |
| 群设置、邀请与审批 | `setChatTitle`、`setChatDescription`、`setChatPhoto`、`createChatInviteLink`、`approveChatJoinRequest` |
| 用户资料中的照片、音频和关联聊天消息 | `getUserProfilePhotos`、`getUserProfileAudios`、`getUserPersonalChatMessages` |

**Telegram Bot API 不能列出完整群成员。** 可以查询管理员、成员总数和已知用户 ID 的成员信息；查询其他成员通常要求 Bot 为管理员。

用户资料读取仍依附于授权聊天：私聊省略 `user_id` 时查询当前对方，不能改成其他人；群聊必须指定当前群成员的 `user_id`。只有 Topic 权限时，不能据此读取父群或成员资料。资料中的媒体返回 Telegram 元数据与文件标识；关联聊天消息不会取得另一个聊天的授权，也不会变成新收到的 IM 消息。

## QQ NapCat

下面列出 QQ NapCat 支持的操作，具体可用性取决于安装版本和账号权限。目录同时提供简短名称和原生操作名。

| 对象 | 操作 |
| --- | --- |
| 群与成员、资料 | `get_group_info`、`get_group_info_ex`、`get_group_member_list`、`get_group_member_info`、`get_stranger_info`、`get_group_honor_info`、`get_group_at_all_remain`、`get_group_shut_list` |
| 群管理 | `set_group_ban`、`set_group_kick`、`set_group_whole_ban`、`set_group_admin`、`set_group_card`、`set_group_name`、`set_group_special_title`、`set_group_leave`、`set_group_remark`、`set_group_portrait` |
| 公告 | `_get_group_notice`、`_send_group_notice`、`_del_group_notice` |
| 文件与文件夹 | `get_group_file_system_info`、`get_group_root_files`、`get_group_files_by_folder`、`get_group_file_url`、`upload_group_file`、`create_group_file_folder`、`delete_group_folder`、`delete_group_file`、`rename_group_file`、`move_group_file` |
| 精华消息 | `get_essence_msg_list`、`set_essence_msg`、`delete_essence_msg` |

`user_id` 等标识建议用字符串，`enable` 等开关使用 JSON 布尔值。`set_group_ban` 的 `duration` 单位为秒，`0` 解除禁言，未传时为 1800 秒，上限 2592000 秒。资料卡只能查询当前私聊对象或已核实的当前群成员。

公告支持查询、发布和删除，暂不支持编辑正文。群文件可以改名、移动和删除；文件夹支持新建、查询和删除，暂不支持改名。

### 读取和保存头像

QQ NapCat 聊天可以通过 Gateway 直接读取头像，或保存到电脑：

| 任务 | 接口 |
| --- | --- |
| 读取群头像或私聊对方头像 | `GET /v1/conversations/{id}/avatar/content` |
| 将该头像保存到调用电脑 | `POST /v1/conversations/{id}/avatar/download` |
| 读取指定个人头像 | `GET /v1/conversations/{id}/members/{user_id}/avatar/content` |
| 将该个人头像保存到调用电脑 | `POST /v1/conversations/{id}/members/{user_id}/avatar/download` |

`{id}` 使用已授权的 Astral 聊天 ID，`{user_id}` 使用 QQ 号。个人头像只能读取私聊对方，或经核实仍在所选群内的成员。这些接口仅适用于 QQ NapCat，不适用于 QQ 官方 Bot。

读取接口返回图片字节。保存时向对应的下载接口提交 `{"path":"/path/on/calling/computer/avatar.jpg"}`；响应中的 `file` 是 FileRef，`uri` 是文件引用地址，文件保存在当前命令的来源电脑。图片来自 QQ 头像服务，可能存在平台缓存；接口不接受任意图片 URL。

### 上传与文件操作

上传使用来源电脑的路径或 `astral-file://` 引用，不接受 NapCat 宿主机路径或通过参数提供任意 URL。例如，选择 `upload_group_file`：

```json
{
  "conversation_id": "<Astral 聊天 ID>",
  "parameters": { "name": "report.pdf" },
  "files": { "file": "/path/on/calling/computer/report.pdf" }
}
```

发布带图公告时，选择 `_send_group_notice`，提供 `parameters.content` 和 `files.image`。设置群头像使用 `set_group_portrait` 与 `files.file`。文件从来源电脑流式上传，切换当前会话的电脑不会改变已有命令的路径来源。

先查询当前群的公告或目录，再使用返回的 `notice_id`、`file_id`、`folder_id`。子目录中文件的下载地址和删除操作需要同时提供 `folder_id`；改名使用 `current_parent_directory`，移动还需 `target_parent_directory`。根目录写成空字符串或 `/`。Astral 会再次核实这些资源属于当前授权群。

### 图文精华

精华列表保留每条消息的有序 `content`，包括文字、图片及平台实际提供的其他媒体，同时保留原消息标识。不会把图片压成文字摘要，也不会因查询列表而批量下载图片或导入聊天历史。

需要查看图片时，读取 `GET /v1/conversations/{id}/essence/{message_id}/parts`，并在可用时同时提供精华条目的 `msg_seq` 和 `msg_random` 查询参数。响应保留内容顺序，并提供每个媒体的 `content_url` 和 `download_url`。这些链接携带稳定的精华身份，即使 NapCat 后来找回原消息、改变短消息 ID，也仍指向同条精华。

`GET content_url` 按需读取原件；向 `download_url` 发出 `POST`，请求体为 `{"path":"/path/on/calling/computer/image.png"}`，可保存到当前命令的来源电脑并取得 FileRef。始终使用返回的完整媒体链接，其中的必需参数 `media_digest` 用来核对选中来源，不能仅凭片段序号自行拼接链接。如果原消息恢复后内容顺序变化，旧链接会要求重新读取 parts，避免下载另一张图。

每次都会重新检查当前群的精华列表，原消息无需先被 Astral 归档。精华已移除或权限已撤销时，旧链接不能继续读取。图片响应保留真实媒体类型，不把没有文件名的 PNG 或 GIF 一律标为 JPEG。

设为精华前核实原消息所在群；取消精华从当前群的精华列表确认目标，因此也能处理 Astral 接入前的历史精华。取消精华只移除标记，不删除原消息。平台没有独立的“编辑精华正文”操作。

## 写操作的结果

具名平台操作只尝试一次，不使用消息发送接口的 `Idempotency-Key` 合约。收到 `outcome_unknown` 时，先查询平台状态，再决定是否重复踢人、发布公告或上传文件。权限不足、接口不可用或平台拒绝时，按返回的错误处理。