---
title: "Connect with the Bot API"
description: "Use HTTP to run a Bot, upload attachments, read conversation history, and manage its automations."
---

Use the Bot API from scripts and apps that need HTTP access to one Bot. Use [ACP](/docs/hosting/acp/) when an editor or other ACP client needs to join a live Astral session.

## Create an access token

1. In **Settings → Bots → your Bot → Basic settings → External access**, copy the **Bot API address**.
2. Select **Create token**, choose **Bot API**, and name the token for the app or script. Copy it before closing the dialog. Astral shows the token once.
3. Send it in the `Authorization: Bearer <token>` header. The API uses the same host and port as Astral under `/api/v1/bot`. Use the HTTPS address when connecting over a network, and forward the authorization header through your reverse proxy.

Use a separate token per integration so you can revoke it individually. Bot API and ACP tokens are used with their respective addresses. Revoking a Bot API token blocks access with that token and ends any event streams using it.

Set `BOT_API` to the copied address and supply the token to your app as the secret `ASTRAL_BOT_TOKEN`. The following request verifies which Bot you are connected to. Its response includes the Bot name and ID.

```sh
curl --fail-with-body "$BOT_API/self" \
  -H "Authorization: Bearer $ASTRAL_BOT_TOKEN"
```

If you get `401`, check that the token belongs to the Bot API and still appears under **External access**. If a session cannot be found, verify it belongs to the Bot identified by `GET /self` and is not private.

## Start or follow a conversation

- Use `GET /sessions` to list the Bot’s ordinary sessions. To create one, send `POST /sessions` with JSON such as `{"title":"Review the deployment checklist"}`. The token selects the Bot. Use the returned session `id` in later requests.
- To send instructions, call `POST /sessions/{id}/inputs` with JSON such as `{"id":"deployment-check-1","text":"Review the deployment checklist."}`. Give each input a stable ID. If a request is interrupted, check `GET /sessions/{id}/inputs/{input}` before sending it again with the same ID.
- Read user-facing entries with `GET /sessions/{id}/timeline?limit=30`. Use `GET /sessions/{id}/history?limit=30` for structured session records. When the response includes `next_cursor`, pass it unchanged as `cursor` to the same session and endpoint. If the server rejects an old cursor, start with the first page again.
- Open `GET /sessions/{id}/events` as a Server-Sent Events stream using an HTTP client that can send the bearer header. It sends an initial snapshot followed by live updates. Reconnect for a fresh snapshot if the stream ends or sends a `resync` event.
- Use `GET /sessions/{id}/subagents` to discover child sessions and open `GET /sessions/{child_id}/events` to follow a child. Child sessions are read-only through this API. Send work and controls to the parent session.

Use `GET /models` to find available model profiles and supported reasoning options. Send `POST /sessions/{id}/model` with `profile_id` and, if needed, `reasoning` from the selected profile’s choices. Use `GET /computers` to see the computers already authorized for this Bot. A session can select one of them with `POST /sessions/{id}/computer`.

Archive or restore an ordinary session with `PATCH /sessions/{id}` and `{"archived":true}` or `{"archived":false}`. Archiving pauses its work and preserves its history. Restoring does not restart work or re-enable automation. Astral applies the same archive rules as the Web interface.

## Upload an attachment

Choose a Computer for the session. Set `SESSION_ID` to the session ID and send the file as the binary request body. The `name` query parameter is the file name, which should be URL-encoded if needed. You can also pass `computer_id` to pick an authorized Computer.

```sh
curl --fail-with-body "$BOT_API/sessions/$SESSION_ID/attachments?name=diagram.png" \
  -H "Authorization: Bearer $ASTRAL_BOT_TOKEN" \
  -H "Content-Type: application/octet-stream" \
  --data-binary @diagram.png
```

Use the returned `uri` in the `files` array of `POST /sessions/{id}/inputs`. An ACP client can pass the same URI as a `resource_link` when connected to the same Bot and session. Images require a model that accepts them.

## Manage automations

- Use `GET /schedules` and `POST /schedules` to list or create schedules. With an existing schedule ID, use `GET`, `PATCH`, or `DELETE /schedules/{id}`. Use the current `revision` for updates and deletion.
- Use `GET /wake-endpoints` to find wake endpoints. Use `GET`, `PATCH`, or `DELETE /wake-endpoints/{id}` for an existing endpoint. Create new wake endpoints from the authorized Computer where the event originates.

The token operates only within its Bot’s scope. Computer authorization, platform chat bindings, private root sessions, and permanent conversation deletion are managed by the Owner in Astral.

Request `GET /api/v1/bot/openapi.json` on your Astral host with the same bearer token for the available methods, request bodies, and response schemas.
