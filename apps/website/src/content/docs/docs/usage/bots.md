---
title: "Configure a Bot"
description: "Choose how an assistant works, which model it uses, and which computers it can access."
---

A Bot is an assistant you use over time. It has a name and working preferences, can hold multiple sessions, and can connect to several messaging accounts. First-time setup provides an Astral Bot that you can use for everyday work.

Create another Bot when you need different behavior or access. A personal assistant might use your work computer, while a public-group Bot uses a dedicated container and chat account. For a different topic, you can simply [start another session](/docs/usage/conversations/#separate-unrelated-work).

## Basic settings

In the main sidebar, open the Bot selector and choose **Manage Bot** to go directly to the selected Bot's **Basic settings**. You can also open **Settings → Bots → your Bot → Basic settings**.

Model and search connections live in the [global settings](/docs/usage/models/), where you manage credentials once and choose resources for each Bot.

Deleting a search service turns off web search for every Bot that uses it. The confirmation lists the affected Bots; existing sessions and messages are kept. You can choose another search service later.

Use the persona for preferences that should apply across tasks. For example:

> Help me organize my daily work. Start with practical recommendations. If a source does not specify an owner or date, mark it as “To confirm” rather than filling it in.

Select **Apply persona** to use it in subsequent model requests across this Bot's sessions, including existing subagents that are already working. Requests already frozen retain their original instructions. Clear it and apply again to remove it. Applying a persona does not start idle sessions or subagents. Proactive group-participation decisions also use the current persona. Leave it empty when you have no standing preferences to add.

## Keep work moving when a model is unavailable

Under **Model priority**, put the model you want new sessions to prefer first and add alternatives below it. Each model keeps its own reasoning choice. Use connections that you trust with the same task material: continuing with another model sends the conversation and its necessary context to that model's service too.

Drag a row by its handle to change the order. With the handle focused, use Up or Down to move it one place, or Home or End to move it to the top or bottom. The completed move saves automatically.

Astral tries the models in order. Quota, authorization, and other errors that need a different model move to the next available candidate. Temporary connection failures are retried up to five times per model, including the first attempt, before trying the next one. Subagents use the same recovery rules. Models or connections you disable are skipped while their positions remain in the list.

List changes save automatically and update the alternatives available to existing sessions too. Each existing session keeps its preferred model. Choosing a model in the conversation's input area makes it that session's preferred choice: with a Bot list of A → B → C, choosing B gives B → A → C; choosing a model D outside the list gives D → A → B → C.

To make existing sessions prefer the Bot list's first model, select **Apply to all sessions** and review the confirmation. This affects every session of this Bot, including subagents, and replaces individually chosen preferred models. Requests already sent finish with their original model; later requests use the new preference. Tasks you stopped remain stopped.

**Automatically restore the preferred model** is off by default. When enabled, Astral periodically makes a real request to each affected session's preferred model and switches back after it responds successfully, at the next request boundary. Checks may incur model charges. If all candidates fail, the task waits for recovery instead of ending; checks are spaced one minute apart after each check finishes. With the option off, a task stops with an error once no candidate can continue. Fix the connection, quota, or model choices, then continue the session. Enabling automatic recovery later also requires continuing the failed session; it does not restart old failed tasks on its own.

The group-participation model remains a separate setting. When it follows the session, it uses the model actually serving that session. After choosing a fixed model, you can return to **Follow the Session’s current model** in the same menu; its reasoning settings will follow the session too.

## Computer access

Open the Bot's **Computer access** page, or choose **Manage Computers** from the conversation's computer menu. Allow the computers it needs and optionally choose a default. New sessions start with that default; existing sessions can select another allowed computer.

If the computer is not connected yet, follow [Connect a computer](/docs/computers/connect/), then return here to grant access. Permission allows the Bot to read files, write files, and run commands with the rights of the computer service's operating-system account. Grant only the computers this Bot needs, especially before [using it in a public group](/docs/start/safe-use/#group-chats-use-a-dedicated-bot).

Revoking access to the default computer also clears that default. Choose another computer explicitly when work should continue elsewhere.

## Platforms and accounts

To use the Bot from a messaging app, choose **Connect platform** in its settings, select Telegram, QQ, WeChat, or Feishu, and follow the [platform guide](/docs/chat/overview/).

A Bot can connect several accounts, including more than one on the same platform. Each account has its own credentials, enabled state, allowed chats, and session bindings.

## Disable or delete

To stop receiving messages temporarily, disable the relevant platform account and keep its records. To stop the Bot using a particular computer, revoke that computer's access.

Delete the Bot when you want to remove the assistant completely. This permanently removes its sessions and schedules, platform credentials, and chat data stored by Astral. Save any records you need first.

Accounts, chats, and messages on the external platforms remain, as do working files on computers. Manage those separately where they are stored.
