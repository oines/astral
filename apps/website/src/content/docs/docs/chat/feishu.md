---
title: "Connect Feishu"
description: "Create a Feishu app, grant message permissions, and connect a private chat or group to Astral."
---

Create an **enterprise custom app** in Feishu, enable its bot capability, and let Astral use that bot to receive and send messages. A custom webhook bot added through group settings can only push notifications and cannot provide this two-way connection.

You need a Feishu account that can manage custom apps, plus an Astral Bot with its [model and computer configured](/docs/start/first-steps/). Your organization may require administrator approval for permissions and releases. Start by making the app available to yourself and your test users.

Follow this order: **create the app → grant permissions and publish → connect Astral → configure events and publish again → add a chat and test**. The first release activates the bot. Connecting Astral then lets Feishu save the long-connection event configuration.

## 1. Create the app and find its credentials

1. Open the [Feishu Developer Console](https://open.feishu.cn/app), create an enterprise custom app, and give it the name you want people to see in Feishu.
2. In the app, add **Bot** under **Add app capability**.
3. Open **Credentials & Basic Info** and locate **App ID** and **App Secret**. Keep this page open; you will copy both values into Astral.

App Secret is a connection credential. Do not share it in a group. Astral's **Configure Feishu app** link also opens the console; after you enter a valid App ID, it opens that app's permissions page directly.

## 2. Grant permissions and publish the first version

In the app's **Development Configuration → Permissions → Batch import/export permissions**, choose import and paste the complete JSON below. Confirm the new permissions, then apply to enable them. These are application permissions; leave `user` empty.

```json
{
  "scopes": {
    "tenant": [
      "im:chat:readonly",
      "im:message:readonly",
      "im:message.p2p_msg:readonly",
      "im:message.group_msg",
      "im:message:send_as_bot",
      "im:resource",
      "im:message.reactions:write_only"
    ],
    "user": []
  }
}
```

Each permission serves a specific part of the connection:

| Feature | Permission |
| --- | --- |
| Receive private messages sent to the bot | `im:message.p2p_msg:readonly` |
| Receive group messages, including mentions; read group messages and replies | `im:message.group_msg` |
| Read messages, download received attachments, and read reactions | `im:message:readonly` |
| Send messages and recall the Bot's own messages | `im:message:send_as_bot` |
| Upload images and files | `im:resource` |
| Read chat information, such as group names | `im:chat:readonly` |
| Add and remove the Bot's own reactions | `im:message.reactions:write_only` |

`im:message.group_msg` is a sensitive permission subject to your organization's approval. It also supports reading group messages and replies; it is not just a switch for proactive participation. Upload permission through `im:resource` alone does not provide the permissions needed to read incoming attachments.

Open **Version Management & Release**, create a version, add yourself and test users to its **availability range**, and submit it for release. Complete any required approval and make sure the app is enabled. Permissions marked “Pending release” are not yet active in the published version. Keep existing permissions; batch import skips duplicates.

## 3. Connect the app in Astral

At **Settings → Bots → your Bot → Connect a platform → Feishu**, use App ID and App Secret from Feishu's **Credentials & Basic Info**, normally keeping the default server address. After connecting, leave Astral running while you configure events in Feishu.

If the bot is inactive, finish the release, enablement, and approval from the previous step before connecting again.

## 4. Configure events and publish again

In the Feishu app, open **Events & Callbacks → Event Configuration**:

1. Select **Receive events through a long connection** and save. Astral must already be connected and running. Astral opens the connection to Feishu, so you do not need a public webhook URL.
2. Add these three events:

   | Event identifier | Purpose |
   | --- | --- |
   | `im.message.receive_v1` | Receive chat messages |
   | `im.message.reaction.created_v1` | Receive added reactions |
   | `im.message.reaction.deleted_v1` | Receive removed reactions |

3. Return to **Version Management & Release** and publish another version to activate the event configuration and any pending permissions.

If saving the subscription fails, check that the Astral account is connected and its host can reach Feishu. Feishu requires an online client before saving this subscription method; see its [long-connection configuration guide](https://open.feishu.cn/document/ukTMukTMukTM/uYDNxYjL2QTM24iN0EjN/event-subscription-configure-/request-url-configuration-case).

## 5. Add a chat and verify a real reply

Have a user within the app's availability range start a private chat, or add the bot to a group. Send a message such as “Hello,” mentioning the Bot in a group. Astral can then discover the chat; add it under the account's **Connected chats** and choose a conversation.

After adding it, return to Feishu and **send a new message**: “Please reply in this Feishu chat: connection successful.” Mention the Bot in a group or reply to one of its messages. Check Astral's processing and the actual reply in Feishu. Earlier discovery messages do not run as tasks after adding the chat.

For manual lookup, use the `oc_…` Chat ID Astral actually received. Feishu threads currently share authorization and an Astral conversation with their parent chat.

## Images, files, and group discussions

Test reading and sending an image or small file after the first reply works. Images can accompany text and native mentions; ordinary files and their explanatory text are separate messages, so check that the attachment also arrives.

The permissions above include ordinary group messages. Astral starts tasks on mentions and replies by default; enable proactive participation per group if wanted. See [Group collaboration](/docs/chat/groups/), or [Platform APIs](/docs/chat/platform-api/) for custom message and reaction queries.

## Check the step that is failing

| Symptom | What to check |
| --- | --- |
| Astral says the bot is inactive | Complete the first release, enablement, and administrator approval |
| Long-connection subscription cannot be saved | Connect the Astral account and leave it running before saving event settings |
| Users cannot find the app or start a private chat | Add them to the app's availability range and publish; check whether an administrator disabled access |
| The account connects, but no chat is discovered | Check `im.message.receive_v1`, message permissions, and the published version; the bot must also be in the group |
| Only a Chat ID appears, without a group name | Check that `im:chat:readonly` is active |
| Text arrives, but attachments or quoted messages cannot be read | Check `im:message:readonly` and, for groups, `im:message.group_msg`; publish permission changes |
| An added chat still gets no reply | Send a new task in Feishu and mention the Bot in groups; inspect model, computer, and platform send errors in Astral |

If a send times out or has an uncertain result, check the actual Feishu chat before sending again.
