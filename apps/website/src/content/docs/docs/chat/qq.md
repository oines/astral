---
title: "Connect QQ"
description: "Use a regular QQ account or an official QQ Bot to give Astral tasks in private chats and groups."
---

Choose the connection method for the account you want to use:

- **A regular QQ account for friends and groups:** follow [NapCat](#napcat) below. NapCat signs in to QQ; Astral handles its messages and tasks.
- **A bot you created on QQ Open Platform:** go to [Official QQ Bot](#official-qq-bot) and connect with its App ID and App Secret.

Both methods need an Astral Bot with a working [model and computer](/docs/start/first-steps/). Start with one chat you can test. For public groups, use a dedicated Bot and a [separate execution environment](/docs/start/safe-use/).

## NapCat

This route uses a regular QQ account and does not need QQ Open Platform credentials. Prepare the account Astral will use and **another QQ account to send test messages**.

The example below runs Astral and NapCat directly on the same computer. For Docker, a remote server, or separate computers, prepare the addresses with [NapCat networking](/docs/chat/napcat-network/) first, then return here.

### 1. Open NapCat and sign in to QQ

Check whether NapCat is already running. Reuse an existing instance through its management page to avoid duplicate logins or port conflicts. If it is not installed, follow the [official NapCat installation guide](https://napneko.github.io/guide/boot/Shell) for your operating system.

NapCat's startup log gives its **WebUI address and login token**. A common address is `http://127.0.0.1:6099/webui/`; use the actual port from your log. In this management page, sign in to the QQ account you want Astral to use. Scan with QQ on your phone when requested, follow any WebUI password-change prompt, and leave NapCat running with QQ online.

**WebUI manages NapCat. Its address and login token do not go into Astral.** Next, enable the services that Astral will connect to.

### 2. Enable two separate services

NapCat's **Network configuration** must have both:

- An **HTTP server**, used by Astral to call APIs such as sending a message.
- A **WebSocket server**, also called forward WebSocket, which delivers incoming QQ messages and notices to Astral.

Reuse suitable existing services and create only the missing ones. Both must belong to the same signed-in QQ account. Do not choose an HTTP client or reverse WebSocket client.

For two applications running directly on the same computer, use the example below. Existing services can keep their own port numbers.

| NapCat setting | HTTP server | WebSocket server |
| --- | --- | --- |
| Name | `astral-http` | `astral-ws` |
| Host | `127.0.0.1` | `127.0.0.1` |
| Port | `3000` | `3001` |
| Message format (`messagePostFormat`) | `Array` / `array` | `Array` / `array` |
| Token | Set an Access Token | Copy the same token from the HTTP server |
| Enabled / Enable after saving | On | On |

NapCat may generate different tokens for new services. You can keep the HTTP server's token and copy it into the WebSocket server, then into Astral. If you change an existing token, update other applications using that service too.

Give the services different names and confirm both are enabled after saving. Keep the default WebSocket heartbeat, such as `30000` milliseconds. “Report self messages” can remain off, and other options can keep their defaults. The HTTP form's “Enable Websocket” option does not replace the separate WebSocket server. See the [NapCat WebUI guide](https://napneko.github.io/config/basic) for its configuration fields.

### 3. Enable attachment access

In NapCat's **OneBot configuration**, enable **Local files to URL** (`enableLocalFile2Url`) and save. Labels can vary by version; use the configuration key to identify the option.

This lets NapCat provide local attachment content to Astral. A disk path on the NapCat host alone is not enough. Some images already have accessible URLs and can work without this option, so test a small file later as well.

### 4. Connect NapCat to Astral

Open **Settings → Bots → your Bot → Connect a platform → QQ** and choose **QQ NapCat**. Use the two service addresses and their shared token:

| Astral field | Same-host example | Where to get it |
| --- | --- | --- |
| NapCat HTTP address | `http://127.0.0.1:3000` | The HTTP server's host and port, without an action path such as `/get_login_info` |
| NapCat forward WebSocket address | `ws://127.0.0.1:3001` | The WebSocket server's host and port; use its event endpoint, not `/api` |
| Access token | The shared service token | Copy it from NapCat's service settings; enter only the value, without `Bearer ` |

Astral checks that QQ is online and that both services identify the same account. Use **Save connection** when changing the addresses or token later. You do not need an Astral callback URL, and CORS can keep its default.

Here, `127.0.0.1` means **the computer running Astral**. When accessing a remote Astral instance in a browser, it does not mean your browser's computer. Use the [matching network setup](/docs/chat/napcat-network/) for other deployments.

### 5. Add a chat and test from another QQ account

First join the target group with the NapCat account, or establish a working private contact with your test account, for example by adding each other as friends. Adding a chat in Astral does not add QQ friends or join a group.

New accounts use **Only specified chats** with an empty list. Add your test chat under **Connected chats** and choose its Astral conversation. Select a discovered chat, or find it by Chat ID:

| Chat | Chat ID format |
| --- | --- |
| Private chat with QQ account `123456789` | `private:123456789`, using **the other person's QQ number** |
| Group number `987654321` | `group:987654321`, using **the group number** |

Replace the sample numbers, check the returned name, and complete the addition. A new conversation is suitable for the first test; choosing an existing one continues its work context.

After adding the chat, send a new message from **another QQ account**:

> Please reply in this QQ chat with “Connection successful” and tell me which computer you are using.

In a group, use QQ's mention picker to **mention the account signed in to NapCat**. Typing its display name is not a mention. Messages sent by the account signed in to NapCat do not wake the Bot.

Check the task in its Astral conversation and the actual reply in QQ. Then send an image or small file and ask the Bot to read it and send it back. Groups start tasks when the Bot is mentioned or replied to by default; enable [proactive participation](/docs/chat/groups/) later if wanted.

## Official QQ Bot

This route uses a bot created on QQ Open Platform and does not need NapCat. Available users, groups, and message types depend on the permissions granted to that app by QQ.

1. Open or create your bot on [QQ Open Platform](https://q.qq.com/) and obtain **App ID** and **App Secret** from its app information.
2. Configure test users, test groups, or the published availability range, and request the private-chat, group-message, and sending permissions you need. Make sure your test user or group is included.
3. In Astral, open **Settings → Bots → your Bot → Connect a platform → QQ**, choose **QQ official Bot**, and connect using those credentials. Normally keep the default API and token addresses. Astral connects to the platform gateway without a public webhook.
4. Send the bot a message from an allowed user or group so Astral can discover the chat. Add it under the account's connected chats and choose an Astral conversation.
5. Send a new message asking “Please reply in this QQ chat: connection successful.” Mention the bot in groups. Check Astral's processing and the actual reply in QQ.

Prefer selecting a discovered chat; you do not need to find an openid first. For manual entry, use the actual `private:<user_openid>` or `group:<group_openid>` received by this app. **These openids are not QQ numbers or group numbers**, and they cannot be copied from another app.

Astral currently connects official QQ private chats and permitted groups, not QQ channels. Ordinary group messages, reply windows, Markdown, templates, and file sending depend on platform permissions and rules. This official group/private-chat adapter does not support message reactions.

## Images, files, and other QQ content

Once connected, ask the Bot to read images, files, QQ cards, or merged forwards and send its findings or files back to the chat. NapCat sends plain text, so Markdown markers appear literally. Working notes shown in Astral are not all forwarded.

For group management, notices, group files, essence messages, reactions, and pokes, see [Platform APIs](/docs/chat/platform-api/). These operations use the connected account's permissions.

## Connection and reply problems

| Symptom | Next step |
| --- | --- |
| WebUI opens, but Astral cannot connect | Check both OneBot services are enabled; their ports differ from the WebUI port |
| HTTP times out or refuses the connection | Check listen addresses, ports, Docker mappings, or SSH forwarding in the [network reference](/docs/chat/napcat-network/) |
| `401` / `403` or a token error | Use the same OneBot token in HTTP, WebSocket, and Astral, not the WebUI token; proxies must preserve Authorization |
| WebSocket cannot connect or reports no event account | Use a forward WebSocket **server** with `ws://` or `wss://`, without `/api`; check `array` format and QQ login |
| HTTP and WebSocket identify different accounts | Correct ports or proxy routes so both reach the same NapCat instance and QQ account |
| The account connects, but messages do not start work | Check it is enabled and the chat is added; test NapCat from another QQ account and use a real mention in groups |
| Astral processes a task, but QQ receives no reply | Ask it to send back to QQ; check send errors and muted status, plus platform sending permissions for official bots |
| Text works, but attachments fail | Enable `enableLocalFile2Url` and check file URL access from Astral's host; voice messages may also depend on NapCat transcoding |

NapCat does not replay messages missed during a disconnection. Resend the content you need after restoring the connection.

If a send times out or has an uncertain result, check QQ before asking it to send again.
