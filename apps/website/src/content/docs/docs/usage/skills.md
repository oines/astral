---
title: "Skills and project guidance"
description: "Save reusable working methods and instructions for a particular project."
---

If you keep explaining the same report format, save it as a skill. A skill contains instructions, optionally with scripts or templates, that the Bot can use for relevant tasks. A one-off requirement can stay in your conversation.

For rules specific to a project, such as “Put all output in the reports folder,” use an `AGENTS.md` file in that project's working directory. Skills describe reusable methods; project guidance describes how to work in that directory.

## Use a built-in skill

On a computer with a current Daemon, ask the Bot to create a skill from a repeated workflow, find a decision from an earlier Astral session, or set up a scheduled task or local event hook. The Server supplies the corresponding built-in skills to the computer. They appear alongside your own skills after the first sync. Session history and the Computer API require **Enable Astral API** on the selected computer. The bundled API command helper uses Python 3.8 or later.

When Astral updates the built-in bundle, it replaces the files in the same folder, including any local edits. To keep your edits, ask the Bot to save a copy as a project or user skill before the update.

## Add a skill

First check the session's computer and working directory. Put the skill on that computer, either yourself or by asking the Bot to create the files.

Choose its location:

| Where you want to use it | Location |
| --- | --- |
| Across projects for the same system user on this computer | `~/.agents/skills/` in that user's home directory |
| In the current project | `.agents/skills/` under the current working directory |

Each skill has a folder containing a file named `SKILL.md`. For example, create `.agents/skills/meeting-actions/SKILL.md` with:

```md
---
name: meeting-actions
description: Turn meeting notes into actions, owners, and due dates.
---

Read the meeting notes identified by the user.
Create a table with action, owner, and due-date columns.
Mark an unspecified owner or date as "To confirm."
Keep the source notes unchanged. Save the result separately and provide a file link.
```

The block between the first two `---` lines describes the skill: `name` identifies it and `description` says when to use it. Both must be nonempty. The rest explains the method. Put supporting scripts and templates in the same folder and explain how they should be used.

After saving, ask the Bot which skills it can see, then try:

> Use the meeting-actions skill to process the meeting notes I attached.

Check the table and output file against the instructions. Review a third-party skill's source, instructions, and scripts before using it; see [outside content and skills](/docs/start/safe-use/#use-care-with-third-party-material-and-skills).

## Add project guidance

Place `AGENTS.md` in the directory the session actually uses. Explain the project's purpose, file organization, and working conventions. For example:

```md
# Project guidance

This directory holds weekly operations reports.
Source material is in input/. Put generated reports in reports/.
Keep source files unchanged and name new reports by date.
Cite the source file for numbers and flag uncertain information.
```

Ask the Bot to confirm its working directory and summarize the guidance it found.

Astral reads **the `AGENTS.md` in the current working directory**. It does not search parent directories. If the file is `/work/reports/AGENTS.md` but the session is working in `/work/reports/input/`, return to the project directory or provide guidance in the directory actually being used.

## Use skills across computers

Use a shared skill when you want the same published version on multiple connected computers. Ask the Bot to create a shared skill or adapt one it can already see. Test it on the current computer, make sure [**Enable Astral API**](/docs/computers/connect/#control-what-the-computer-can-call) is on there, then explicitly ask the Bot to publish it. Until publication, edits are visible only on that computer, where Bots use the same local copy. Other computers receive the published version when they sync, including those with Astral API turned off.

If a computer has changes to the same skill, it keeps its edited files. Ask the Bot to check the skill's local and published versions. It can compare the drafts with the published version, combine the changes you want, and publish the result. Other shared skills can continue to update.

For a project or user skill, copy its folder and any required resources to the other computer. Install any software the skill needs there before using it.

If the Bot cannot find a skill, check:

1. The selected computer and current working directory.
2. The folder location, the exact `SKILL.md` filename, and whether the computer service's system account can read it.
3. The opening `---` block and its nonempty `name` and `description` fields.
4. Whether the computer service needs an [update](/docs/computers/connect/#update-a-connected-daemon).

If the skill is visible but fails to run, check that its required software and files exist on the current computer rather than only on another machine.
