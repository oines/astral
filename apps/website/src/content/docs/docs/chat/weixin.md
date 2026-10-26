---
title: "Connect Weixin"
description: "Authorize ClawBot / iLink by QR code and connect private chats."
---

Weixin uses ClawBot / iLink QR authorization. It currently supports **private chats**, not groups. Availability depends on authorization by the Weixin service. You do not need to install OpenClaw on the Astral Server.

## Authorize the account

1. Open **Settings → Bots → your Bot → Connect a platform → Weixin**.
2. Name the account and request a QR code.
3. Scan it in Weixin and confirm on your phone.
4. If a verification code is requested, enter the **digits displayed in Weixin** and continue waiting.

Request a new QR code if it expires. Astral stores the confirmed authorization; no manual Bot Token entry is needed.

## Connect a private chat

Ask the intended user to message the Bot first. Astral needs that incoming chat context to resolve the user and send replies.

Select the discovered chat, or enter its actual received identifier, then [authorize and bind it](/docs/chat/overview/). Inventing or manually entering an arbitrary user identifier does not make that user reachable.

## Message support

Text, images, files, video, and voice attachments or transcripts supplied by the platform are supported. Outgoing quoted replies, groups, message editing, and read receipts are not currently supported.

Text and files may be sent separately. A successful text reply does not prove that an attachment was sent. The service's own file restrictions apply.

## Reauthorize

Use Reauthorize by QR code on the existing account when login expires. Error `-14` indicates an expired login session and normally requires scanning again.

Reauthorize the same identity to retain the account relationship. Create a new connection for a different Bot identity. If messages are missing, also check the enabled state, chat authorization, and whether the user has sent an initial message.
