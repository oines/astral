---
title: "Models and search"
description: "Connect a model account, choose a usable model, and add web search when you need it."
---

Connect a model service and select a model before asking a Bot to work. The service provides the model; Astral sends requests using the account or API key you connect.

The Model step in first-time setup uses the same interface as **Settings → Models**. After connecting a service, select an available model before continuing to the computer step.

## Prepare your service account

Start with the service you intend to use. Sign in or create an account on its official website, then check that the account has access to your chosen model and enough quota. Check the provider's terms for the access required here; a subscription to its chat product may have different entitlements.

The connection form tells you what to prepare:

| The form asks for | What you need |
| --- | --- |
| **API key** | Create a key in that provider's developer platform or console, then copy it into Astral. Use the form's **Get API key** link when available. This is an API credential, not your account password. |
| **Continue to sign in / Connect account** | Open the provider's sign-in page and complete authorization with the displayed code. This connection uses that account's permissions and model catalog. |
| A custom service address | Obtain the Base URL, protocol, and any key from the service administrator or its documentation. Use this for your own service or a compatible API. |

Enter keys in the connection form rather than in a conversation. Task messages, material the Bot reads, and tool results go to the selected model service. See [where data goes](/docs/start/safe-use/#where-does-my-data-go).

## Add a model service

Use **Add** under **Settings → Models**. Choose the preset matching your account. You can usually keep its prefilled Base URL (API address) and protocol. If it offers regions, match the region used by your account and key.

Key-based connections have **Import models after saving** selected by default. Check the model list after saving. Account-based connections load the catalog after authorization. Start with one enabled model that has no missing-configuration warning.

If there is no matching preset, select **Custom provider**. Enter the address, protocol, and key supplied by the service. Configure advanced fields such as extra headers or reasoning mappings only when its documentation requires them.

Requests originate from Astral's background service, the Server. With Desktop, this is normally the computer running the app. With a standalone Server, it is the deployment host. That machine must be able to reach a custom API address.

### The model you need is missing

Try **Refresh model catalog** and check any error. If the service has no catalog, or you need a manual entry, select Add model and use the **full model ID** supplied by the service. This is the API identifier, which may differ from a model's display name.

Astral fills in known parameters from the connection and public model information. Keep the supplied recommendations when appropriate. If required limits are missing, use the provider's documentation for this exact model:

- **Context window** is the amount of material a request can accommodate, including conversation, source material, and output. It is measured in tokens.
- **Maximum output tokens** limits the size of a single response.

Tokens are units used by models to measure content, rather than a direct word count. Do not use a placeholder number or another service's similarly named model as a substitute for the actual limits. Complete and save the configuration to make the model selectable.

## Choose a model for a Bot or session

During first-time setup, select the configured model and proceed to the computer step. Later, arrange the preferred model and alternatives under **Settings → Bots → your Bot → Basic settings → Model priority**. Each entry has its own reasoning choice. New sessions prefer the first entry; existing sessions keep their preferred model and use the current list for alternatives. **Apply to all sessions** sets this Bot's existing sessions and subagents to prefer the first entry too. See [model recovery](/docs/usage/bots/#keep-work-moving-when-a-model-is-unavailable) for automatic switching and restoring the preferred model. You can still choose a preferred model separately in a session's input area.

Use a row's drag handle to change its priority. The focused handle also accepts Up, Down, Home, and End; each completed move saves automatically. Connection names help distinguish models with the same name.

With an online computer selected, try a small task whose result you can check:

> Read the current working directory and list its filenames without changing anything. Tell me which computer and directory you used.

Check that the Bot actually performs the operation and returns the result. A selectable model has the required configuration; a real task checks the credentials, quota, and ability to use tools. Model requests may incur service charges.

Changing the model or reasoning settings in a conversation's input area also updates all its existing subagents, including deeper levels and subagents previously assigned another model. A request already sent completes with its original settings; later requests use the new choice. Other conversations and ordinary Fork copies keep their settings. See [background work](/docs/usage/conversations/#delegate-background-work) for following subagent activity and model selection for newly created subagents. The picker can be temporarily unavailable while preparing a request or summarizing a long conversation. If a model is disabled or removed, choose another available model; the history remains accessible.

## Set up web search

Add a search connection when you want the Bot to search the web. Search is configured separately from the model service; the model API key is not automatically a search credential.

Add a service under **Settings → Web search**, using credentials from that search provider or your own self-hosted address. After saving, try a simple query such as “Astral GitHub” and inspect the returned titles and links. Then select this connection in the Bot's **Basic settings**.

Tests use the saved connection. The test query does not become a standing task for the Bot. Each Bot uses one selected search service; a missing or failed connection needs attention rather than automatically falling back to another service. Self-hosted search must also be reachable from the Server machine.

## Troubleshoot the connection

| What you see | What to check |
| --- | --- |
| A saved service, but setup cannot continue | Enable at least one model, complete its required limits, and select it in the setup step. |
| An invalid-key or permission error | Check that the key belongs to this service and region, and that its account can use this model. For account authorization, try reconnecting the account. |
| A quota or rate-limit error | Review the provider's quota and request limits. Add another trusted model to the Bot's priority list and apply it to existing sessions when needed; see [model recovery](/docs/usage/bots/#keep-work-moving-when-a-model-is-unavailable). |
| A timeout or unreachable address | Check the Server machine's network and the Base URL. Reaching the provider's website in your browser does not test that connection. |
| Text replies work, but tool use fails | Check that this model supports tools through the selected API protocol. Choose a suitable model if necessary. |
| A search test works, but the Bot cannot search | Check that this Bot's basic settings select the connection you just tested. |

## Adjust reasoning

Start with the model defaults and adjust them after a basic task works. Models that support reasoning controls can have a default mode in the model editor; sessions can also select a supported level or budget.

**Inherit model default** uses your saved model preference. **API default** lets the service use its own default behavior. Other choices depend on the model's supported controls. After changing the model ID or API protocol, review the reasoning options and resolve any validation errors before saving.

For a subagent that needs a different model, ask in the original conversation and name the subtask. The Bot can select another available model and its supported reasoning settings for a new or existing subagent. If a subagent's run fails, its parent is notified and can decide to change models or retry; changing the model alone does not start another run. Choosing a model without a separate reasoning choice uses that model's saved default; API default remains a separate explicit choice. Changing one subagent leaves its descendants unchanged, while changing the original conversation's picker updates every existing subagent. See [delegate background work](/docs/usage/conversations/#delegate-background-work).

## Refresh or remove models

**Refresh model catalog** synchronizes Astral's list with the current list returned by that connection. It adds newly listed models and removes absent ones, including manual entries. Models still listed retain your edits; unedited parameters receive updated recommendations.

A failed or incomplete fetch preserves the old list. A successfully returned empty catalog clears it. Codex and GitHub Copilot catalogs come from the signed-in account.

To stop using a model temporarily, turn it off. Turning off a connection makes all its models unavailable without changing each model's own enabled setting. Priority lists keep these entries and skip them. Turning the same resource back on makes it eligible again; a request already in progress keeps its original settings.

Deleting one model does not prevent a later refresh from adding it again, but the new entry has a new identity. Deleting a provider removes its models too. Priority lists retain missing entries until you remove or replace them; another usable entry can continue the work. Recreating a model or connection with the same name does not repair old choices automatically. Replace affected entries explicitly, and use **Apply to all sessions** when existing sessions should prefer the list's first entry. History is preserved.

## Group participation model

With [proactive group participation](/docs/chat/groups/) enabled, Astral decides whether an ordinary group message should wake an idle Bot. By default it uses the model actually serving the session, including after an automatic switch; you can select a separate model for this decision in the Bot's settings.

This model only decides whether to start work. The session model performs the task and handles new chat content while the Bot is working.
