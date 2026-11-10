---
name: astral-api
description: Use the Astral Computer API for scheduled tasks, event hooks, session management, shared skills, chat and platform operations, files, and runtime information. Use when the task needs an Astral API operation or the user asks how to call one.
---

# Astral Computer API

Use the Computer API from the selected Computer within the calling Bot and session's permissions. Read `local_api.base_url` from the latest `Current Astral environment snapshot`. If `local_api` is absent, report which API operation is unavailable on this Computer and use the available file or command tools for any remaining work.

## Discover and call an operation

In `exec_command`, run the bundled [Astral API CLI](scripts/astral_api.py) using the absolute path beside this skill's `SKILL.md`. Use the current environment's exact base URL and a Python 3.8+ interpreter available on the selected Computer (`python3`, `python`, or `py -3`).

```sh
python3 '<absolute path to astral-api/scripts/astral_api.py>' --base-url '<local_api.base_url>' capabilities
python3 '<absolute path to astral-api/scripts/astral_api.py>' --base-url '<local_api.base_url>' capabilities schedules
python3 '<absolute path to astral-api/scripts/astral_api.py>' --base-url '<local_api.base_url>' request GET /v1/context
```

1. Use `capabilities` to read `GET /v1/capabilities`. Read the matching capability's notes, examples, and schema with `capabilities <id>` or follow its returned `read_url` with `request GET`.
2. Resolve exact resource IDs and obtain the intended effect from the user's request. If the operation requires a revision, read the target's current revision first.
3. Use `request METHOD PATH` with the live contract's method and path. Pass a JSON request body using `--body-file FILE` or `--body-file -` for stdin. Pass `--idempotency-key KEY` and `--range VALUE` when the operation requires them. The CLI reads the active command's `ASTRAL_API_TOKEN` from its environment.
4. Keep large responses in a local file or filter them inside the command. Use `--output FILE` with a new file path for streamed bytes. An HTTP error exits nonzero. After an uncertain write, use the operation's read or receipt workflow to verify the outcome before retrying.

Use the CLI for authenticated `/v1/` operations. For local programs delivering anonymous hook events, follow the hook's own `trigger_url` and the [event delivery instructions](references/schedules-and-hooks.md).

If a suitable Python interpreter is unavailable, use the same live API with the Computer's existing HTTP client. In a POSIX shell, substitute the exact URL from the environment:

```sh
printf 'Authorization: Bearer %s\n' "$ASTRAL_API_TOKEN" |
  curl -q --noproxy '*' --silent --show-error --fail --header @- '<local_api.capabilities_url>'
```

In PowerShell, pass `@{ Authorization = "Bearer $env:ASTRAL_API_TOKEN" }` to `Invoke-RestMethod -Headers` within the command. Keep the token out of URLs, files, logs, and expanded external process arguments.

## Choose the capability

| Task | Capability and guidance |
| --- | --- |
| Schedule a one-time or recurring prompt, or wake a session when a local process emits an event | `schedules` or `wake-endpoints`. Read [Schedules and hooks](references/schedules-and-hooks.md). |
| Find earlier decisions, task results, or messages in Astral sessions | `history`. Use the `session-history` skill when available. |
| Rename, archive, or restore an existing session | `sessions`. Resolve the exact ID from the history catalog. The endpoint applies to this Bot's unbound root sessions. Use `agent_control` for supported actions on direct subagents. |
| Find the managed skill directory, check a draft, or publish a shared skill | `skills`. Read [Shared skills](references/shared-skills.md). |
| Inspect or act on authorized chats, send a file, or retrieve a file | `chats` or `files`. Read [Chats and files](references/chats-and-files.md). |
| Check the calling session, its source Computer, available Computers, or configured timezone | `environment`. Read `/v1/context` or `/v1/computers` as appropriate. |
| Choose a configured model and reasoning level for a subagent | `models`. Use the returned Profile ID and reasoning value for the subagent tool; treat availability as saved configuration. |

For capabilities added by a newer Astral version, use the live index and its schemas to discover the supported workflow.
