---
title: "Models and search"
description: "Connect a model service, choose a Bot's default model, and set up web search."
---

Astral uses the model service you connect. Connections originate from the Server, so a custom endpoint must be reachable from that machine. Task messages, content read by the Bot, and tool results are sent to the selected service; see [where data goes](/docs/start/safe-use/#know-where-data-goes).

## Add a model service

1. Open **Settings → Models** and add a service.
2. Choose a preset or compatible API and enter its endpoint and credentials.
3. Select **Save connection** and fetch its model catalog. If fetching fails, retry or add a model ID manually.
4. Check the model you want to use and fill in any required context-window and maximum-output limits.

## Choose a model for a Bot or session

Choose the default under **Settings → Bots → your Bot → Basic settings**. It applies to new sessions. Existing sessions have their own model and reasoning choices in the input area.

Send a small task to check that the model can respond and use tools. If a call fails, use the error to check the API key, model ID, service quota, and the Server's network connection.

Changing a session's model applies to subsequent requests. A request already in progress finishes first; the picker is temporarily unavailable during request preparation and context compaction. If the selected model is removed, disabled, or incomplete, choose another available model. Existing history remains accessible.

## Set up web search

1. Open **Settings → Web search**, choose a service, and enter its connection details.
2. Save the connection and run a test query.
3. Select that service in the Bot's basic settings.

Search needs its own connection. Test queries use the saved connection and apply only to that test. A self-hosted search endpoint must also be reachable from the Server.

## Adjust reasoning

Set default reasoning in the model editor. A session can inherit that default, use the API default, or choose a supported level or budget. Available controls depend on the model and API format.

After changing a model ID or API format, check whether the reasoning choices still apply. Your entries stay in the draft; complete required limits and resolve validation errors before saving.

Model details come from the connection, the public models.dev catalog, and your manual settings. Missing details remain blank. Calls use the provider's full model ID unchanged.

## Refresh or remove models

Open a provider's model list and select **Refresh model catalog** to synchronize with that connection. Codex and GitHub Copilot use the signed-in account's catalog.

- Newly listed models are added. Models no longer listed are removed from Astral, including models you added or edited manually.
- Models still listed retain your edits. Unedited models receive updated recommendations.
- A successful empty catalog clears the current list. A failed or incomplete fetch keeps the previous list.

Deleting one model may be temporary if the next refresh lists it again. Deleting a provider also removes its models. After adding a connection again, select the models your Bots or sessions should use.

## Group participation model

With [proactive participation](/docs/chat/groups/) enabled, Astral uses a model to decide whether ordinary group messages should wake an idle Bot. It uses the session model by default; you can select a separate model for this decision.

While the Bot is working, the main model reads incoming chat messages directly. The participation model only decides whether to start work; the session model carries out the task.
