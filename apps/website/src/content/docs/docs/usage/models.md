---
title: "Models and web search"
description: "Connect a model provider, pick or add models, adjust reasoning, and optionally enable web search."
---

A Bot needs a connected model provider before it can run tasks. The Model step during first-time setup uses the exact same screen as **Settings → Models**.

## 1. Add a model provider

Go to **Settings → Models → Add** and choose a preset or **Custom provider**:

| Connection type | What to enter |
| --- | --- |
| **API key** | Create an API key in the provider's developer console and paste it in (match the region if prompted). **Import models after saving** is checked by default. |
| **Continue to sign in / Connect account** | Complete browser authorization to load the account's model catalog automatically. |
| **Custom provider** | Enter the Base URL, protocol, and any required key. Configure extra headers or reasoning mappings only if the service docs require them. |

:::note[Where requests originate]
Model requests are sent by **Astral Server** (your computer on Desktop, or the server host on standalone deployments). Custom or self-hosted endpoints must be reachable from that machine.
:::

### Missing the model you want?

Try **Refresh model catalog** first. If the provider doesn't expose a catalog—or you need a manual entry—click **Add model** and enter the exact **full model ID** used by the API (not just its marketing name).

If limits are missing, fill them from the provider's docs for that exact model:
- **Context window:** total tokens a request can hold (conversation, files, and output).
- **Maximum output tokens:** maximum tokens in a single response.

## 2. Choose models for a Bot or session

- **First-time setup:** select the model you just configured and move on to choosing a computer.
- **Everyday use:** order preferred and fallback models in **Settings → Bots → your Bot → Basic settings → Model priority**, or pick a model directly in a session's input area (see [model recovery](/docs/usage/bots/#keep-work-moving-when-a-model-is-unavailable)).

Test credentials, quota, and tool calling together with a quick read-only prompt:

> Read the current working directory and list its filenames without changing anything. Tell me which computer and directory you used.

:::tip[Image support and reasoning levels]
- Models marked as not supporting images replace images with text markers so text work continues; original images stay in history and work again if you switch to a vision-capable model.
- Start with **Inherit model default** for reasoning, then tune per model or per session once basic tasks work (**API default** defers to the provider's own default).
:::

## 3. Add web search (optional)

Web search is configured separately from model providers (your model API key does not automatically power search).

1. Go to **Settings → Web search** and add a provider key or self-hosted address (must be reachable from the Server machine).
2. Run a quick test query (e.g., `Astral GitHub`) to confirm titles and links return.
3. Select that connection in the target Bot's **Basic settings** to activate it.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| Provider saved, but setup won't advance | Enable at least one model, complete required limits, and select it in the setup step. |
| Invalid key or permission denied | Verify the key matches the provider and region and that your account has access; re-authorize for account sign-in connections. |
| Quota or rate-limit errors | Check provider limits, or add backup providers to the Bot's priority list. |
| Timeout or unreachable address | Check network access and Base URL from the **Server machine** (working in your browser does not prove Server reachability). |
| Text works, tool calls fail | Verify the model and selected API protocol support tool use. |
| Search test passes, but Bot can't search | Make sure the search connection is selected in that Bot's **Basic settings**. |

<details class="astral-doc-details">
<summary>Subagent model overrides, catalog refreshes, disabling vs. deleting, and group models</summary>

- **How session model switches affect subagents:** Picking a model or reasoning level in the main session input bar updates all existing subagents (including deeper levels and individually assigned ones). In-flight requests finish with their previous settings; other sessions and ordinary forks stay independent. You can also ask the Bot to change the model for one named subagent without affecting its descendants or starting a new turn.
- **Refreshing the catalog:** Syncs Astral's list with the provider—adding new entries and removing absent ones (including manual entries) while keeping edits on retained models. Failed fetches keep the old list; a valid empty catalog clears it. Codex and GitHub Copilot catalogs come from the signed-in account.
- **Disable vs. delete:** Disable a model or connection to pause it without breaking priority lists (entries are skipped until re-enabled). Deleting and recreating a model with the same name creates a new identity—replace missing entries explicitly in priority lists. Session history is always preserved.
- **Group participation model:** When [proactive group participation](/docs/chat/groups/) is on, deciding whether to wake an idle Bot uses the session's active model by default, or a separate model chosen in Bot settings (that model only decides whether to start, not how to execute the task).

</details>
