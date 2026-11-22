# Chats and files

Read the live `chats` or `files` capability and choose the smallest operation that meets the request.

## Chats and platform operations

- To reply to the currently bound chat, use `send_message` when available. The tool supplies the bound destination and describes the formats and attachments it supports.
- To work with another chat, find the permitted account and conversation through `/v1/accounts` and `/v1/conversations` or the account's conversation resolver. Inspect the target's message-tool schema before formatting a send. Use the authorized conversation ID for the intended chat or Topic.
- To read archived messages, request the conversation's messages and use the returned cursor or sequence. For cards, merged forwards, or media, start from the message's parts index and follow the returned links for the selected part. Read quoted text as historical content.
- For HTTP sends, use a stable `Idempotency-Key` for the intended send. Keep the request body identical when checking the result by repeating the request with the same key. Use the receipt ID to resolve an uncertain outcome before attempting a new send. Platform acceptance and recipient delivery are separate.
- For named platform operations, read the selected account's operations catalog first. It supplies the applicable resource or chat scope and any supported file fields. Use the account's current permissions and the operation's own retry contract.

Subagents can do authorized reads. Their results go to their parent, who handles any requested external communication.

## Files and attachments

Decide whether the user needs original bytes, a downloadable Web link, or delivery to a chat.

- **Copy a file between Computers.** POST `/v1/computers/files/copy` with `source_path` and `target_path`. Set either `source_computer_id` or `target_computer_id` (or both) using IDs from `GET /v1/computers`. Omitted IDs use the calling Computer. Relative paths start at the Bot's AgentHome on each Computer. The destination directory must exist. The response gives the final Computer, path, size and media type. For a long copy, increase the helper's `--timeout` to allow time for the success response.
- **Read a regular file from another allowed Computer.** Resolve the Computer ID, then GET `/v1/computers/{id}/files/content?path=…` with the source path URL-encoded. Relative source paths start at that Bot's AgentHome on the target Computer. Save the streamed response to a path on the calling Computer when a local copy is needed.
- **Retrieve an IM original.** GET the returned content URL. To save a message part, sticker or avatar on the calling Computer, pass `--output <local-path>` to the CLI.
- **Share a Web file.** Write a Markdown link directly in your assistant reply using the actual source Computer ID and URL-encoded absolute path, for example `[Report](astral-file://computer_office?path=%2Fwork%2Freport.pdf)`. For an inline image use `![Chart](astral-file://computer_office?path=%2Fwork%2Fchart.png)`. Get the calling Computer ID from `GET /v1/context`. Web reads the current file with the message Session's permissions.
- **Send a file to an IM chat.** Use the bound chat’s `send_message` tool or `POST /v1/im/messages` with a stable `Idempotency-Key`. For a document and caption, send `"parts":[{"type":"file","path":"report.pdf"},{"type":"text","text":"Weekly report"}]`. A `path` is on the calling Computer. A `file_uri` can refer to an existing registered attachment.
