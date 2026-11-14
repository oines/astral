---
title: "Platform API reference"
description: "Query chats, download computer files, and call platform operations from custom scripts."
---

This reference is for writing scripts or inspecting the Bot's platform calls. For everyday tasks, ask the Bot directly: “List this QQ group's files” or “Pin the conclusion we just wrote.” You do not need to call an API yourself.

Astral's built-in Gateway API lets the Bot query chats, download files from allowed computers, send files, and manage groups within its authorized scope. Chat actions use **the connected platform account's permissions**, not the identity of the group member requesting a task. Limit the account's and Bot's [access](/docs/start/safe-use/) before connecting a public group.

## Discover APIs in the current environment

Read `local_api.capabilities_url` from the Bot's current environment, then open the chat or file capability document for your task. Use the addresses, request schemas, and platform capabilities returned by that instance. Resolve relative links against `local_api.base_url`.

Requests require `Authorization: Bearer …` using the executing command process's `ASTRAL_API_TOKEN` environment variable. Read it within your program without printing, saving, or putting it in a URL or another program's command-line arguments. This is the current command's credential, not a Telegram token or an app secret.

## Call a platform operation

To read a QQ NapCat group's members:

1. Call `GET /v1/accounts` and record the `id` of the current Bot's NapCat account.
2. Call `GET /v1/conversations`, find the authorized group, and check its `connection_id` matches that account. Record the chat's `id`.
3. Call `GET /v1/accounts/{id}/operations`. Find `get_group_member_list` and read its parameters, `file_fields`, and platform documentation link.
4. Make the following request, replacing the placeholder IDs:

```http
POST /v1/accounts/<account-id>/operations/get_group_member_list
Content-Type: application/json
Authorization: Bearer <current-command-credential>

{
  "conversation_id": "<Astral-chat-ID>",
  "parameters": { "no_cache": true }
}
```

`conversation_id` is the **Astral chat record ID** obtained in step 2, not a QQ group number, Telegram Chat ID, Feishu `oc_…`, or Astral Session ID. Astral derives the platform destination and Topic from it. Do not override the target in `parameters`.

A successful response contains the platform operation's data in `result`. It may be an object, array, or empty object depending on the method. Follow that method's pagination fields when reading lists. A query does not create an Astral conversation or automatically import historical messages into the current task.

The account must belong to the current Bot, and the chat must remain authorized. Access to one Telegram topic does not grant parent-group management. Chats bound to private conversations keep their private-access restrictions.

Upload operations stream files directly from the specified computer path or `astral-file://` reference, without creating an extra copy for sending. Keep the source file available and unchanged until the operation finishes.

## Download a file from an allowed computer

Call `GET /v1/computers` to find the source computer's `id`, then request `GET /v1/computers/{id}/files/content?path=…`. Each request checks that the source is still allowed for the current Bot, connected, and not revoked. The file must be readable by that computer's Daemon account.

URL-encode `path`. The source computer resolves it using its own filesystem rules: an absolute path refers to that computer, and a relative path starts at the Bot's AgentHome there. Home expansion such as `~/` and symbolic links follow those same rules. There is no additional directory allowlist. The endpoint reads regular files; it does not package directories.

For example, in Bash, replace the first two values with `local_api.base_url` from the current environment and an ID returned by `/v1/computers`:

```bash
base_url='<local_api.base_url>'
computer_id='<computer-id>'
curl --fail-with-body --config - \
  --get --data-urlencode 'path=reports/report.zip' \
  --output /tmp/report.zip \
  "${base_url%/}/v1/computers/$computer_id/files/content" <<EOF
header = "Authorization: Bearer $ASTRAL_API_TOKEN"
EOF
```

The response contains the original bytes. Here, `/tmp/report.zip` is on the computer running curl; `reports/report.zip` is on the selected source computer. The credential passes through curl's standard input rather than its command-line arguments. The request creates no FileRef, snapshot, or Server copy and does not switch the Session's selected computer.

There is no file-size or total-transfer-time limit. A canceled request or disconnected connection stops the transfer. A single `Range` request reads from the selected offset on the source Daemon, without transferring the preceding bytes to the Server.

## Read original file byte ranges

The `.../content` endpoints stream original bytes without saving a file. They accept one `Range` header when the source has a known positive size, for example `bytes=1048576-2097151`, `bytes=1048576-`, or `bytes=-65536`. A valid range returns **206**, `Accept-Ranges: bytes`, and the matching `Content-Range` and `Content-Length`. An invalid, multiple, or unsatisfiable range returns **416** with `Content-Range: bytes */<size>`.

Range support is needed in both the Server and the calling computer's Daemon, so update them together. An older Daemon does not forward the Range header; updating only the Server does not enable resuming downloads.

Without a usable known size, the endpoint ignores Range and returns the complete stream with **200**. Verify **206** and the returned range before appending to a partial download; download the complete file again if the source has changed. When using curl, reject non-2xx statuses with `--fail-with-body` or an explicit status check, and check its exit code for body-read failures too.

Astral does not cache the source file for these requests. Computer file requests seek on the source Daemon. For platform media, Astral may reopen the source and read and discard the preceding bytes, so a Range request does not guarantee that the platform CDN itself supports resume.

## Telegram

Read exact method names and parameters from the account's catalog. Field definitions are in the corresponding [Telegram Bot API](https://core.telegram.org/bots/api) method.

| Task | Example methods |
| --- | --- |
| Chat information, administrators, member count, and a known member | `getChat`, `getChatAdministrators`, `getChatMemberCount`, `getChatMember` |
| Restrict, ban, unban, and promote members | `restrictChatMember`, `banChatMember`, `unbanChatMember`, `promoteChatMember` |
| Chat settings, invitations, and join requests | `setChatTitle`, `setChatDescription`, `setChatPhoto`, `createChatInviteLink`, `approveChatJoinRequest` |
| Member profile photos, audio, and personal-chat messages | `getUserProfilePhotos`, `getUserProfileAudios`, `getUserPersonalChatMessages` |
| Other platform capabilities | Sending, editing, deleting, forwarding, pins, forum topics, polls, stickers, and more; select from the catalog |

Telegram Bot API **cannot enumerate the complete group membership**. Looking up a known member generally requires administrator status. User-profile reads also need an authorized chat: a private chat can query only its peer and defaults `user_id` to that peer; a group requires a verified current member. Topic-only access does not grant parent-group or member-profile access. Linked chats in profile results do not become authorized.

Copy and forward operations use `source_conversation_id`; source and destination must both be authorized through the same account. Chat-wide editing, deletion, and copying also enforce message ownership.

### Sticker sets and custom emoji

`getStickerSet` and `getCustomEmojiStickers` are marked `account_scoped: true` in the catalog. These resource reads can omit `conversation_id`. For `https://t.me/addstickers/oines1`, use:

```json
{ "parameters": { "name": "oines1" } }
```

The result contains sticker metadata and `file_id` values, not downloaded images. Read a selected file through `GET /v1/accounts/{id}/telegram/stickers/content`, or save it with `POST /v1/accounts/{id}/telegram/stickers/download`. Follow the advertised request schema: specify exactly one of `set_name` or `custom_emoji_id`, plus a `file_id` from that result. Saving also requires `path`.

Send an existing sticker with `sendSticker` using the original sticker's `file_id` from the same account, or upload through `files.sticker`. A thumbnail, `file_unique_id`, or `custom_emoji_id` cannot replace the original `file_id`. An animation's thumbnail is not its full animation.

## QQ NapCat

Availability depends on the installed NapCat version and QQ account permissions. Use the account catalog for each operation's exact parameters:

| Purpose | Operations |
| --- | --- |
| Group information, members, and profiles | `get_group_info`, `get_group_info_ex`, `get_group_member_list`, `get_group_member_info`, `get_stranger_info`, `get_group_honor_info`, `get_group_at_all_remain`, `get_group_shut_list` |
| Group management | `set_group_ban`, `set_group_kick`, `set_group_whole_ban`, `set_group_admin`, `set_group_card`, `set_group_name`, `set_group_special_title`, `set_group_leave`, `set_group_remark`, `set_group_portrait` |
| Notices | `_get_group_notice`, `_send_group_notice`, `_del_group_notice` |
| Group files and folders | `get_group_file_system_info`, `get_group_root_files`, `get_group_files_by_folder`, `get_group_file_url`, `upload_group_file`, `create_group_file_folder`, `delete_group_folder`, `delete_group_file`, `rename_group_file`, `move_group_file` |
| Essence messages | `get_essence_msg_list`, `set_essence_msg`, `delete_essence_msg` |
| Messages, reactions, and pokes | `get_message`, `delete_message`, `set_reaction`, `poke` |

Use strings for QQ user and message IDs, and JSON booleans for switches such as `enable`. `set_group_ban.duration` is in seconds: `0` unmutes, the default is 1800, and the maximum is 2592000. Profile reads and pokes can target only the current private peer or a verified current group member.

Message lookup, deletion, and reactions require an archived message in the selected chat and a verified association. Notice text cannot currently be edited, and folders cannot be renamed. Private window shakes can be received but not sent.

### Upload files, notice images, and group avatars

Upload sources are paths on **the computer running the calling command**, or `astral-file://` references. Do not use paths on the NapCat host or arbitrary download URLs. For example:

```http
POST /v1/accounts/<account-id>/operations/upload_group_file
Content-Type: application/json
Authorization: Bearer <current-command-credential>

{
  "conversation_id": "<Astral-chat-ID>",
  "parameters": { "name": "report.pdf" },
  "files": { "file": "/path/on/calling/computer/report.pdf" }
}
```

For an illustrated notice, use `_send_group_notice` with `parameters.content` and `files.image`. For a group avatar, use `set_group_portrait` with `files.file`. Uploads read from the calling computer; switching the conversation's computer later does not change that command's source.

Query the current group's resources before using returned `notice_id`, `file_id`, or `folder_id` values. Downloads and deletion for files in a subfolder require `folder_id`. Renaming uses `current_parent_directory`; moving also needs `target_parent_directory`. Use an empty string or `/` for the root. Astral checks that resources still belong to the selected group.

### Read and save avatars

| Task | Endpoint |
| --- | --- |
| Read the group avatar or private peer's avatar | `GET /v1/conversations/{id}/avatar/content` |
| Save that avatar | `POST /v1/conversations/{id}/avatar/download` |
| Read a member's avatar | `GET /v1/conversations/{id}/members/{user_id}/avatar/content` |
| Save that member's avatar | `POST /v1/conversations/{id}/members/{user_id}/avatar/download` |

`{id}` is an Astral chat ID and `{user_id}` is a QQ number. Personal avatars are limited to the private peer or a verified current group member. Content endpoints return image bytes. Download requests use `{"path":"/absolute/path/avatar.jpg"}` and save on the calling computer; `file` in the response is a FileRef and `uri` is its reference address. QQ may cache avatars. These endpoints are NapCat-only and do not accept arbitrary image URLs.

### Read images in cards, merged forwards, and essence messages

For an ordinary message, start with `GET /v1/conversations/{id}/messages/{message_id}/parts`, then follow the returned read, expand, or download links. Cards and nested forwards keep their original order. Source metadata does not grant access to other chats. Quoted originals must already be archived in the current chat; a quote cannot retrieve missing content across chats.

For an essence message, use `GET /v1/conversations/{id}/essence/{message_id}/parts`. Include both `msg_seq` and `msg_random` query parameters when available. This can read an existing historical essence entry without a previously archived original message.

Results include ordered content and media `content_url` and `download_url` links. `GET content_url` reads the original. `POST download_url` with `{"path":"/absolute/path/image.png"}` saves it on the calling computer and returns a FileRef. Use the complete returned links; do not construct part indexes or omit `media_digest`. Refresh parts when the source changes. Old links stop working if permission is revoked or the essence entry is removed.

Removing an essence mark keeps the original message. There is no separate operation to edit the essence message's text.

## Feishu, Weixin, and official QQ Bots

| Platform | Additional operations |
| --- | --- |
| Feishu | `get_chat`, `get_message`, `list_messages`, `delete_message`, `list_reactions`, `add_reaction`, `delete_reaction` |
| Weixin | `set_typing`, with JSON boolean parameter `active` |
| Official QQ Bot | No additional named operations; use shared send and archive endpoints |

Feishu message operations require an archived message in the authorized chat and verify the chat returned by the platform. Use a string for `message_id`. Grant and publish the app permissions described in [Feishu setup](/docs/chat/feishu/).

## Send messages and handle errors

The Bot should use `send_message` for text or attachments in its currently bound chat. Before using `POST /v1/im/messages` or `/v1/im/files` from a script, read the target chat's `message-tool` capability. These HTTP send endpoints require a stable `Idempotency-Key` and provide send records for outcome checks.

In Telegram chats, you can attach the original MP3 as a document. To display it in Telegram's music player, use the named `sendAudio` operation.

**Named platform operations** on this page make one attempt and do not use that send-idempotency contract. Read `result` on success. On failure, inspect `code` and `message`:

| Result | Action |
| --- | --- |
| `invalid` | Check field names, types, and limits against the operation catalog; do not supply a group ID to redirect the target |
| Insufficient permission, disabled account, or invisible resource | Check Bot ownership, chat access, platform roles, and app permissions |
| `platform_rejected` | The platform explicitly refused; correct the reported permission or request problem |
| `outcome_unknown` | The action may have happened; inspect the platform before deciding to repeat it |

Do not immediately retry a ban, notice publication, file upload, or other write with an unknown result. Check current platform state first to avoid duplicate effects.
