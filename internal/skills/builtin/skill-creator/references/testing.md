# Testing a skill

<!-- Adapted by Astral from Anthropic's skill-creator. Copyright 2026 Anthropic, PBC. See ../LICENSE.txt. -->

## Pick a few representative cases

Start with two or three realistic prompts. Use the kind of inputs the user will actually give the Bot. Include a normal case, a useful edge case, and a similar request where the skill should stay out of the way. For each case, write down the expected result and any action that must stay within the user's authorization.

Keep test inputs and outputs in a temporary workspace outside the skill's directory. Give each run its own output path. Use sample or read-only fixtures when the skill would otherwise publish, send messages, or mutate important files.

## Check execution and selection separately

- **Execution.** Ask a Bot to use the exact skill path and do the task. Check the artifact and the actions taken. This shows how the skill behaves once loaded.
- **Selection.** Give a fresh session the task without naming the skill. Confirm that the expected skill is in that session's index, then check whether the Bot reads its `SKILL.md`. Try an adjacent request where it should use another workflow. Adjust the description using concrete evidence of missed selections or false matches.

An Astral root session can optionally start a subagent with `inherit_context: false` for an independent run. It starts with its own conversation history and still uses the skills visible on its selected Computer and CWD. Give it only the case prompt, necessary fixture paths, and its output location for a selection test. If a fresh run is unavailable, report which behavior you verified directly.

## Compare and refine

When comparing revisions, run the same prompt and fixtures against the old and new skill under comparable settings. For a new skill, use a no-skill baseline only if the baseline session can actually run without seeing the draft. Separate output paths keep the runs from overwriting each other.

Judge the outputs against observable checks, such as whether a report contains the requested fields, a script succeeds on the fixture, or a file is left intact. Keep subjective feedback qualitative. Report specific evidence from the outputs and use it to decide whether to change the description, instructions, or a resource. Rerun the affected cases after a revision and keep a fresh case for a broader change.
