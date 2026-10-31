---
title: "Platform APIs"
description: "Use the Gateway to query members and profiles, moderate chats, and manage notices, group files and essence messages."
---

The Bot can use its built-in Gateway API to query members, manage groups, publish notices, and work with group files. Actions use the connected platform account’s permissions, without a separate check of the requester’s group role. Review [safe-use guidance](/docs/start/safe-use/) before connecting a public group.

## Discover and invoke operations

Read `local_api.capabilities_url` from the current environment, then open the chats capability document. Every request needs `Authorization: Bearer …`, reading `ASTRAL_API_TOKEN` from the current command's environment. Do not print the token or put it in a URL.

1. Use `GET /v1/accounts` to find this Bot's accounts.
2. Use `GET /v1/conversations` to find an authorized chat, matching its `connection_id` to the selected account, and obtain its Astral `conversation_id`.
3. Read `GET /v1/accounts/{id}/operations` for supported names, parameters, upload `file_fields` and platform documentation.
4. Invoke `POST /v1/accounts/{id}/operations/{operation}` with the listed `parameters` and the authorized `conversation_id`. The response preserves the platform result in `result`.

For example, select NapCat's `get_group_member_list` operation with:

```json
{
  "conversation_id": "<Astral conversation ID>",
  "parameters": { "no_cache": true }
}
```

Astral derives the group, Telegram chat and Topic identifiers from the authorized conversation. Parameters cannot redirect a request to another group or account. Reading platform resources does not create sessions or import old messages.

## Telegram

Named operations cover chat and member information, restrictions and bans, administrator roles, titles, descriptions, photos, invitations, join requests, pins, Topics, message sending and editing. Inspect the account catalog for exact method names and parameters; see the [Telegram Bot API](https://core.telegram.org/bots/api) for platform details.

| Task | Example methods |
| --- | --- |
| Chat information, administrators, count and a known member | `getChat`, `getChatAdministrators`, `getChatMemberCount`, `getChatMember` |
| Restrict, ban, unban and promote | `restrictChatMember`, `banChatMember`, `unbanChatMember`, `promoteChatMember` |
| Chat settings, invitations and approvals | `setChatTitle`, `setChatDescription`, `setChatPhoto`, `createChatInviteLink`, `approveChatJoinRequest` |
| Profile photos, audio and personal-chat messages | `getUserProfilePhotos`, `getUserProfileAudios`, `getUserPersonalChatMessages` |

**The Telegram Bot API cannot enumerate every group member.** It can return administrators, the member count and membership for a known user ID. Checking other members generally requires the Bot to be an administrator.

Profile reads remain scoped to an authorized chat. In a private chat, an omitted `user_id` selects its peer and cannot select another person. A group requires an explicit current member's `user_id`. Topic-only access does not grant parent-group or profile access. Profile media returns Telegram metadata and file identifiers; personal-chat messages do not grant access to another chat or become incoming IM observations.

## QQ NapCat

The following operations are available through QQ NapCat, subject to the installed version and account permissions. The catalog provides short names alongside native operation names.

| Resource | Operations |
| --- | --- |
| Groups, members and profiles | `get_group_info`, `get_group_info_ex`, `get_group_member_list`, `get_group_member_info`, `get_stranger_info`, `get_group_honor_info`, `get_group_at_all_remain`, `get_group_shut_list` |
| Group management | `set_group_ban`, `set_group_kick`, `set_group_whole_ban`, `set_group_admin`, `set_group_card`, `set_group_name`, `set_group_special_title`, `set_group_leave`, `set_group_remark`, `set_group_portrait` |
| Notices | `_get_group_notice`, `_send_group_notice`, `_del_group_notice` |
| Files and folders | `get_group_file_system_info`, `get_group_root_files`, `get_group_files_by_folder`, `get_group_file_url`, `upload_group_file`, `create_group_file_folder`, `delete_group_folder`, `delete_group_file`, `rename_group_file`, `move_group_file` |
| Essence messages | `get_essence_msg_list`, `set_essence_msg`, `delete_essence_msg` |

Prefer strings for identifiers such as `user_id`; switches such as `enable` must be JSON booleans. `set_group_ban` takes `duration` in seconds: `0` unmutes, omission defaults to 1800, and the maximum is 2592000. Profile queries select only the private peer or a verified current group member.

Notices can be listed, posted, and deleted; editing their text is not currently supported. Group files can be renamed, moved, and deleted. Folders can be created, listed, and deleted, but not renamed.

### Read and save avatars

QQ NapCat chats also provide avatar resources through the Gateway:

| Task | Endpoint |
| --- | --- |
| Read a group avatar or private peer's avatar | `GET /v1/conversations/{id}/avatar/content` |
| Save that avatar to the calling Computer | `POST /v1/conversations/{id}/avatar/download` |
| Read a specific person's avatar | `GET /v1/conversations/{id}/members/{user_id}/avatar/content` |
| Save that person's avatar to the calling Computer | `POST /v1/conversations/{id}/members/{user_id}/avatar/download` |

Use an authorized Astral conversation ID for `{id}` and a QQ number for `{user_id}`. A person's avatar is limited to the private chat's peer or a current member verified in the selected group. These endpoints are available for QQ NapCat, not the official QQ Bot integration.

Content requests return image bytes. For either download endpoint, send `{"path":"/path/on/calling/computer/avatar.jpg"}`. The response contains `file` (a FileRef) and its `uri`; the file is saved on the current command's source Computer. The image comes from QQ's avatar service and may reflect its cache. An arbitrary image URL is not accepted.

### Uploads and file operations

Uploads use a path on the calling Computer or an `astral-file://` reference. A NapCat-host path or arbitrary URL in parameters is not accepted. For example, invoke `upload_group_file` with:

```json
{
  "conversation_id": "<Astral conversation ID>",
  "parameters": { "name": "report.pdf" },
  "files": { "file": "/path/on/calling/computer/report.pdf" }
}
```

For a notice with an image, use `_send_group_notice` with `parameters.content` and `files.image`. A group portrait uses `set_group_portrait` with `files.file`. Bytes stream from the source Computer; selecting a different Computer in the session does not change an existing command's path source.

Read the selected group's notices or directories before using a returned `notice_id`, `file_id` or `folder_id`. For a file inside a folder, URL and deletion operations also take `folder_id`. Renaming uses `current_parent_directory`; moving also takes `target_parent_directory`. The root directory is an empty string or `/`. Astral verifies that each resource belongs to the authorized group before invoking the platform.

### Essence messages with images

Essence results preserve each message's ordered `content`, including text, images and other media actually supplied by the platform, together with its message identity. Images are not flattened into a text summary. Listing does not download every image or import the messages into chat history.

To inspect an image, read `GET /v1/conversations/{id}/essence/{message_id}/parts`, supplying the entry's `msg_seq` and `msg_random` query parameters together when available. The response keeps content order and provides media `content_url` and `download_url` links. These links retain the stable essence identity even if NapCat later recovers the original and changes its short message ID.

`GET content_url` reads an original on demand. `POST` to `download_url` with `{"path":"/path/on/calling/computer/image.png"}` saves it on the current command's source Computer and returns a FileRef. Always use the complete returned media link: its required `media_digest` parameter verifies the selected source. Do not construct a media link from a part index alone. If recovering the original changes its part order, an old link requires a new parts read instead of downloading a different image.

Each request checks the group's current essence list; the original need not have been archived by Astral. Removed essence entries or revoked access invalidate subsequent reads through old links. Image responses preserve their actual media type instead of labeling every unnamed PNG or GIF as JPEG.

Adding essence checks the original message's group. Removing essence checks the selected group's current essence list, including entries from before Astral joined. Removal changes the essence marker and does not delete the original message. There is no separate action to edit an essence message's body.

## Mutation outcomes

Named platform operations make one attempt and do not use the message-send endpoints’ `Idempotency-Key` contract. On `outcome_unknown`, inspect the platform state before repeating a kick, notice, or upload. Follow the returned error for missing permissions, unavailable methods, or platform rejections.