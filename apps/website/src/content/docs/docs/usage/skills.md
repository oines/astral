---
title: "Skills and AGENTS.md"
description: "Save reusable workflows as skills (SKILL.md), or add project conventions in AGENTS.md."
---

When you repeat the same formatting rules or multi-step workflow, save it as a **skill**. When rules belong only to one folder (such as “put outputs in `reports/`”), put them in **`AGENTS.md`**. One-off requests can stay in your prompt.

| Purpose | File | Scope |
| --- | --- | --- |
| Reusable methods (with optional scripts or templates) | `SKILL.md` | Current project, local user, or **Shared skills** synced across all computers |
| Folder layout and project rules | `AGENTS.md` | **Current working directory only** (parent folders are not searched) |

## Built-in skills

On computers running a current Daemon, Server syncs built-in skills automatically—letting you ask the Bot to turn a workflow into a skill, search past Astral sessions, or configure schedules and local event wake-ups.

:::note[Requirements for Astral API skills]
Session search and Computer API calls require [**Enable Astral API**](/docs/computers/connect/#control-what-the-computer-can-call) on that computer; the bundled API helper requires Python 3.8+. Updating Astral replaces the built-in skill directory—save any local customizations as a project or user skill first.
:::

## Create a skill

Verify your session's computer and working directory, then create the file yourself or ask the Bot to write it:

- **Current project only:** `.agents/skills/<skill-name>/SKILL.md`
- **All projects for this OS user on this computer:** `~/.agents/skills/<skill-name>/SKILL.md`

Example `.agents/skills/meeting-actions/SKILL.md`:

```md
---
name: meeting-actions
description: Turn meeting notes into actions, owners, and due dates.
---

Read the meeting notes identified by the user.
Create a table with action, owner, and due-date columns; mark missing details as "To confirm."
Keep the original notes unchanged, save the result separately, and provide a file link.
```

Both `name` and `description` inside the opening `---` block are required so the Bot knows when to pick it up. Put helper scripts or templates in the same folder. Afterward, ask the Bot which skills it sees and try it:

> Use the meeting-actions skill to process the meeting notes I attached.

:::caution[Inspect third-party skills first]
Skills can bundle executable scripts—always review their source, instructions, and code before use. See [Permissions and privacy](/docs/start/safe-use/#use-care-with-third-party-material-and-skills).
:::

## Add project guidance (AGENTS.md)

Place `AGENTS.md` in the exact working directory your session uses:

```md
# Project guidance

This directory holds weekly operations reports.
Source files live in input/. Write reports to reports/ named by date.
Keep source files untouched, cite numbers, and flag uncertain details.
```

Ask the Bot to confirm its working directory and summarize the conventions it read. Remember: if the file is `/work/reports/AGENTS.md` but the session moves into `/work/reports/input/`, switch back to the project root or add guidance where you work.

## Sync across computers: Shared skills

Use **shared skills** to keep one published version synced across every connected computer:

1. Ask the Bot to create or edit a shared skill and test it locally first (unpublished edits stay on the current computer).
2. Ensure [**Enable Astral API**](/docs/computers/connect/#control-what-the-computer-can-call) is enabled on this computer, then explicitly ask the Bot to **publish** it.
3. Other computers sync the published version automatically—even those with Astral API turned off (sync replaces the folder; copy any local edits to a project or user skill to preserve them).
4. To remove a shared skill everywhere, ask the Bot by name from a computer with Astral API enabled (offline computers clean up when reconnecting).

Older computer services need a [Daemon update](/docs/computers/connect/#update-a-connected-daemon) to publish and sync under these rules. Regular project and user skills stay local—copy them manually and install required software on other machines.

## Skill not showing up?

1. Verify the session's selected computer and current working directory.
2. Check the folder path, exact `SKILL.md` filename, and read permissions for the service OS account.
3. Confirm the opening `---` header has non-empty `name` and `description` fields.
4. Check if the computer Daemon needs an [update](/docs/computers/connect/#update-a-connected-daemon); if visible but failing to run, confirm required tools and files exist on the **current computer**.
