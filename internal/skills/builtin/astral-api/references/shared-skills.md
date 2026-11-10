# Shared skills

Read the live `skills` capability, then call `GET /v1/skills` from the Computer where you plan to edit the skill. `manifest.shared` lists the currently published skills. `local` describes the folders on that Computer. These API operations use the calling command's Astral API access. Computers with Astral API disabled can still edit and sync their local files.

## Create or edit a shared skill

- For a new skill, check the manifest and local entries for the name. In `local.shared_root`, create `<name>/SKILL.md` and any scripts or references. Use a lowercase, hyphen-separated name and give the entry point a matching `name` and a clear `description`.
- To edit an existing skill, find its `local.shared` entry by `name` and use the reported `path`. Edit and test the files with the usual tools. A newly published version replaces the entire folder, including unpublished edits.

## Publish one skill

When the user asks to publish, call `POST /v1/skills/shared/{name}/publish` from the Computer with the tested files. Send an empty body. Astral publishes the whole folder identified by `{name}`. If the Server reports that the skill changed, wait for sync, then check the new local files before preparing another publish.

## Delete one shared skill

When the user asks to remove a skill from sharing, find its exact name in `manifest.shared`, then call `DELETE /v1/skills/shared/{name}` with an empty body. Deletion removes previously installed folders and their local edits when each Computer syncs. Deleting a folder with file tools affects only that Computer.

Use the CLI's `--idempotency-key` for each publish or delete. If the outcome is uncertain, repeat the same method, path, and key. Give a new operation a new key.
