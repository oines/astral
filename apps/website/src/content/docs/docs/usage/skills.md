---
title: "Skills and project guidance"
description: "Give your Bot reusable instructions and guidance for the current project."
---

Skills are reusable working instructions stored as files on a computer. Astral discovers skills on the current computer, and the Bot reads relevant instructions for its task.

## Add a skill

Create a skill directory under either:

- `~/.agents/skills/` in the computer user's home directory.
- `.agents/skills/` inside the session's current working directory.

For example, create `.agents/skills/project-review/SKILL.md`:

```md
---
name: project-review
description: Review project changes and identify behavior that needs verification.
---

Read the project guidance before inspecting changes.
Prioritize user-visible problems and include file locations and validation steps.
```

Both `name` and `description` must be nonempty. Put supporting scripts and assets in the same skill directory and explain how to use them.

## Add project guidance

Place `AGENTS.md` in the actual working directory used by the session. Describe project structure, useful commands, and working conventions.

Astral reads **that file in the current directory**. It does not walk parent directories for inherited instructions. Arrange your working directory or guidance files accordingly.

## Use skills across computers

Switching computers does not copy skills. Install the skill and its dependencies on each computer where it is needed. Discovering instructions does not mean the required software is installed.

If a skill is missing, check the session's computer, working directory, readable files, frontmatter, and computer service version. Ask the Bot which skills it currently sees to help diagnose the issue.
