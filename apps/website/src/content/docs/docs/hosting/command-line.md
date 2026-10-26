---
title: "Command-line access"
description: "Sign in to an existing Server, inspect resources, and operate sessions."
---

The `astral` CLI can connect to a running Server, inspect resources, send tasks, and export history. It uses a web account rather than Server private keys or the former Owner Token.

## Sign in

```sh
astral connect --server https://astral.example.com --username admin
astral doctor
```

Replace the address and username. The terminal prompts for a hidden password and, when needed, an authenticator or recovery code. The CLI does not perform browser Passkey ceremonies.

The credential is saved in the current user's Astral configuration directory. This CLI login appears in Signed-in devices and can be revoked independently.

## Inspect resources

```sh
astral resources agents
astral resources computers
astral resources sessions
```

Results are structured data. The UI calls them Bots; the CLI collection is still named `agents`. Use the returned IDs in later commands.

## Create and send work

Replace `BOT_ID` and `SESSION_ID` with actual identifiers:

```sh
astral sessions new --agent BOT_ID --title "Check project"
astral sessions send SESSION_ID --text "List the files in the current directory"
astral sessions history SESSION_ID
```

Use `--mode steer` to add a correction to running work. Ordinary `follow_up` messages are queued.

```sh
astral sessions stop SESSION_ID
astral sessions continue SESSION_ID
astral sessions export SESSION_ID --output session.jsonl
```

Exporting history does not bundle computer work files. Stopping does not roll back completed operations.

## Call the management API

Use `astral request GET /api/v1/bootstrap` against the signed-in Server. Supply JSON request bodies with `--body-file` to avoid shell interpretation of complex content.

Check `astral help` for your installed version. Signing the CLI in is different from enrolling an execution computer; use the generated [computer connection command](/docs/computers/connect/) for that.
