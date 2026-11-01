---
title: "Before your first task"
description: "Prepare a model account, choose where tasks run, and try a simple task you can check."
---

On first launch, Astral guides you through two choices: a model and a computer. Follow that setup in the app. This page covers what to prepare beforehand and how to check that everything works.

## Prepare a model account

Astral uses the model service you connect to understand tasks. Start with a service you already have access to, and check how it connects:

- **API key:** create a key in the service's developer portal or account console, ready to paste into Astral. Some presets include a **Get API key** link. The provider manages model access, usage allowance, and charges.
- **Account sign-in:** if Astral offers sign-in for that service, follow its authorization prompt.
- **A model service you host:** provide its API address and any required key. That address must be reachable from the machine running Astral.

An API key is a secret for applications to use; a chat website password cannot replace it. Whether an existing chat subscription works depends on the service's connection method and account permissions.

After connecting a service, select an available model. If the list is empty or a model cannot be selected, use [Models and web search](/docs/usage/models/) to check authorization, allowance, or missing model information.

## Check where tasks will run

In the desktop app, you can start with your own computer. When accessing Server through a browser, **Astral host means the server**. To work on files from your laptop, upload them or [connect that laptop](/docs/computers/connect/).

Completing setup grants the Bot access to files and commands on the selected computer. Use test material for your first attempts. Before connecting a shared group, arrange a [dedicated Bot and separate environment](/docs/start/safe-use/#use-a-separate-environment-for-public-groups).

## Try a task you can verify

Once the chat opens, send:

> Tell me which computer you are using and the current working directory, then list the files in that directory. Do not change any files yet.

Check the computer named in the execution record and the directory and file list returned. An empty directory is a valid result. This checks that the model can understand your request and that the computer can execute it.

If the assistant only answers in text, ask it to actually check using its computer tools. If it reports an error, distinguish a model connection problem from an offline computer, then follow [Troubleshooting](/docs/hosting/troubleshooting/).

Next, try [working with a file](/docs/usage/files/). Connecting [QQ, Feishu, or another chat platform](/docs/chat/overview/) can wait until that task succeeds.
