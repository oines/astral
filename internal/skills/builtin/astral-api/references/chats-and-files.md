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

- **Read a regular file from another allowed Computer.** Resolve the Computer ID, then GET `/v1/computers/{id}/files/content?path=…` with the source path URL-encoded. Relative source paths start at that Bot's AgentHome on the target Computer. Save the streamed response to a path on the calling Computer when a local copy is needed.
- **Retrieve an IM original.** Follow the attachment's returned content URL to read bytes, or its download operation to save them to an explicit destination on the calling Computer. Attachment names and preview metadata alone do not provide the original file.
- **Share a Web download link.** Create a file reference for the existing source file with `POST /v1/files/reference` and use the returned `astral-file://` URI. Set `snapshot` when the user needs the link to refer to the current version.
- **Send a file to an IM chat.** For the bound chat use `send_message` with its supported attachment format. For an authorized HTTP send, use the files capability's send operation and the same idempotency and receipt rules as text. Supply the source path or file URI in the format the live schema requests.
