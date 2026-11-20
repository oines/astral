---
title: "Your first task"
description: "Prepare a model account, confirm which computer runs tasks, and verify everything with a safe read-only check."
---

On first launch, Astral walks you through two steps: **connecting a model** and **choosing a computer**. Follow the in-app guide—this page tells you what to prepare beforehand and how to verify everything works end to end.

## Prepare a model account

Pick a model provider you already have access to and prepare the credentials it uses:

| Connection type | What to prepare |
| --- | --- |
| **API key** | Create a key in the provider's developer console (an application key, not your chat website password). Some presets include a **Get API key** link right in the form. |
| **Account sign-in** | For providers with built-in sign-in, follow the browser authorization prompt. |
| **Self-hosted / custom endpoint** | Have the Base URL and any required key ready. The machine running Astral must be able to reach that address. |

:::tip[Remember to select a model]
After saving a provider, pick at least one enabled model so setup can continue. If the list is empty or a model shows missing limits, see [Models and web search](/docs/usage/models/).
:::

## Check which computer will run tasks

- **Desktop app:** select the computer you are using right now.
- **Server via browser:** **Astral host** refers to the server, not your laptop. To work on files on your laptop, upload them or [connect your laptop](/docs/computers/connect/) later.

:::caution[First-time access and shared chats]
Finishing setup authorizes the default Bot to read files and run commands on the selected computer. Start with test copies—and before connecting a public group chat, set up a [dedicated Bot and separate environment](/docs/start/safe-use/#use-a-separate-environment-for-public-groups).
:::

## Verify with a read-only task

Once the chat opens, send:

> Tell me which computer you are using and the current working directory, then list the files in that directory. Do not change any files yet.

Check two things:
1. Expand the tool activity and verify the computer name and directory match what you expect (an empty folder is fine).
2. Make sure it **actually ran a computer tool** rather than answering only in text. If it didn't, ask it to check with its tools.

Once this succeeds, both your model connection and computer execution are ready. If an error appears, note whether it points to the model or the computer and check [Troubleshooting](/docs/hosting/troubleshooting/).

## Next steps

- Try [Files and results](/docs/usage/files/) with a sample document to test uploading, previews, and downloads.
- Once your first task works inside Astral, [connect Telegram, QQ, WeChat, or Feishu](/docs/chat/overview/).
