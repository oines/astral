---
name: skill-creator
description: Create, adapt, test, or improve reusable Astral skills. Use when the user wants to turn a repeated workflow into a skill, revise a SKILL.md, or check whether a skill works or is selected for the right requests.
license: Apache-2.0
---

<!-- Adapted by Astral from Anthropic's skill-creator. Copyright 2026 Anthropic, PBC. See LICENSE.txt. -->

# Skill Creator

Create or improve a skill through a short cycle: capture the intended workflow, write a draft, try realistic tasks, and refine it from the results. Start at the stage the user has reached. For a narrow edit, a focused check may be enough.

## Capture the workflow

Use the current conversation and any existing skill as evidence. Identify what the skill helps a Bot do, when it should be selected, what a successful result looks like, and any requirements or actions that need the user's authorization. Ask only about choices that would change the result. A single instruction about the current project may belong in its `AGENTS.md`; a skill describes a workflow someone expects to use again.

## Choose the skill's location

Use the latest Astral environment snapshot to identify the selected Computer and the session's current working directory.

- For this project, use `<CWD>/.agents/skills/<name>/`. Use this as the default for a new skill unless the user wants a different scope.
- To reuse it across projects on this Computer, use `~/.agents/skills/<name>/` under the operating-system account running the Daemon. Resolve the home directory on that Computer.
- To edit an existing skill, use the scope or path the user supplied to select its absolute `SKILL.md` path from the current index. If several entries still fit, resolve which copy the user wants before editing. When adapting a built-in for personal use, create a copy in the chosen user or project directory unless the user explicitly wants to edit the installed copy.
- For an existing shared skill, use its managed path from the current skill index. To create a shared draft, find `local.shared_root` with `GET /v1/skills` when the Computer API is available, then create `<shared_root>/<name>/SKILL.md`. If the shared location is unavailable, explain that before choosing another scope. Saving a shared draft affects this Computer. When the user asks to publish, use the shared skill workflow in the built-in `astral-api` skill.

Use the existing file and command tools. A shell `cd` changes that command's working directory; Astral discovers project skills from the session CWD.

## Draft or revise

Put the entry point at `<name>/SKILL.md`. Use a lowercase, hyphen-separated name that matches the directory and a nonempty description that tells the Bot when to choose the skill.

```markdown
---
name: meeting-actions
description: Extract action items, owners, and due dates from meeting notes and transcripts.
---

# Meeting Actions

Read the notes the user selects. List each action, its owner, and its due date.
Mark missing owners and dates as unknown. Cite the source passage for each item.
```

Keep the core workflow and its constraints in `SKILL.md`. Link to `references/` for details that are only needed in certain cases. Add `scripts/` for repeatable operations where executable code improves reliability, and `assets/` for files the workflow uses in its outputs. Use relative links to those files and explain when to use them. Check that any required program or connector is actually available in the target environment.

## Verify and iterate

Check that the entry point and referenced files exist. On the next environment refresh, confirm the index shows the intended name, scope, and path. For a new skill or a substantial rewrite, try a representative task and an adjacent task that should use a different workflow. Check the actual output and side effects against the user's requirements. If editing an existing skill, preserve a copy of the old version before making changes when a comparison would help.

For independent trials, comparisons, or description tuning, read [Testing a skill](references/testing.md). Use the observed failures to refine the smallest relevant part of the skill, then rerun the affected case. Tell the user where the skill lives, which checks you ran, and any behavior still unverified.
