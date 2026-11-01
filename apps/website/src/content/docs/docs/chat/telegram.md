---
title: "Connect Telegram"
description: "Create a Telegram bot, connect a private chat or group, and receive your first reply."
---

You need a **Telegram bot that you manage**. It is the account you talk to in Telegram; your Astral Bot provides its model, computers, and work context. Start with one chat, then add group and file workflows as needed.

Complete Astral's [First-time setup](/docs/start/first-steps/) first. The computer or server running Astral must be able to reach the Telegram API. Being able to open Telegram on your phone is not enough.

## 1. Create a bot and copy its token

1. Open the official [@BotFather](https://t.me/BotFather) in Telegram and send `/newbot`.
2. Follow the prompts to choose a name and username. Keep the username so you can find the bot later.
3. Copy the **Bot Token** returned by BotFather. It authorizes Astral to use this bot. Put it only in the connection form, not in a group message.

You can use an existing bot and its token. See [Telegram's creation instructions](https://core.telegram.org/bots/features#creating-a-new-bot). If another service is receiving its messages, stop that receiver first. Remove any webhook configured by the old service as well: Astral uses long polling, and an active webhook blocks connection.

## 2. Connect the account

Use the token at **Settings → Bots → your Bot → Connect a platform → Telegram**, normally keeping the default server address. Then open the bot's private chat in Telegram or add it to the target group.

## Find the Chat ID

The Chat ID tells Astral which chat you want to connect. You can get it in the chat itself.

Send this in the private chat:

```text
/chat_id
```

In a group, send `/chat_id@your_bot_username`, replacing the username with the one from BotFather. For example, use `/chat_id@my_astral_bot` for `my_astral_bot`.

Astral replies in the same chat with its **Chat ID**. In a forum topic, it also returns a **Topic ID**. This command works before you add the chat and does not use a model or computer.

## 3. Add the chat and test a reply

Under the account's **Connected chats**, select the discovered chat or find it with the returned Chat ID. Add it to a new conversation or an unbound conversation belonging to the same Bot. Forum topics need both the parent group's Chat ID and their Topic ID. New accounts allow only added chats; `/chat_id` itself does not grant access.

Then **send a new message** in Telegram: “Please reply in this Telegram chat: connection successful.” In a group, reply to the Bot's message containing the Chat ID, so the test also works with default privacy mode.

Check that Astral starts processing and that the reply actually arrives in Telegram.

## Use groups

Replying to the Bot's messages works with the default Telegram privacy settings. To let it follow ordinary discussion and receive more mentions, turn off the bot's group privacy mode in BotFather, then remove and re-add it to the group. Group administrators also receive group messages; grant only the management permissions you need. [Telegram privacy rules](https://core.telegram.org/bots/features#privacy-mode)

Astral's proactive group participation is off by default. Once ordinary messages can reach Astral, choose whether the Bot joins discussion in that group's reply setting. See [Group collaboration](/docs/chat/groups/).

Forum topics can have separate permissions and Astral conversations. Replying to a member in an ordinary group stays in the group's conversation; it does not create a separate topic binding.

## Images, files, quotes, and group management

Send images, files, or forwards and ask the Bot to read them and send its findings back. Replies and selected quotes retain the original preview, fragment, and media references supplied by Telegram. Mention the Bot in your new message or reply to the Bot itself when you need it to act; a name inside a quote is not a mention. Source metadata does not grant access to other chats.

The Bot supports text, native mentions, images, files, and Markdown/HTML rich messages. Ask for an image as an original document when needed. Files upload directly from the computer without an image host, subject to Telegram's size and format limits.

Ask the Bot to look up members, manage topics, pin messages, or set reactions. Receiving group reactions requires administrator status, and an ordinary reaction does not guarantee a reply. For custom calls, see [Platform APIs](/docs/chat/platform-api/).

## If the result is missing

| What happens | Next step |
| --- | --- |
| The account will not connect | Check the token, API access from the machine running Astral, and whether the old receiver and webhook are disabled |
| `/chat_id` gets no reply | Check that the account is enabled and the bot can receive and send in this chat; use the command with its username in groups |
| Chat ID works, but tasks do not run | Add the chat, choose its Astral conversation, and send a new task; check Topic ID for forum topics |
| Private chat works, but a group does not | First test by replying to a Bot message, then check privacy mode and Astral's group authorization |
| Astral has a result, but Telegram has no reply | Ask it to send the result to this chat and inspect platform send errors; channels also need sending permissions |

If a send times out or has an uncertain outcome, check Telegram before asking it to send again.
