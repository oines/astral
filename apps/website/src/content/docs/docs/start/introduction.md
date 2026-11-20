---
title: "What Astral can do"
description: "Give a personal AI assistant access to your computers to work with files, run tools, and report back."
---

Astral is an open-source personal AI assistant that works directly on your computers. Describe what you want in plain language—it can read files, run commands, edit code, create documents, and explain what it did.

Common ways people use it:

- **Summarize notes:** read meeting notes or scattered docs, extract action items, and save a clean report.
- **Work with data:** inspect spreadsheets for gaps or anomalies and generate a summary.
- **Work on real projects:** explore a codebase, make edits, run tests, and use the CLI tools already installed on your machine.
- **Span machines and chats:** move across your laptop, desktop, and servers in one session, or send tasks from Telegram, QQ, WeChat, and Feishu.

## Up and running in 5 minutes

1. **[Download and install Desktop](/docs/start/get-astral/):** the fastest way to start—it bundles the local runtime and needs no extra sign-in setup.
2. **[Connect a model and pick a computer](/docs/start/first-steps/):** add an AI model service you already have access to, then select the computer Astral can use.
3. **Run a small test task:** ask it to inspect a folder or work on a copy of a file so you can see how tool calls and results look.

:::note[Bring your own model]
Astral provides the agent runtime, not model credits. You connect your own model provider—see [Prepare a model account](/docs/start/first-steps/#prepare-a-model-account).
:::

## Three concepts to know

| Concept | What it is | When to care |
| --- | --- | --- |
| **Bot** | An assistant you configure with its own instructions, models, computer access, and chat accounts. | Astral starts with a default **Astral** Bot. [Add more Bots](/docs/usage/bots/) when you want to separate personal work from shared group chats. |
| **Session** | A continuous thread of work with a Bot that can span multiple computers. | Keep follow-ups in the same session; start a new one for unrelated tasks. |
| **Computer** | The machine where files live and commands run—your local machine, another computer, or a Docker container. | Start on your local machine; [connect other computers](/docs/computers/connect/) whenever you need them. |

## Where to go next

- **Work with your files:** see [Files and results](/docs/usage/files/) to upload material or point Astral at local paths.
- **Automate recurring work:** see [Schedules and events](/docs/usage/schedules/) for timed runs and local event wake-ups.
- **Delegate from chat apps:** see [Connect a chat](/docs/chat/overview/) for Telegram, QQ, WeChat, or Feishu.
- **Keep Bots online 24/7:** see [Desktop or Server?](/docs/hosting/architecture/) to deploy on a Linux server.

:::caution[Before touching real files]
Authorizing a computer lets the Bot read files and run commands with that OS account's permissions. Try your first tasks on copies, and read [Permissions and privacy](/docs/start/safe-use/) before handling sensitive files or shared groups.
:::
