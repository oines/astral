---
title: "Configure a Bot"
description: "Manage a Bot’s identity, models, computer access, and chat accounts."
---

A Bot is a persistent identity. It can own several sessions, use authorized computers, and connect multiple chat accounts.

Open it from the **Bot list in Settings**. These settings belong to the selected Bot and do not automatically apply to others.

## Basic settings

Change the name, avatar, default model, and search service here. Use the optional persona for identity, speaking style, or working preferences, then choose Apply persona. It takes effect on subsequent model requests for this Bot.

Leave it empty if no custom persona is needed. Model and search connections are maintained globally; the Bot chooses which ones to use.

## Computer access

Select the computers this Bot may use and its default for new sessions. Global Computer settings manage connections; Bot settings manage permission.

Access allows file operations and commands under the computer service's operating-system account. The default working directory does not restrict access to that folder.

## Platforms and accounts

Choose Connect a platform to add Telegram, QQ, Weixin, or Feishu. A platform can have several accounts, each with separate credentials, enabled state, chat access, and session bindings.

Connection fields take effect after Save connection in their section. Account switches and chat permissions have their own scope.

See [Connect a chat](/docs/chat/overview/) for the full workflow.

## Disable or delete

Disable a platform account to pause incoming messages while keeping its sessions.

Deleting a Bot permanently removes its sessions, connection credentials, and Astral's stored chat data. It does not delete external accounts, chats, messages, or computer work files. Save any records and files you need before deleting.
