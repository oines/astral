# IM accounts and Computer API

Supported adapters: Telegram (`telegram.bot`), QQ official Bot (`qq.bot`), QQ NapCat (`qq.napcat`), Weixin ClawBot / iLink (`weixin.ilink`) and Feishu custom app Bot (`feishu.bot`). See the [platform setup guide](im-platforms.md) for credentials, permissions, protocol references and capability differences.

Each account belongs to one Agent, called a **Bot** in the product UI. The [UI/UX specification](ui-ux-spec.md) defines the settings hierarchy: Settings → Bot list → Bot → platform → account list → account settings and chats. Each account has independent credentials and access settings. New accounts use `specified` scope and have no chats. Add a chat by its platform ID and optional Topic when supported by the adapter, then select a new Session or an existing unbound Session owned by the same Agent. Replacing a binding stops the original and target Session, retains both histories and fixes previously accepted sends to their original destination.

`all` scope allows API access to any reachable chat on that account. Private messages activate a Session immediately; a group's first mention or reply to the Bot activates it. Earlier messages remain in IMStore and are not prefilled. API resolution and sending do not activate a Session. Switching to `specified` retains every active binding and its settings. Switch to `specified` before removing an individual chat.

Ordinary messages continuously enter Core as passive observations, including when proactive mode is off. Core applies them at safe boundaries and compacts with its ordinary budget and summary machinery. No fixed history window truncates received messages. IMStore retains received events, accepted outgoing messages, edits and deletions supplied by the platform, without automatic expiry. Telegram Bot polling does not provide arbitrary deletion notifications; explicit successful delete operations are recorded.

For currently bound Sessions, the Host overrides the compaction system prompt with the [continuity-preservation prompt](../internal/harness/prompts/im-compaction.md). It preserves task context, shared experiences, interaction patterns, ongoing topics and changes over time in a coherent summary without a mandatory task-checkpoint template. Binding changes and reopening refresh this selection. Unbound Sessions, including unbound forks, use Core's default prompt. Message order, tool-call/result preservation and compaction budgets remain governed by Core.

Deleting a Bot permanently removes its Sessions and owned Astral IMStore data, including received and outgoing events, delivery records, account credentials, chat bindings, cursors and account configuration. The Server persists deletion intent, rejects new work for that Bot and joins its workers before cleanup; retries and startup recovery finish the same deletion. Other Bots retain their data. External platform accounts, chats and messages, and Computer work files are kept.

Older development builds saved some Weixin protocol credentials without account ownership. If anonymous entries remain, deleting a Bot with a retained Weixin account fails before any deletion intent is committed. Normal runtime does not guess which Bot owns those entries. Stop Desktop or Server, back up its data directory, then run the one-time preparation command:

```sh
astral maintenance weixin-wire-ownership --data /path/to/server
```

It attributes entries only when retained account, chat or attachment identities prove their owner, and prints counts without credentials. Remaining anonymous entries produce a nonzero exit. The optional `--purge-unowned` flag explicitly clears the remaining anonymous Weixin protocol caches across all accounts and their unreferenced credentials. Other Weixin accounts may need a new incoming message before sending again. Attributed caches, conversation history and external platform messages are preserved. This command requires the Server process lock and refuses to create a new database or run alongside Desktop or Server.

## Find a Telegram Chat ID

Send `/chat_id` or `/chat_id@bot_username` in a private chat, group, forum Topic or channel the connected bot can receive and reply to. Astral replies in that chat with `Chat ID: …`; a Topic also includes `Topic ID: …` and the reply stays in that Topic. Use the addressed form in groups with privacy mode enabled.

This host utility works before adding or authorizing the chat and does not need a model or Computer. It discovers the chat for the picker but does not create a Session, grant access, invoke a model or schedule a Probe. Utility requests and replies stay outside normal model observations and have separate persisted receipts. Each incoming message can cause at most one send attempt, including after update replay or server restart; uncertain outcomes are not retried automatically. Edited commands and commands from bots are consumed without a reply. Commands addressed to a different bot, captions and text containing the command inside a longer message are ordinary messages, not utility requests.

## Telegram replies, Topics and selected quotes

An incoming `message_thread_id` alone does not identify a separate Topic. Ordinary group replies can carry a thread ID and remain in the group's existing Conversation and Session. Only `is_topic_message: true` together with a nonzero thread ID selects a Topic Conversation. This also preserves incoming Bot-private-chat Topics; `chat.is_forum` is not an additional routing requirement. Actual Topics retain independent bindings and authorization. This correction applies to new observations; existing misrouted archives are neither rewritten nor replayed.

`reply_to_message` retains its local message ID, sender and an original-message preview of at most 500 Unicode characters. When plain text is absent, the preview can use the readable Rich Message body, caption or sticker label. A selected Telegram `quote` is stored independently as optional `Observation.reply_quote` with `text`, `position` and `manual`. The complete supplied fragment is retained separately from the 500-character preview, including a valid 1,024-character quote. Its position is the sender's approximate UTF-16 offset in the original source; quote text is authoritative and is never reconstructed by slicing that source.

Model observations render the original preview as `<in-reply-to ...>` and the escaped selection as a separate `<quote position-utf16="…" manual="…">…</quote>`. A fragment remains visible without a local reply ID, such as a quote accompanying `external_reply`. An external message ID is not treated as a message ID in the current chat. Quote contents, including `@Bot`, do not create a mention trigger or grant access to another chat; current-message mentions and replies to the Bot retain their usual trigger rules.

## Referenced media and QQ notices

`Observation.reply_attachments` retains referenced media separately from the current message's `attachments`. Its `<attachment>` nodes live inside `<in-reply-to>` and identify the original message, attachment reference, type, filename, MIME type, size and same-chat original-download path. They are not automatically downloaded, converted into image previews, saved as Computer files or registered as FileRefs. The Agent can request the original through the advertised Computer API path when it needs it.

Telegram can supply original media metadata in a same-chat `reply_to_message` even when Astral never received that original as its own update. This metadata supports an authorized original download without fabricating a `message.created` event for the source. Cross-chat `external_reply` does not authorize media lookup. NapCat resolves reply metadata only from the current chat's existing archive; missing source text/media is explicitly marked unavailable, without an automatic `get_msg` or a lookup in another chat.

NapCat `notify.poke` becomes a separate `notice.poke` observation, not a user-authored message. Its readable text preserves the platform's `raw_info` action and custom suffix, replaces participant references with readable names and QQ IDs, and excludes image URLs, jump links and opaque UIDs. If readable platform wording is unavailable, a plain actor/target description is used. Group notices use `user_id` as actor; private notices use `sender_id` when supplied and keep `user_id` as the conversation peer. Another account poking the current Bot produces a `poke` trigger through the existing chat authorization and binding rules. Self pokes and pokes between other people are passive.

Recalls remain independent `message.deleted` observations describing the operator, original author and target message ID. Both notices render as escaped `<event id="…" kind="…" ...>description</event>` records; a poke has no fabricated message ID, and its `<im_trigger>` refers to the notice's event ID. The original Core observation and receipt remain unchanged. Archive lookup can expose the latest message state, while the ordered event stream retains the original and later notice.

NapCat notices without an upstream `event_id` receive a local identity once per received WebSocket frame. Pending-batch retries retain that identity; separately received identical pokes remain distinct. An explicit upstream event ID provides scoped deduplication. NapCat does not offer missed-history replay, and local identities do not promise exact deduplication across disconnection and upstream replay.

## Proactive mode and output

Proactive mode is a per-chat setting, off by default. For groups with it enabled, ordinary messages become eligible for one Probe after 10 seconds of quiet or 60 seconds from the first unhandled message. Probe reads an observer view of Core context: summaries, IM observations and bounded tool activity, without assistant text or reasoning. Images are included only when its model supports them. It follows the Session's current model and reasoning settings unless the Agent selects an explicit Probe model, which has its own reasoning override.

Each new Probe reads the currently bound Bot's latest persona to help judge whether that Bot should participate. The fixed Probe instructions state: “The Bot persona below describes the Bot you are evaluating, not you.” A nonblank persona follows under `Bot persona:` as its original text; blank persona omits that whole section. Dynamic observations remain in messages, with current time and Bot name in the final User message. This does not copy the main Agent's Host instructions, environment snapshot or tools into Probe: it still has only the forced `decide` tool. A saved persona takes effect on the next new Probe without rewriting a request that is already prepared.

The only decision is `decide({"participate":true|false})`. The first valid decision wins. Missing tools, invalid arguments or request failure cover the snapshot without waking the Agent or retrying that snapshot. A positive result supplies a neutral Host wakeup. It supplies no analysis or action instruction; the main Agent may remain silent. Mentions, replies and private messages bypass Probe. Turning proactive mode off or changing a binding invalidates outstanding results.

Bound Sessions remain normal Web Sessions with full tool use and history. Their title follows the chat and the sidebar shows its platform. Web input and direct IM triggers use Steer. Assistant text remains in Web. Only `send_message` or an explicit API call sends to IM. The tool supplies the current bound destination automatically. Its description and parameters reflect the selected adapter, and change with the binding without losing Session history. Attachments are Computer paths or `astral-file://` references; text may be empty for attachment-only sends.

Every direct IM trigger and positive Probe wakeup reminds the main Agent: `Direct assistant text is not sent to IM. To reply to this chat, you must call send_message.` Passive observations do not repeat this reminder. It does not require a response: the Agent may remain silent, and the reminder changes neither the tool's bound destination nor delivery semantics.

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

Start at `local_api.capabilities_url` from the current environment. The index links to purpose-specific documentation at `/v1/capabilities/{id}` for history, chats, files and environment. Each document contains behavior rules, examples and a self-contained OpenAPI subset generated from the actual routes, including its command Bearer security definition. The index also links the complete OpenAPI. Resolve relative links against `local_api.base_url`; the API address stays in the environment context.

Every Computer API request, including discovery and OpenAPI, requires `Authorization: Bearer …` using the calling `exec_command` process's `ASTRAL_API_TOKEN` environment variable. The Server issues a separate credential before dispatch, holds only its hash and execution binding in memory, and validates every forwarded request against that record, its original Daemon connection and current permissions. The Daemon injects the actual value only into the command environment. Read the value from the environment without printing, saving or expanding it into another process's arguments; it never belongs in a model-visible environment snapshot. Provider and account credentials are never returned.

An asynchronous command retains its credential until exit; `write_stdin` continues the same process without issuing another credential. Process exit, termination or connection loss revokes it, and Server restart invalidates all old credentials. Confirmed connection loss makes the Daemon terminate that connection's process trees before reconnecting; old commands are not recovered or automatically rerun. Previously accepted sends and other completed effects cannot be undone by revocation. Switching Computer only selects the destination of new calls: existing processes, credentials and `session_id` handles retain their source Computer while its connection remains alive.

The Host prompt points to this discovery entry and explains the command credential header. Endpoint paths, pagination, HTTP idempotency and file handling instructions live in the capability documents. Only currently IM-bound Sessions receive `send_message` and its matching output instructions; ordinary unbound Sessions use Web assistant text. This follows the current binding, regardless of where the Session was created or opened. The Host prompt does not mention Probe or assume proactive mode is enabled. Use the bound `send_message` tool for text and attachments supported by its schema; its execution identity is supplied by the Host, with no `Idempotency-Key` argument.

| Endpoint | Purpose |
| --- | --- |
| `GET /v1/capabilities` | Unified capability index, scope and documentation links |
| `GET /v1/capabilities/{id}` | Instructions, examples and actual request/response schemas for one capability |
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
| `GET /v1/accounts/{id}/operations` | Named operations, account-resource/chat scope, parameters and official documentation |
| `POST /v1/accounts/{id}/operations/{operation}` | Invoke a named operation within its declared account-resource or chat scope |
| `GET /v1/accounts/{id}/telegram/operations` | Explicit Telegram operation names, scope, parameters and official method documentation |
| `POST /v1/accounts/{id}/telegram/{operation}` | Invoke a named, scoped Telegram operation |
| `GET /v1/accounts/{id}/telegram/stickers/content` | Stream a selected original or thumbnail using `file_id` and exactly one of `set_name` or `custom_emoji_id` |
| `POST /v1/accounts/{id}/telegram/stickers/download` | The same resource source plus `path`; explicitly save to the calling Computer and return a FileRef |
| `GET /v1/openapi.json` | Current request and response schemas |

All platform chat and message IDs are strings. QQ uses `private:<user ID>` and `group:<group ID>` to preserve its distinct address spaces. Topic IDs are only accepted by adapters that support them.

HTTP text and file sends require a stable `Idempotency-Key`. Reusing it with different content fails; replaying the same intent reads its existing outcome rather than sending it again. Successful calls return accepted receipts; send failures return an HTTP error containing the receipt ID and actual send error. Receipt lookup remains available after cancellation or disconnection. Accepted means platform acceptance, not final delivery. Weixin may acknowledge only the sent `client_id`; a `client:` receipt identifies that accepted request, not a server-side message identity usable for quoting or editing. Files with text on Weixin or Feishu use separate ordered receipts for the text and file. An uncertain outcome stays `unknown`, with no automatic retry.

All adapters stream original file uploads and downloads without an Astral file-size ceiling. QQ and NapCat stream Base64 JSON; Weixin streams digest calculation and encryption from the immutable source Computer snapshot, reopening that snapshot for the encrypted upload. Server memory does not grow with the file, and files are not staged on Server disk. Platform errors, including an upstream size rejection, return to the caller. Telegram sends JPEG/PNG/WebP attachments with `sendPhoto` by default; `as_document:true` preserves an image as a document. Other files use `sendDocument`.

IM reception does not create Computer files. Before a new image enters passive observation context, preparation reads its original into a bounded buffer (20 MiB budget) and uses Core's shared image processing with an additional limit of 75,000 total display pixels. Processing corrects orientation, resizes proportionally without upscaling, and preserves transparency and original/display dimensions. Core's existing decode and encoded-size safeguards continue to apply. The observation explicitly labels the image as a compressed preview and tells the Agent to download the original when text or details need closer inspection; attachment identity and the original-download path are retained.

This processing happens once before observation admission. Probe, the main model and later compaction use the same frozen preview; request construction and replay do not download or recompress it. The `read` tool retains its existing image behavior. Existing history is not migrated, rewritten or prepared again. Images exceeding the inline preparation budget retain attachment metadata and a preview-omitted note, remain downloadable as streamed originals, and do not block later incoming messages. Other attachments contain only identity, name, type, size and the original-download path.

Telegram stickers follow the same file lifecycle: reception archives metadata and platform file references, without creating a separate attachment file on Server or Computer disk or registering a FileRef. The original attachment has `kind: "sticker"`, its Telegram `file_id` in `external_id`, and `sticker` metadata containing type, format (`static`, `animated`, or `video`), emoji, set name, dimensions and stable identity. Static WebP originals use the same 75,000-pixel observation preview preparation. Animated TGS and video WebM originals remain downloadable files; only Telegram's separate thumbnail is prepared as an image under the same limit. `sticker.thumbnail_ref` identifies that image attachment in the same message. The model sees `preview-only` and `preview-of`, so a thumbnail cannot be mistaken for the full animation. Missing or unusable sticker previews retain the metadata and original-download path rather than blocking delivery. Core history retains the prepared preview payload through the ordinary image path; a small static image may retain its original bytes as that payload. This does not create an independently saved attachment file.

An Agent that needs a file must explicitly download its original through the existing attachment API to a chosen Computer path. Sticker thumbnails remain separately downloadable, without replacing the original. Receiving a sticker and archiving a `sendSticker` result use the same normalization, and outgoing results have `self: true` with no inbound trigger.

## Named Telegram operations

The catalog follows the [Telegram Bot API](https://core.telegram.org/bots/api), including message send/edit/delete/reactions, copy/forward, member and chat management, invites, forum Topics, chat-scoped commands, polls, stickers, media, games and invoices. The Bot still needs the permissions required by Telegram. Identity, credentials, webhook management and update consumption are outside this API.

A request supplies `conversation_id`, optional `source_conversation_id` for copy/forward operations, `parameters` using the method's documented fields, and optional `files` mapping multipart field names to Computer paths or FileRefs. Astral supplies `chat_id` and the selected Topic. File fields in nested media can refer to `attach://<field>`.

Send stickers through the existing named `sendSticker` operation; the `send_message` tool and ordinary text/file sends are unchanged. Set `parameters.sticker` to the original attachment's `external_id` (shown as `platform-file-id` in the model observation) to reuse a sticker in the same Telegram Bot account, or supply `files.sticker` with an explicit Computer path or FileRef for a multipart upload. A thumbnail's file ID cannot be reused as a sticker, and `file_unique_id` and `custom_emoji_id` are not substitutes for `file_id`. Static WebP, animated TGS and video WebM uploads remain subject to Telegram's validation. Named operations make one attempt; an uncertain send outcome must not be automatically repeated.

```json
{
  "conversation_id": "conversation_target",
  "source_conversation_id": "conversation_source",
  "parameters": { "message_id": 42, "disable_notification": true }
}
```

The target, copy/forward source and nested chat references each require access through the same account. Topic-only grants also require an archived message identity before a chat-wide edit, delete or copy can act on it. Chat-wide management requires access to the parent chat. Command scopes are restricted to `chat` or `chat_member` for the selected chat. Alternate inline-message and business identities are not Bot chat grants; direct-message Topics are not resolved by this version's forum Topic adapter.

Named operations return Telegram's result directly and archive accepted messages or deletion facts. Each invocation makes one attempt.

### Sticker packs and custom emoji resources

[`getStickerSet`](https://core.telegram.org/bots/api#getstickerset) and [`getCustomEmojiStickers`](https://core.telegram.org/bots/api#getcustomemojistickers) have `account_scoped: true` in both operation catalogs. These are read-only resources of the selected, enabled Telegram account owned by the current Bot. They need no `conversation_id`, including in unbound Web Sessions and Topic-bound Sessions. If a conversation is explicitly supplied, it must still be accessible through that account. Chat access restrictions on all other operations are unchanged.

For `https://t.me/addstickers/oines1`, use `getStickerSet` with `{"parameters":{"name":"oines1"}}`. The name is the final component of an `addstickers` or `addemoji` link, not the full URL. The result contains the complete set's metadata and sticker list, including original `file_id` values and available thumbnails. `getCustomEmojiStickers` accepts `{"parameters":{"custom_emoji_ids":["<custom-emoji-id>"]}}`, with at most 200 IDs. Results retain Telegram's JSON shape. Neither method downloads image bytes, creates messages, activates a Session, or registers FileRefs.

Read a selected image on demand through the content endpoint, or explicitly save it with:

```http
POST /v1/accounts/<account-id>/telegram/stickers/download
Content-Type: application/json
Authorization: Bearer <calling-command-credential>

{
  "set_name": "oines1",
  "file_id": "<original-or-thumbnail-file-id-from-result>",
  "path": "/absolute/path/sticker.webp"
}
```

Supply exactly one source: `set_name`, or a single `custom_emoji_id`. The Server queries that resource again and accepts only a matching original or thumbnail file ID in its current result; a sticker set's own thumbnail is also readable. It never offers arbitrary `getFile` access or returns Telegram's credential-bearing file URLs. The content endpoint streams bytes with private, non-cacheable headers and creates no file. The download endpoint streams to the explicit path on the credential's original Computer and registers that file only after the write succeeds, even if the Session has switched Computers. There is no resource cache, automatic pack download, or synthetic IM observation.

Metadata is not image pixels: use the thumbnail's `file_id` to inspect a small preview, or the sticker's original `file_id` to download the original. A preview is not the full animation. Reuse the original `file_id` with the account's named `sendSticker` operation to send a selected sticker directly; this does not require first saving it locally. Thumbnail IDs cannot be used as sticker originals, and `send_message` remains unchanged.

## Additional platform operations

The account operation catalog uses the same Computer HTTP API and Agent view. No additional model tools are registered. Telegram's existing named-operation paths remain available. NapCat exposes chat/message lookup, deletion and selected group member operations; Feishu exposes chat/history/message lookup, deletion and reactions; Weixin exposes `set_typing`. QQ official Bot currently has no extra named operations; use the shared send and archive endpoints.

Chat operations supply `conversation_id` and an allowlisted `parameters` object; only the explicitly marked Telegram account-resource reads above may omit a chat. Every chat target belongs to the selected account and must pass its `all | specified` scope. Globally addressed messages must already be archived in that chat, and adapters also verify remote chat membership where needed. Parameters cannot supply a URL or override the destination. A successful delete appends the deletion fact to IMStore. Reading platform history does not backfill Core or activate a Session.

These direct operations make one attempt. Use the text/file endpoints for sends with persisted request identities. NapCat generic execution exceptions remain `unknown` in the internal send record, because its timeout response may follow submission to QQ.

QQ official and Feishu only contribute ordinary group context when platform permissions deliver those events. NapCat forward WebSocket has no offline replay. Weixin persists per-chat context tokens and media download secrets in encrypted Server records; receiving does not create Computer files. Its QR authorization is Owner-only, supports Tencent region redirects and phone verification, and never returns credentials to the browser.

A confirmed Weixin QR grant is the account identity proof. Persist it without an additional `getconfig` call; that API provides per-peer typing configuration and is not a login validation endpoint. Platform business errors preserve their safe numeric code in Owner API responses without forwarding raw remote error text.
