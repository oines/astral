---
title: "Configure Bots"
description: "Set up a Bot's working preferences, model priority and fallback, computer access, and chat accounts."
---

A **Bot** is an assistant with its own persona, model priority list, computer access, and chat accounts. Astral includes a default **Astral** Bot ready for everyday use.

- **New topic, same assistant** → [start a new session](/docs/usage/conversations/#separate-unrelated-work).
- **Different behavior or trust level** (e.g., personal laptop vs. public group container) → create another Bot.

Open the Bot switcher in the sidebar and choose **Manage Bot**, or go to **Settings → Bots → your Bot**.

## Basic settings and persona

Model providers and web search live in [global settings](/docs/usage/models/); here you simply pick which ones this Bot uses.

Use **Persona** for standing instructions across all of this Bot's tasks (leave blank if not needed):

> Help me organize daily work. Lead with actionable suggestions. Mark unspecified owners or due dates as “To confirm” instead of guessing.

Click **Apply persona** so subsequent model requests (including active subagents and proactive group decisions) use it. Clear and apply again to remove it.

## Keep work moving when a model is unavailable

Under **Basic settings → Model priority**, put your preferred model first and add backups beneath it (each row keeps its own reasoning settings; drag the handle to reorder and auto-save).

:::note[Only add providers you trust with the task]
When Astral falls back to another model, the conversation and required context are sent to that provider too.
:::

- **How fallback works:** Quota or auth errors move immediately to the next candidate; temporary network/service errors retry first, then fail over. Subagents follow the same rules.
- **Per-session overrides:** Picking a model in a session's input bar makes it that session's preferred model while keeping the Bot's list as backups (e.g., Bot list `A → B → C` + manual choice `B` tries `B → A → C`; picking `D` tries `D → A → B → C`).
- **Update all active sessions:** Click **Apply to unarchived sessions** to reset every unarchived session (and its subagents) to prefer the first model in the list.
- **Automatically restore the preferred model** (off by default): Periodically probes the session's preferred model with a real request (which may incur charges) and switches back once healthy; if all candidates fail, waits and retries every minute instead of halting immediately.

<details class="astral-doc-details">
<summary>Retry counts, keyboard reordering, archived sessions, and group-participation model details</summary>

- **Retry limit:** Transient connection errors retry up to 5 times per model (including the first attempt) before moving on. Disabled models or providers remain in the list and are skipped.
- **Archived sessions & in-flight requests:** **Apply to unarchived sessions** leaves archived sessions untouched (even after restoration). Requests already sent finish on their original model, and manually stopped tasks stay stopped. Turning on automatic recovery later does not restart past failed tasks automatically—continue the session manually.
- **Group participation model:** Defaults to **Follow the current conversation model** (tracking automatic failovers and reasoning settings), or you can pin a fixed model and switch back anytime.
- **Keyboard sorting & deleting search:** Focus a drag handle and use Up/Down or `Home`/`End` to reorder. Deleting a global web search connection disables search for every Bot using it while preserving sessions.

</details>

## Choose a model for live progress summaries

In **Basic settings → Activity summary model**, the default **Follow the current conversation model** uses whichever model is doing the work, including a backup after a switch. It uses the lowest available reasoning level for the summary. You can switch to **Off** to skip the extra requests or choose a specific model for this Bot.

When you watch a session live, Astral may send a short version of the task and recent work to the summary model's provider. Choose a provider you trust with that information. The activity line and its details are available in all three modes.

## Computer access

Open **Computer access** (or pick **Manage Computers** from the session's computer menu) to allow the computers this Bot can use and set an optional default for new sessions.

- Need to add a machine first? Follow [Connect computers](/docs/computers/connect/).
- Revoking access to the default computer clears the default. For public groups, double-check that only a dedicated computer or container is allowed—see [Group chats: use a dedicated Bot](/docs/start/safe-use/#group-chats-use-a-dedicated-bot).

## Platforms and accounts

Choose **Connect platform** in Bot settings and follow [Connect a chat](/docs/chat/overview/) for Telegram, QQ, WeChat, or Feishu. One Bot can hold multiple accounts (even on the same platform), each with isolated credentials, enabled state, and allowed chats.

## Connect an app over ACP

In **Basic settings → ACP access**, copy the WebSocket address and create a token named for the app or device that will use it. Copy the token before closing the dialog. Give a different token to each app so you can revoke its access separately. Revoking a token immediately disconnects any clients using it.

The token grants access to this Bot's ordinary, unarchived, non-private sessions, including their history and the ability to start or stop work. Confirm the Bot's computer access before sharing a token. [Check client compatibility and connect](/docs/hosting/acp/).

## Disable or delete

- **Pause incoming chats:** disable that platform account to keep history intact.
- **Block a computer:** uncheck it under **Computer access**.

:::caution[Deleting a Bot is permanent]
Deleting a Bot permanently removes all its sessions, schedules, platform credentials, and chat data stored by Astral, and disconnects clients using its ACP tokens. Save anything important first. External chat messages and files on your computers remain untouched.
:::
