# IM accounts and Computer API

Each account belongs to one Agent, called a **Bot** in the product UI. The [UI/UX specification](ui-ux-spec.md) defines the settings hierarchy: Settings → Bot list → Bot → platform → account list → account settings and chats. Each account has independent credentials and access settings. New accounts use `specified` scope and have no chats. Add a chat by its platform ID and optional forum Topic, then select a new Session or an existing unbound Session owned by the same Agent. Replacing a binding stops the original and target Session, retains both histories and fixes previously accepted sends to their original destination.

`all` scope allows API access to any reachable chat on that account. Private messages activate a Session immediately; a group's first mention or reply to the Bot activates it. Earlier messages remain in IMStore and are not prefilled. API resolution and sending do not activate a Session. Switching to `specified` retains every active binding and its settings. Switch to `specified` before removing an individual chat.

Ordinary messages continuously enter Core as passive observations, including when proactive mode is off. Core applies them at safe boundaries and compacts with its ordinary budget and summary machinery. No fixed history window truncates received messages. IMStore retains received events, accepted outgoing messages, edits and deletions supplied by the platform, without automatic expiry. Telegram Bot polling does not provide arbitrary deletion notifications; explicit successful delete operations are recorded.

## Find a Telegram Chat ID

Send `/chat_id` or `/chat_id@bot_username` in a private chat, group, forum Topic or channel the connected bot can receive and reply to. Astral replies in that chat with `Chat ID: …`; a Topic also includes `Topic ID: …` and the reply stays in that Topic. Use the addressed form in groups with privacy mode enabled.

This host utility works before adding or authorizing the chat and does not need a model or Computer. It discovers the chat for the picker but does not create a Session, grant access, invoke a model or schedule a Probe. Utility requests and replies stay outside normal model observations and have separate persisted receipts. Each incoming message can cause at most one send attempt, including after update replay or server restart; uncertain outcomes are not retried automatically. Edited commands and commands from bots are consumed without a reply. Commands addressed to a different bot, captions and text containing the command inside a longer message are ordinary messages, not utility requests.

## Proactive mode and output

Proactive mode is a per-chat setting, off by default. For groups with it enabled, ordinary messages become eligible for one Probe after 10 seconds of quiet or 60 seconds from the first unhandled message. Probe reads an observer view of Core context: summaries, IM observations and bounded tool activity, without assistant text or reasoning. Images are included only when its model supports them. It uses the Session's current model unless the Agent's Probe model overrides it.

The only decision is `decide({"participate":true|false})`. The first valid decision wins. Missing tools, invalid arguments or request failure cover the snapshot without waking the Agent or retrying that snapshot. A positive result supplies a neutral Host wakeup. It supplies no analysis or action instruction; the main Agent may remain silent. Mentions, replies and private messages bypass Probe. Turning proactive mode off or changing a binding invalidates outstanding results.

Bound Sessions remain normal Web Sessions with full tool use and history. Their title follows the chat and the sidebar shows its platform. Web input and direct IM triggers use Steer. Assistant text remains in Web. Only `send_message({text,reply_to?,attachments?})` or an explicit API call sends to IM. The tool supplies the current bound destination automatically and returns persisted receipts. Attachments are Computer paths or `astral-file://` references; text may be empty for attachment-only sends.

## Computer API

Read `local_api.base_url` from the current environment snapshot and append the paths below. The listener supplies Agent, Session and source Computer identity. Account credentials are never returned. Old processes retain their original Computer binding.

| Endpoint | Purpose |
| --- | --- |
| `GET /v1/accounts` | Owned account identities and chat scope |
| `POST /v1/accounts/{id}/conversations/resolve` | Resolve `{external_id,topic_id?}` without creating a Session |
| `GET /v1/conversations` | Known authorized chats |
| `GET /v1/conversations/{id}/messages?after=0&limit=100&q=` | Archive search; filtering happens before pagination; continue from `next_sequence` |
| `GET /v1/conversations/{id}/messages/{message_id}` | Latest archived version of one platform message |
| `GET /v1/conversations/{id}/messages/{message_id}/attachments/{ref}/content` | Original file bytes, streamed over the Daemon transport |
| `POST /v1/attachments/download` | `{conversation_id,event_id,attachment_ref,path}` writes to an explicit path on the calling Computer |
| `POST /v1/im/messages` | `{conversation_id?,text,reply_to?}` queues text |
| `POST /v1/im/files` | `{conversation_id?,file_uri?|path?,text?,reply_to?}` queues a file |
| `GET /v1/im/receipts/{id}` | Inspect queued, accepted, failed, blocked or unknown sends |
| `GET /v1/accounts/{id}/telegram/operations` | Explicit Bot chat operation names, parameters and official method documentation |
| `POST /v1/accounts/{id}/telegram/{operation}` | Invoke a named, scoped Telegram operation |
| `GET /v1/openapi.json` | Current request and response schemas |

Text and file sends require a stable `Idempotency-Key`. Reusing it with different content fails; replaying the same intent returns its existing receipt. Accepted means platform acceptance, not final delivery. Unknown outcomes require inspection before retry. A manual accepted resolution includes the platform message IDs and archives the sent fact.

IM reception does not create Computer files. Images are downloaded into memory and processed once by Core: orientation correction, proportional resizing to a longest edge of 2048, at most 2500 32px patches and at most 4.5 MiB of base64. Transparency and original/display dimensions are retained. Core history stores the actual model payload, so replay does not download or recompress it. Other attachments contain only identity, name, type, size and the original-download path.

## Named Telegram operations

The catalog follows the [Telegram Bot API](https://core.telegram.org/bots/api) chat methods, including message send/edit/delete/reactions, copy/forward, member and chat management, invites, forum Topics, chat-scoped commands, polls, stickers, media, games and invoices. The Bot still needs the permissions required by Telegram. Identity, credentials, webhook management and update consumption are outside this API.

A request supplies `conversation_id`, optional `source_conversation_id` for copy/forward operations, `parameters` using the method's documented fields, and optional `files` mapping multipart field names to Computer paths or FileRefs. Astral supplies `chat_id` and the selected Topic. File fields in nested media can refer to `attach://<field>`.

```json
{
  "conversation_id": "conversation_target",
  "source_conversation_id": "conversation_source",
  "parameters": { "message_id": 42, "disable_notification": true }
}
```

The target, copy/forward source and nested chat references each require access through the same account. Topic-only grants also require an archived message identity before a chat-wide edit, delete or copy can act on it. Chat-wide management requires access to the parent chat. Command scopes are restricted to `chat` or `chat_member` for the selected chat. Alternate inline-message and business identities are not Bot chat grants; direct-message Topics are not resolved by this version's forum Topic adapter.

Named operations return Telegram's result directly and archive accepted messages or deletion facts. They are not automatically retried. Use the queued text/file endpoints when persisted send-receipt reconciliation is needed.
