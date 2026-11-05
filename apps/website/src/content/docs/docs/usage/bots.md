---
title: "Configure a Bot"
description: "Choose how an assistant works, which model it uses, and which computers it can access."
---

A Bot is an assistant you use over time. It has a name and working preferences, can hold multiple sessions, and can connect to several messaging accounts. First-time setup provides an Astral Bot that you can use for everyday work.

Create another Bot when you need different behavior or access. A personal assistant might use your work computer, while a public-group Bot uses a dedicated container and chat account. For a different topic, you can simply [start another session](/docs/usage/conversations/#separate-unrelated-work).

## Basic settings

Open **Settings → Bots → your Bot → Basic settings**. The default model applies to new sessions; existing sessions keep their own selections. Model and search connections live in the [global settings](/docs/usage/models/), where you manage credentials once and choose a connection for each Bot.

Deleting a search service turns off web search for every Bot that uses it. The confirmation lists the affected Bots; existing sessions and messages are kept. You can choose another search service later.

Use the persona for preferences that should apply across tasks. For example:

> Help me organize my daily work. Start with practical recommendations. If a source does not specify an owner or date, mark it as “To confirm” rather than filling it in.

Select **Apply persona** to use it in subsequent model requests across this Bot's sessions, including existing subagents that are already working. Requests already frozen retain their original instructions. Clear it and apply again to remove it. Applying a persona does not start idle sessions or subagents. Proactive group-participation decisions also use the current persona. Leave it empty when you have no standing preferences to add.

## Computer access

Open the Bot's **Computer access** page, allow the computers it needs, and optionally choose a default. New sessions start with that default; existing sessions can select another allowed computer.

If the computer is not connected yet, follow [Connect a computer](/docs/computers/connect/), then return here to grant access. Permission allows the Bot to read files, write files, and run commands with the rights of the computer service's operating-system account. Grant only the computers this Bot needs, especially before [using it in a public group](/docs/start/safe-use/#group-chats-use-a-dedicated-bot).

Revoking access to the default computer also clears that default. Choose another computer explicitly when work should continue elsewhere.

## Platforms and accounts

To use the Bot from a messaging app, choose **Connect platform** in its settings, select Telegram, QQ, WeChat, or Feishu, and follow the [platform guide](/docs/chat/overview/).

A Bot can connect several accounts, including more than one on the same platform. Each account has its own credentials, enabled state, allowed chats, and session bindings.

## Disable or delete

To stop receiving messages temporarily, disable the relevant platform account and keep its records. To stop the Bot using a particular computer, revoke that computer's access.

Delete the Bot when you want to remove the assistant completely. This permanently removes its sessions and schedules, platform credentials, and chat data stored by Astral. Save any records you need first.

Accounts, chats, and messages on the external platforms remain, as do working files on computers. Manage those separately where they are stored.
