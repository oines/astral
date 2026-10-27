---
title: "Models and search"
description: "Connect model services, choose Bot defaults, and configure web search."
---

Astral uses model services you supply. Requests originate from the Server, so check endpoints, credentials, and connectivity from the Server machine.

## Add a model service

1. Open **Settings → Models** to see your configured connections, then add a service. Provider presets are choices for creating a connection.
2. Choose the provider or compatible API, then enter its address and credentials.
3. Save the connection and fetch its model catalog. A failed fetch leaves the saved connection available for retry; you can also add an explicit model ID.
4. Select the intended model and configure supported reasoning options.

Reachability does not guarantee access to a model. For failed requests, check the API key, model ID, quota, and returned service error. An endpoint working in your browser does not prove that the Server can reach it.

## Refresh or remove models

Open a provider's model list and select **Refresh model catalog** to synchronize it with the complete list returned by that connection. Newly listed models are added; models no longer listed are removed, including manually added or edited models. A successful empty list removes all current models. A failed or incomplete fetch keeps the previous list.

These rules also apply to Codex and GitHub Copilot, using the model list returned for the signed-in account.

Models you have edited keep their entire saved configuration while they remain in the returned list, including recommendations, display name, protocol, limits and enabled state. Models you have not edited receive updated catalog information. Individual fields can still be restored to their saved recommended values in the model editor; that action does not fetch a new catalog.

When looking up public model details, Astral first matches the part of the model ID after the final `/` exactly. If no exact match exists, it tries again ignoring letter case. Dates, versions and suffixes still have to match, and the model ID sent to your service stays unchanged. Several sources may contribute different fields. Connection data takes priority; public records retain their source priorities, and conflicting values at the same priority remain unknown.

Deleting a model removes it from the current list. Refreshing adds it again if the provider still lists it. Deleting a provider removes its connection and current models. Recreating a connection with the same name creates a separate connection and does not take over earlier selections.

## Choose where it applies

Choose a default under **Settings → Bots → your Bot → Basic settings**. New sessions use this default. Existing sessions can select a model and supported reasoning options near the message input.

Reasoning parameters and tool support differ between services. Set a model’s default reasoning directly in its editor; session choices can inherit that default, use the API default, or choose a supported native option. Capability declarations are separate from default request settings. Model details come from the connection, public models.dev metadata and your explicit configuration; missing data stays unknown.

The editor offers only reasoning options supported by the selected API format. An unavailable switch does not invalidate the API default or another supported option. If the catalog lacks a context or maximum output limit, reasoning options remain visible; fill in the missing limits before saving the model for Bot use.

When changing a model ID or API format, Astral removes catalog information that belongs to the previous model or format. Information fetched for your new selection is retained. Your explicit settings remain in the draft and are checked before saving; choose a supported reasoning default if the previous choice no longer applies.

If a selected connection or model is removed, disabled, or needs configuration, Astral displays its unavailable state and offers the remaining available models. Existing conversation history remains readable, and you can select another model without opening the old connection. A Bot default change applies to new sessions; an existing session keeps its own selection. A request already in progress and its retries retain the configuration captured when that request began. Later model requests use your new selection. Selection is temporarily unavailable while a request is being prepared or context is being compressed. Switching providers preserves the conversation and tool history; signatures and private reasoning that cannot be reused are omitted from that request, while original history remains stored. Forking from a message copies history through that message and its model selection, independently of later changes in the original session.

## Configure web search

Web search is a separate connection; configuring a model does not configure search.

1. Open **Settings → Search**, choose a service, and enter its connection details.
2. Save, then try a test query.
3. Select that search service in the Bot's basic settings.

The test query does not change Bot configuration. Tests use the saved connection. Self-hosted search endpoints must also be reachable from the Server.

## Group participation model

With [proactive group participation](/docs/chat/groups/) enabled, Astral first decides whether an ordinary message calls for a response. This follows the session model by default, or a separately selected Bot model. It decides whether to participate; the main model performs the task.
