---
title: "Use Astral from a terminal"
description: "Connect to an existing Astral, choose a Bot, send tasks, and export conversations."
---

Use the `astral` command to operate Astral from a terminal or your own scripts. This guide first walks through signing in to an existing Server, creating a conversation, and sending a task.

## Prepare the command and account

After an official Server or computer enrollment installation, this should work in the same terminal:

```sh
astral version
```

Have the Astral web address, administrator username, and password ready. Verify that the account works in a browser. Command-line sign-in lets you operate Astral; to let Astral work on this computer, use [computer enrollment](/docs/computers/connect/) instead.

## Sign in to an existing Server

Replace the address and username with your own:

```sh
astral connect --server https://astral.example.com --username admin
```

Enter the password when prompted. If two-step verification is enabled, use an authenticator code or recovery code. The CLI cannot complete a browser Passkey ceremony.

Check the connection after signing in:

```sh
astral doctor
```

Login details are saved in the current OS account's Astral configuration directory. This login appears under **Signed-in devices** in the web interface and can be signed out separately.

## Choose a Bot and send a task

**Find the Bot's ID:**

```sh
astral resources agents
```

Find the intended Bot's name and `id` in the result. The command uses the collection name `agents` for what the interface calls Bots. Make sure this Bot already has a model and default computer configured.

**Create a conversation:** replace `BOT_ID` with the actual ID from that result.

```sh
astral sessions new --agent BOT_ID --title "Check the working directory"
```

The result contains the new conversation's ID. Replace `SESSION_ID` with it and send a task:

```sh
astral sessions send SESSION_ID --text "List the files in the current working directory without changing them"
```

Then inspect the execution and reply:

```sh
astral sessions history SESSION_ID
```

The conversation also appears in Astral's interface. Send follow-up instructions to the same ID to continue that work.

## Common commands

| Purpose | Command |
| --- | --- |
| List connected computers | `astral resources computers` |
| List conversations | `astral resources sessions` |
| Add instructions for the next point where the running task can accept them | `astral sessions send SESSION_ID --mode steer --text "New instructions"` |
| Stop a task | `astral sessions stop SESSION_ID` |
| Continue a conversation | `astral sessions continue SESSION_ID` |
| Export records to a new file | `astral sessions export SESSION_ID --output session.jsonl` |

Ordinary messages wait for the current work to finish. `steer` supplies new instructions at the next point where the Bot can accept them. Stopping does not undo completed file operations. Exports contain conversation records; save work files separately.

## Recover access from a server terminal

Run these commands directly on the Server machine. They do not require an earlier `astral connect`:

| Purpose | Command |
| --- | --- |
| Check whether web sign-in is configured | `astral auth status` |
| Create the initial administrator account | `astral auth setup --username admin` |
| Reset a lost password or verification methods | `astral auth reset --username admin` |
| Inspect hostname restriction | `astral config hostnames show` |
| Disable hostname restriction to restore access | `astral config hostnames disable` |

Account reset signs out existing web logins and removes two-step verification. Read [the recovery instructions](/docs/hosting/security/#recover-account-access) before using it. Commands find installations owned by the current account. Select the intended one when prompted, or use `--data` for a manual Server's original data directory.

Manage the background Server with [systemctl and journalctl](/docs/hosting/server/#manage-the-background-service). On a remote enrolled computer, use `astral computer service status` or `astral computer service logs`, and `astral computer service restart` when it needs restarting.

## Call the management API

An authenticated script can use `astral request`. For example, read the current Server's bootstrap data:

```sh
astral request GET /api/v1/bootstrap
```

Use `--body-file /path/to/request.json` for a JSON request body. Consult the current Server's OpenAPI for paths, parameters, and responses. Run `astral help` for command options.
