---
title: "Connect Telegram"
description: "Use a Bot Token for private chats, groups, and forum topics."
---

Prepare a Telegram Bot you manage and ensure the Astral Server can reach Telegram's API.

## Connect the account

1. Create a Bot through BotFather and obtain its Bot Token.
2. Open **Settings → Bots → your Bot → Connect a platform → Telegram**.
3. Enter an account name and the token, then connect. Normally leave the server address at its default.
4. Message the Bot privately or add it to your group.

Astral receives updates through long polling. An active webhook for the same Bot prevents connection validation. Stop the previous receiver and remove its webhook before handing it over to Astral.

## Find the Chat ID

In a chat where the Bot can receive and reply, send:

```text
/chat_id
```

In groups, use `/chat_id@your_bot_username`. Astral replies with the Chat ID and, in a forum topic, the Topic ID. This utility needs no model or computer and does not authorize the chat.

Add these identifiers in Connected chats and choose a session. Forum topics use the parent group's Chat ID plus a separate Topic ID.

## Use groups

Authorize the chat in Astral, then mention the Bot or reply to it. To receive ordinary group messages as context, configure privacy mode or administrator permissions according to [Telegram's privacy rules](https://core.telegram.org/bots/features#privacy-mode).

Ordinary replies stay in the group's existing session, including replies to another member that also mention the Bot. Actual Topics keep their own authorization and session; a reply's thread ID alone does not create a Topic. This applies to new incoming messages, without moving or replaying older archived messages.

## Replies and selected quotes

The Bot receives a preview of the original message as well as the exact fragment you selected when Telegram supplies a quote. Quoting text that mentions the Bot does not itself notify it; mention the Bot in your new message or reply to a message it sent.

Referenced images, files and stickers retain their original message identity and download reference. They are kept separate from attachments on your new message and are not automatically downloaded or shown as image previews. The Agent can download the original when needed. This also works when Telegram includes the original media in a reply but Astral did not receive that original message separately. An external quote does not grant access to another chat or its files.

## Message reactions

Astral subscribes to Telegram reaction updates. Receiving them requires the Bot to be a chat administrator; private-chat notifications are not guaranteed. Additions, removals and anonymous count changes enter context as separate events targeting the original message, with only the actor or counts actually supplied. They do not rewrite that message. Anonymous totals may arrive late and do not identify an actor. Topic routing uses the original message's archive; unknown targets are never broadcast to all topics.

The Agent can use the existing Computer API's `setMessageReaction` operation in an authorized chat. Telegram does not echo reactions set by bots, so Astral records the accepted selection itself without waking the Bot again. Anonymous totals and reactions with an unknown actor are passive context only, without starting a participation check; a working Bot can still read them. Group reactions from a known external actor follow optional proactive rules; private-chat reactions do not directly wake the Bot as new private messages.

## Files and troubleshooting

The Bot can send ordered text, native user mentions, images, and files. Markdown and HTML use Telegram Rich Messages, including media embedded between text sections. A mention can use the numeric user ID even without a username. Files stream from the computer to Telegram; no public image host is needed. Spaces and newlines in text are preserved. Use the Gateway capability documentation for the actual `parts` schema and account permissions.

Text, images, and files are supported. Request an image as an original document when needed. Telegram's own size and format limits still apply.

If connected but silent, check chat authorization, Topic ID, message visibility, model access, and computer availability. Channels also need the relevant sending permissions.
