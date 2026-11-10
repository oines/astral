# Shared skills

Read the live `skills` capability, then call `GET /v1/skills` from the Computer where you plan to edit the skill. Its `manifest` shows the Server's published revisions. `local` describes the skills on that Computer. These API operations use the calling command's Astral API access. Skills can still sync and be edited locally on Computers whose Astral API is disabled.

## Create or edit a shared skill

- For a new skill, check the manifest and local entries for the name. In `local.shared_root`, create `<name>/SKILL.md` and any scripts or references. Use a lowercase, hyphen-separated name and give the entry point a matching `name` and a clear `description`.
- To edit an existing skill, find its `local.shared` entry by `name` and use the reported `path`. Normal sync updates that copy in its existing directory when it has no unpublished changes.
- Edit and test the files with the usual tools. Re-read `GET /v1/skills` after editing. Check the intended path, local `revision`, and `content_digest`. Use any reported `error` to correct the package before publishing. Edits stay on this Computer until you publish them.

## Publish one skill

When the user asks to publish, call `POST /v1/skills/shared/{name}/publish` from the Computer with the edited files. Astral uses `{name}` to locate that Computer's working directory for the skill. Set `base_revision` to the local entry's `revision`, or use `0` for a name with no published version. Set `digest` to the entry's current `content_digest`.

```json
{
  "base_revision": 3,
  "digest": "<local.shared entry content_digest>"
}
```

Give this intended publish a stable `Idempotency-Key` through the CLI's `--idempotency-key`. Keep the same name, body, and key if the outcome is uncertain and you need to repeat that request. Use a new key for a different body or publish intent. A successful response identifies the published revision. Other Computers can sync it after publication.

## Resolve a revision conflict

Re-read `GET /v1/skills` to learn the latest published revision. The edited files remain in the local skill directory. Download the packages you need with `GET /v1/skills/shared/{name}/revisions/{revision}/content`, using the CLI's `--output` with distinct new `.tar` files. Compare the original base, the latest published revision, and the local files. With a base of `0`, compare the latest published revision directly with the local files. Inspect or unpack the archives into separate scratch directories to preserve the edited files.

Integrate the changes into the local skill directory and test the result. If the user requested publication, get the updated `content_digest` from `GET /v1/skills`. Submit with the exact latest revision you compared as `base_revision` and a new idempotency key. If the Server moved forward again, compare the newer revision before another attempt. When the competing changes require a decision, show the relevant differences to the user before publishing.

To restore a previous published version on request, replace the files in the local skill directory with the downloaded contents and publish them against the current Server revision. This creates a new revision containing the older files.
