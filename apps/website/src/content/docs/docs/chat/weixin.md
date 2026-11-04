---
title: "Connect Weixin"
description: "Understand Weixin authorization, private-chat setup, and expired logins."
---

Weixin currently supports **private chats**, not groups. Astral uses ClawBot / iLink QR authorization without a separate OpenClaw installation. Whether an account receives authorization is determined by Weixin.

First [configure Astral's model and computer](/docs/start/first-steps/), then connect at **Settings → Bots → your Bot → Connect a platform → Weixin**. Follow the prompts in Weixin and Astral to confirm authorization or enter a verification code when required.

## Receive a message before adding the chat

Have the person who will use the Bot send it a Weixin message, such as “Hello.” Astral needs that incoming message to obtain the contact context required to reply.

Then select and add the private chat under the account's **Connected chats** in Astral. Use a new conversation for a first test; an existing conversation continues its earlier work context. Entering a Weixin username or arbitrary identifier does not make someone reachable if they have never messaged this Bot.

After adding the chat, **send a new message** in Weixin:

> Please reply in this Weixin chat with “Connection successful” and tell me which computer you are using.

Check processing in Astral and the actual reply in Weixin. The earlier discovery message does not run as a task after the chat is added.

## Text and attachments

You can send text, images, files, video, and voice content supported by the platform. Say “send it back to this Weixin chat” when you want a result delivered; working notes in Astral are not all forwarded.

Text and files may be sent separately. Test with a small file once the first reply works. File formats and sizes depend on Weixin. Outgoing quoted replies, editing, and read receipts are not currently supported.

## Expired login or missing messages

If login expires or error `-14` appears, choose **Authorize again** on the existing account. Authorize the same Bot identity to retain the account relationship; create a new account connection for a different identity.

If connected but silent, check that the account is enabled and the private chat is added, then ask the person to send a new message. Existing chats continue in their original conversations after restarting Astral; you do not need to remove and add them again. If Astral processed the task but no result arrived, inspect send errors in its conversation. Check Weixin before repeating a send with an uncertain outcome.
