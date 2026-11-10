# Schedules and event wake-up

Use the `schedules` capability for work that should run at a time. Use `wake-endpoints` when a program on a particular Computer should notify a Session about an event. Read the matching live capability document for request fields. Both management APIs use the current command credential and the current Bot's authorized view.

## Deliver a prompt on a schedule

Resolve the requested date, time, and recurrence before creating the schedule. `GET /v1/context` reports Astral's effective timezone in `timezone.name`. Recurring schedules use that timezone and five-field cron (`minute hour day-of-month month day-of-week`). For weekdays at 09:00 in that timezone, use `0 9 * * 1-5`. If the user wants recurring wall-clock time in a different timezone, check whether Astral's timezone can express it as requested before creating it. A schedule has no separate timezone field. For a one-time schedule, pass an absolute future RFC3339 instant with an offset, for example `{"kind":"once","at":"2030-01-01T09:00:00+08:00"}` with the requested date and offset. Changing Astral's timezone recalculates future recurring times and preserves a saved one-time instant.

Create a schedule with `POST /v1/schedules`. Omit `session_mode` and `session_id` to deliver to the calling root Session. For another visible root Session, supply `session_mode: "existing"` and its exact `session_id`. For a fresh Session on every occurrence, use `session_mode: "new"` and omit `session_id`. Each new Session uses the Bot's then-current defaults and starts without prior history or an IM binding. Put the file locations, task, and output destination in its prompt. Subagents can read schedules but cannot create, edit, delete, or receive scheduled work.

For example, to schedule a weekday checklist in the current Session, send this JSON to `POST /v1/schedules` after checking the timezone:

```json
{
  "title": "Weekday checklist",
  "prompt": "Read the project checklist. Report overdue items here and leave the file intact.",
  "enabled": true,
  "timing": {"kind": "cron", "cron": "0 9 * * 1-5"}
}
```

Use `GET /v1/schedules` to find a schedule and `GET /v1/schedules/{id}` to verify its target, status, and next run. `PATCH /v1/schedules/{id}` replaces the editable configuration. Send the current `revision` and the full `session_mode`, `session_id` for existing mode, `title`, `prompt`, `enabled`, and `timing`, with the intended change. Pause or resume with `enabled`; to resume an expired one-time schedule, move it to a future instant in the same edit. Delete with `DELETE /v1/schedules/{id}?revision=<revision>`. On a revision conflict, reload and apply the intended change to the latest configuration. If a create response is lost, look for a matching target, timing, title, and prompt before issuing another create. A create request has no send-style idempotency key.

`last_status: "delivered"` records durable acceptance of the prompt by the Session. For task completion, inspect the Session, using `last_session_id` for the latest new-session delivery. An idle target starts work; a busy existing Session receives the prompt as a Steer. If a result must go to a bound IM chat, say so in the prompt and verify the send separately. Times missed while the Server is stopped are skipped. Pausing or deleting a schedule prevents future deliveries. Already accepted input remains in the Session.

## Register and use an event wake-up endpoint

Create with `POST /v1/wake-endpoints` from a root Session running on the Computer where the source program will run. The authenticated command fixes the endpoint's Computer. The target defaults to the calling root Session. Supply `session_id` only for a different authorized root Session of the same Bot. Subagents cannot manage endpoints or be targets. Choose the Computer and target before registration because both are fixed. For a migration, register a new endpoint, update the caller, then retire the old endpoint as authorized.

Example registration body:

```json
{
  "title": "Local build events",
  "prompt": "When a build fails, inspect the reported local logs and summarize the failure in this Session.",
  "notes": "Caller: /opt/build-monitor/listener.py"
}
```

`prompt` directs the Bot's handling of events. `notes` records how to locate or maintain the caller. An endpoint is enabled by default; pass `"enabled": false` when registration should leave it paused.

`GET /v1/wake-endpoints` and `GET /v1/wake-endpoints/{id}` show the endpoint and its `trigger_url` when available. Other authorized Computers can manage visible endpoints. `PATCH /v1/wake-endpoints/{id}` sends `revision` and only the fields being changed (`title`, `prompt`, `notes`, `enabled`). Delete with `DELETE /v1/wake-endpoints/{id}?revision=<revision>`. Reload on conflict. After an uncertain create, check for the endpoint on the intended Computer and Session before posting again. Restoring an archived Session leaves its schedules and endpoints paused; explicitly re-enable the ones the user wants to resume.

The source program posts JSON to the returned `trigger_url` from the owning Computer, for example:

```json
{"event_id":"build-1842-failed","text":"Build 1842 failed.","data":{"build_id":1842}}
```

This is `POST /hooks/{id}` on the owning Daemon's localhost listener. It needs no bearer token and accepts at least one of nonempty `text` or non-null `data`. Keep it on that Computer's loopback address. Public forwarding via proxies, port mappings, tunnels, or webhook relays would expose anonymous event admission. Use command authentication for `/v1/wake-endpoints` management; use the returned local trigger URL for the event itself.

Use a stable `event_id` for a single occurrence and a different ID for each new event. Within the seven-day receipt window, retry the same ID with the same content to recover an uncertain response without waking the Session again. Reusing that ID for different content causes a conflict. A `202` receipt confirms durable acceptance. Events may still be batched for delivery and the Bot's result is a separate outcome. On `429`, honor `Retry-After`; keep the same ID and content for a later retry. A rejected event is not queued for replay. If the Daemon is offline, resume delivery once it reconnects. Pausing or deleting the endpoint stops future acceptance; already accepted inputs remain in the Session.
