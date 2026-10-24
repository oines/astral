# IM accounts and Computer API

Supported adapters: Telegram (`telegram.bot`), QQ official Bot (`qq.bot`), QQ NapCat (`qq.napcat`), Weixin ClawBot / iLink (`weixin.ilink`) and Feishu custom app Bot (`feishu.bot`). See the [platform setup guide](im-platforms.md) for credentials, permissions, protocol references and capability differences.

Each account belongs to one Agent, called a **Bot** in the product UI. The [UI/UX specification](ui-ux-spec.md) defines the settings hierarchy: Settings → Bot list → Bot → platform → account list → account settings and chats. Each account has independent credentials and access settings. New accounts use `specified` scope and have no chats. Add a chat by its platform ID and optional Topic when supported by the adapter, then select a new Session or an existing unbound Session owned by the same Agent. Replacing a binding stops the original and target Session, retains both histories and fixes previously accepted sends to their original destination.

`all` scope allows API access to any reachable chat on that account. Private messages activate a Session immediately; a group's first mention or reply to the Bot activates it. Earlier messages remain in IMStore and are not prefilled. API resolution and sending do not activate a Session. Switching to `specified` retains every active binding and its settings. Switch to `specified` before removing an individual chat.

Ordinary messages continuously enter Core as passive observations, including when proactive mode is off. Core applies them at safe boundaries and compacts with its ordinary budget and summary machinery. No fixed history window truncates received messages. IMStore retains received events, accepted outgoing messages, edits and deletions supplied by the platform, without automatic expiry. Telegram Bot polling does not provide arbitrary deletion notifications; explicit successful delete operations are recorded.

For currently bound Sessions, the Host overrides the compaction system prompt with the [continuity-preservation prompt](../internal/harness/prompts/im-compaction.md). It preserves task context, shared experiences, interaction patterns, ongoing topics and changes over time in a coherent summary without a mandatory task-checkpoint template. Binding changes and reopening refresh this selection. Unbound Sessions, including unbound forks, use Core's default prompt. Message order, tool-call/result preservation and compaction budgets remain governed by Core.

## Find a Telegram Chat ID

Send `/chat_id` or `/chat_id@bot_username` in a private chat, group, forum Topic or channel the connected bot can receive and reply to. Astral replies in that chat with `Chat ID: …`; a Topic also includes `Topic ID: …` and the reply stays in that Topic. Use the addressed form in groups with privacy mode enabled.

This host utility works before adding or authorizing the chat and does not need a model or Computer. It discovers the chat for the picker but does not create a Session, grant access, invoke a model or schedule a Probe. Utility requests and replies stay outside normal model observations and have separate persisted receipts. Each incoming message can cause at most one send attempt, including after update replay or server restart; uncertain outcomes are not retried automatically. Edited commands and commands from bots are consumed without a reply. Commands addressed to a different bot, captions and text containing the command inside a longer message are ordinary messages, not utility requests.

## Proactive mode and output

Proactive mode is a per-chat setting, off by default. For groups with it enabled, ordinary messages become eligible for one Probe after 10 seconds of quiet or 60 seconds from the first unhandled message. Probe reads an observer view of Core context: summaries, IM observations and bounded tool activity, without assistant text or reasoning. Images are included only when its model supports them. It follows the Session's current model and reasoning settings unless the Agent selects an explicit Probe model, which has its own reasoning override.

The only decision is `decide({"participate":true|false})`. The first valid decision wins. Missing tools, invalid arguments or request failure cover the snapshot without waking the Agent or retrying that snapshot. A positive result supplies a neutral Host wakeup. It supplies no analysis or action instruction; the main Agent may remain silent. Mentions, replies and private messages bypass Probe. Turning proactive mode off or changing a binding invalidates outstanding results.

Bound Sessions remain normal Web Sessions with full tool use and history. Their title follows the chat and the sidebar shows its platform. Web input and direct IM triggers use Steer. Assistant text remains in Web. Only `send_message` or an explicit API call sends to IM. The tool supplies the current bound destination automatically. Its description and parameters reflect the selected adapter, and change with the binding without losing Session history. Attachments are Computer paths or `astral-file://` references; text may be empty for attachment-only sends.

| Adapter | Body formats and extra parameters |
| --- | --- |
| Telegram | `text` (default), `markdown`, `html`; `disable_notification`, `as_document` |
| QQ official Bot | `text` (default), `markdown`, `markdown_template`; `markdown_template` contains `custom_template_id` and `params` |
| QQ NapCat | Literal text only; Markdown characters are sent unchanged. Ordinary images, files and reply references remain supported. No rich-message, JSON/XML-card or Markdown parameters. |
| Weixin iLink | `text` (default), partial `markdown`; no outbound `reply_to` |
| Feishu | `text` (default), `markdown`; optional Markdown post `title` |

Telegram Markdown/HTML use Rich Messages rather than MarkdownV2. Feishu Markdown uses a `post` containing an `md` block. QQ Markdown and templates depend on the account's platform permissions; rejection is reported rather than silently falling back to text. Weixin Markdown simplifies unsupported CJK italics and level 5/6 headings and preserves inline image references as readable text; actual images use attachments. Formatted bodies and files have separate ordered receipts. Format, platform parameters and prepared fragments are frozen in Outbox before dispatch. Retrying an intent does not reformat its source.

The tool returns the outcome of one send attempt: `sent` is `true` for acceptance, `false` for a known failure, or `null` when the platform result is unconfirmed; `message_ids` and `error` are included when available. Every result completes the tool call so the model can continue. Platform-specific parameters and acceptance rules belong to each adapter. Failure stops later fragments of the same send group. Outbox retains internal send records and prevents duplicate dispatch.

## Computer API

Read `local_api.base_url` from the current environment snapshot and append the paths below. The listener supplies Agent, Session and source Computer identity. Account credentials are never returned. Old processes retain their original Computer binding.

| Endpoint | Purpose |
| --- | --- |
| `GET /v1/accounts` | Owned account identities and chat scope |
| `POST /v1/accounts/{id}/conversations/resolve` | Resolve `{external_id,topic_id?}` without creating a Session |
| `GET /v1/conversations` | Known authorized chats |
| `GET /v1/conversations/{id}/message-tool` | Current adapter's `send_message` description and parameter schema |
| `GET /v1/conversations/{id}/messages?after=0&limit=100&q=` | Archive search; filtering happens before pagination; continue from `next_sequence` |
| `GET /v1/conversations/{id}/messages/{message_id}` | Latest archived version of one platform message |
| `GET /v1/conversations/{id}/messages/{message_id}/attachments/{ref}/content` | Original file bytes, streamed over the Daemon transport |
| `POST /v1/attachments/download` | `{conversation_id,event_id,attachment_ref,path}` writes to an explicit path on the calling Computer |
| `POST /v1/im/messages` | `{conversation_id?,text,reply_to?,format?,…}` sends text and waits for the platform result |
| `POST /v1/im/files` | `{conversation_id?,file_uri?|path?,text?,reply_to?,format?,…}` sends a file and waits for the platform result |
| `GET /v1/im/receipts/{id}` | Inspect queued, accepted, failed, blocked or unknown sends |
| `GET /v1/accounts/{id}/operations` | Named chat operations supported by this account, parameters and official documentation |
| `POST /v1/accounts/{id}/operations/{operation}` | Invoke a named operation within the selected account and chat scope |
| `GET /v1/accounts/{id}/telegram/operations` | Explicit Bot chat operation names, parameters and official method documentation |
| `POST /v1/accounts/{id}/telegram/{operation}` | Invoke a named, scoped Telegram operation |
| `GET /v1/openapi.json` | Current request and response schemas |

All platform chat and message IDs are strings. QQ uses `private:<user ID>` and `group:<group ID>` to preserve its distinct address spaces. Topic IDs are only accepted by adapters that support them.

Text and file sends require a stable `Idempotency-Key`. Reusing it with different content fails; replaying the same intent reads its existing outcome rather than sending it again. Successful calls return accepted receipts; send failures return an HTTP error containing the receipt ID and actual send error. Receipt lookup remains available after cancellation or disconnection. Accepted means platform acceptance, not final delivery. Weixin may acknowledge only the sent `client_id`; a `client:` receipt identifies that accepted request, not a server-side message identity usable for quoting or editing. Files with text on Weixin or Feishu use separate ordered receipts for the text and file. An uncertain outcome stays `unknown`, with no automatic retry.

All adapters stream original file uploads and downloads without an Astral file-size ceiling. QQ and NapCat stream Base64 JSON; Weixin streams digest calculation and encryption from the immutable source Computer snapshot, reopening that snapshot for the encrypted upload. Server memory does not grow with the file, and files are not staged on Server disk. Platform errors, including an upstream size rejection, return to the caller. Telegram sends JPEG/PNG/WebP attachments with `sendPhoto` by default; `as_document:true` preserves an image as a document. Other files use `sendDocument`.

IM reception does not create Computer files. Model image preparation reads an original image into a bounded buffer (20 MiB budget), then Core processes it once: orientation correction, proportional resizing to a longest edge of 2048, at most 2500 32px patches and at most 4.5 MiB of base64. Images exceeding the inline preparation budget are represented by attachment metadata and a preview-omitted note; they remain downloadable as original streamed attachments and do not block later incoming messages. Transparency and original/display dimensions are retained. Core history stores the actual model payload, so replay does not download or recompress it. Other attachments contain only identity, name, type, size and the original-download path.

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

Named operations return Telegram's result directly and archive accepted messages or deletion facts. Each invocation makes one attempt.

## Additional platform operations

The account operation catalog uses the same Computer HTTP API and Agent view. No additional model tools are registered. Telegram's existing named-operation paths remain available. NapCat exposes chat/message lookup, deletion and selected group member operations; Feishu exposes chat/history/message lookup, deletion and reactions; Weixin exposes `set_typing`. QQ official Bot currently has no extra named operations; use the shared send and archive endpoints.

Requests supply `conversation_id` and an allowlisted `parameters` object. Every target belongs to the selected account and must pass its `all | specified` scope. Globally addressed messages must already be archived in that chat, and adapters also verify remote chat membership where needed. Parameters cannot supply a URL or override the destination. A successful delete appends the deletion fact to IMStore. Reading platform history does not backfill Core or activate a Session.

These direct operations make one attempt. Use the text/file endpoints for sends with persisted request identities. NapCat generic execution exceptions remain `unknown` in the internal send record, because its timeout response may follow submission to QQ.

QQ official and Feishu only contribute ordinary group context when platform permissions deliver those events. NapCat forward WebSocket has no offline replay. Weixin persists per-chat context tokens and media download secrets in encrypted Server records; receiving does not create Computer files. Its QR authorization is Owner-only, supports Tencent region redirects and phone verification, and never returns credentials to the browser.

A confirmed Weixin QR grant is the account identity proof. Persist it without an additional `getconfig` call; that API provides per-peer typing configuration and is not a login validation endpoint. Platform business errors preserve their safe numeric code in Owner API responses without forwarding raw remote error text.
