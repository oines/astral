---
title: "Models and search"
description: "Connect model services, choose Bot defaults, and configure web search."
---

Astral uses model services you supply. Requests originate from the Server, so check endpoints, credentials, and connectivity from the Server machine.

## Add a model service

1. Open **Settings → Models** and add a service.
2. Choose the provider or compatible API, then enter its address and credentials.
3. Save the connection, then fetch or add models.
4. Select the intended model and configure supported reasoning options.

Reachability does not guarantee access to a model. For failed requests, check the API key, model ID, quota, and returned service error. An endpoint working in your browser does not prove that the Server can reach it.

## Choose where it applies

Choose a default under **Settings → Bots → your Bot → Basic settings**. New sessions use this default. Existing sessions can select a model and supported reasoning options near the message input.

Reasoning parameters and tool support differ between services. Use the options available for the chosen model and check actual responses.

## Configure web search

Web search is a separate connection; configuring a model does not configure search.

1. Open **Settings → Search**, choose a service, and enter its connection details.
2. Save, then try a test query.
3. Select that search service in the Bot's basic settings.

The test query does not change Bot configuration. Tests use the saved connection. Self-hosted search endpoints must also be reachable from the Server.

## Group participation model

With [proactive group participation](/docs/chat/groups/) enabled, Astral first decides whether an ordinary message calls for a response. This follows the session model by default, or a separately selected Bot model. It decides whether to participate; the main model performs the task.
