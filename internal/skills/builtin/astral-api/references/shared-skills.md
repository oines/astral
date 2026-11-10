# Shared skills

Read the live `skills` capability, then call `GET /v1/skills` from the Computer holding the draft. Its `manifest` shows the Server's published revisions. `local` describes that Computer's installed skills and drafts. These API operations use the calling command's Astral API access. Skills can still sync and be edited locally on Computers whose Astral API is disabled.

## Create or edit a draft

- For a new skill, check the manifest and local entries for the name. In `local.shared_root`, create `<name>/SKILL.md` and any scripts or references. Use a lowercase, hyphen-separated name and give the entry point a matching `name` and a clear `description`.
- To edit a skill, choose the intended `local.shared` entry by `name` and `path`, using `active` to identify the copy currently in use. An older draft can also appear in the list. Keep its `draft_id` if that is the copy you intend to work on.
- Edit and test the files with the usual tools. Re-read `GET /v1/skills` after editing. Check the intended path, `draft_id`, local `revision`, and `content_digest`. Use any reported `error` to correct the package before publishing. A local edit affects the shared files on this Computer.

## Publish one skill

When the user asks to publish, call `POST /v1/skills/shared/{name}/publish` on the Computer holding the draft. Use the draft's local `revision` as `base_revision`. Use `0` for a new name that has no published version. Put the current draft's `content_digest` in the `digest` field and pass its `draft_id`.

```json
{
  "base_revision": 3,
  "draft_id": "<local.shared entry draft_id>",
  "digest": "<local.shared entry content_digest>"
}
```

Give this intended publish a stable `Idempotency-Key` through the CLI's `--idempotency-key`. Keep the same name, body, and key if the outcome is uncertain and you need to repeat that request. Use a new key for a different body or publish intent. A successful response identifies the published revision. Other Computers can sync it after publication.

## Resolve a revision conflict

Re-read `GET /v1/skills` to learn the latest published revision. The working draft remains on the originating Computer. Download the immutable packages you need with `GET /v1/skills/shared/{name}/revisions/{revision}/content`, using the CLI's `--output` with distinct new `.tar` files. For an existing draft, compare its original base, the latest published revision, and the working draft. A draft whose base is `0` has no original Server package to download. Inspect or unpack the archives into separate scratch directories so the working draft remains available.

Integrate the changes into the intended working draft and test the result. If the user requested publication, get its fresh `content_digest` and `draft_id` from `GET /v1/skills`. Submit with the exact latest revision you compared as `base_revision` and a new idempotency key. If the Server moved forward again, compare the newer revision before another attempt. When the competing changes require a decision, show the relevant differences to the user before publishing.

To restore a previous published version on request, use its downloaded contents as a draft and publish them against the current Server revision. This creates a new revision containing the older files.
