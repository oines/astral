# Computers, sessions and execution

The Server owns configuration, credentials, sessions, IM accounts and delivery receipts. [Astral Core](https://github.com/oines/astral-core) owns the persistent agent loop. The Models packages in that same SDK own provider authentication, runtime model metadata, request encoding and response parsing. Core owns the authoritative session model selection; the Server stores business configuration and credential references.

This guide describes the runtime behavior and boundaries. The implementation follows [SPEC.md](../SPEC.md); messaging follows [the IM contract](im.md). For startup and development commands, see [the development guide](development.md).

## Connecting and maintaining Computers

**Settings → Computers → Add Computer** offers three paths:

- **Use this machine** reuses the server's local Computer.
- **Use a local container** creates a persistent container through the server's local Docker-compatible runtime.
- **Connect another machine** produces an expiring enrollment command. Its container option creates a separate Computer on the machine where the command runs.

Native enrollment uses the Server pin before sending its temporary token, then installs a visible **Astral Daemon** service for the current user: a macOS LaunchAgent, Windows logon task in Task Scheduler, or Linux `systemctl --user` unit. It never installs a system-wide daemon or requests elevation. The daemon runs with that user’s permissions after login.

New containers use Ubuntu 24.04 with development languages, build/network tools, Python data and document libraries, office/PDF/media/OCR tools, Chinese fonts and Playwright Chromium. **Computer details → Update Daemon** provides a local shell or PowerShell command. Updating restarts the native user service or the same existing container; it retains the Computer identity, Agent files and container-installed software. Existing containers are not rebuilt to adopt a new base image. Updates use the enrolled computer's saved Server address and pin, not the Server's current default access address. Running tasks are interrupted by the restart.

The Ubuntu Computer image is separate from Server deployment; this repository does not yet ship an official Docker Server image or Compose package. A Docker-hosted Server must persist its data/identity, configure an external address and initialize its Web administrator; `--no-local` can leave execution to separate Daemons.

Registration does not automatically grant a Bot access to a Computer. The initial setup explicitly grants access when you finish; subsequently, open the Bot's **Computer access** page to grant access and optionally set its default. A session can change its current Computer while idle. Existing process handles and file references remain attached to their original Computer.

## Working directories

AgentHome is shared by sessions of the same Agent on the same Computer. Forking copies history through the selected point into an independent Session; later changes to either Session do not enter the other's history. It does not copy working files or IM bindings.

New sessions use that AgentHome as their default CWD. `switch_computer` can change one session's Computer or CWD, including a directory change on the same Computer. A command's `workdir` or shell `cd` is temporary and does not change the session CWD.

## Context and skills

Environment snapshots are ordinary passive context returned by the Host's synchronous `CollectContext` callback. Scans run at creation, activation, complete assistant/tool boundaries and Computer/CWD changes. The Host prompt names the actual `Current Astral environment snapshot:` messages and contains only essential runtime conventions, with no imposed identity or persona. Its output instructions follow the current IM binding: unbound Sessions reply in Web, while bound Sessions receive the matching `send_message` instructions. It does not mention Probe or assume proactive mode is enabled. The optional persona in Bot settings is appended to each new model request; applying it affects every Session of that Bot immediately on its next request, without adding context messages or restarting conversations. Earlier environment snapshots remain in history. When compaction is needed, Core first drafts a summary of committed history, then collects a fresh environment and checks the full candidate request. Summary, environment and selected inputs commit together. A failed collection or budget check leaves the previous context intact and inputs pending; retrying preparation may regenerate the summary.

The initial environment also identifies the actual model, its effective context-window token limit and image-input support (including unknown support). Model-only changes add a short passive notice instead of another Computer environment snapshot; after compaction the fresh snapshot includes the current model.

Discovery runs on the selected Computer: global skills come from its OS user's `~/.agents/skills`, workspace skills from `<cwd>/.agents/skills`, and workspace guidance from exactly `<cwd>/AGENTS.md`. Parent directories are not searched. The snapshot supplies the current `AGENTS.md` content, and the Host prompt tells the model to follow it as the workspace instructions while working in that directory. Changing CWD changes the applicable workspace guidance. The model sees skill names, descriptions, scopes and absolute paths; content hashes stay internal, and body changes identify the files to reread. Environment text retains API target IDs and execution details while omitting boot IDs, repeated discovery identity and successful root diagnostics. Missing, unreadable, offline and unsupported discovery states remain explicit. History and context inspection do not scan or append snapshots.

## Files

Uploaded originals and generated files live on Computers. `astral-file://` links stream from the source Computer for preview or download. Images actually supplied to the model are also frozen inside the durable rollout, so history and replay remain valid after an original is removed or its source goes offline. That preserved model input does not substitute for an unavailable original-file download.

## Chat bindings and delivery

Messaging adapters cover **Telegram**, **QQ official Bot**, **QQ NapCat**, **Weixin ClawBot / iLink**, and **Feishu Bot**. Open **Settings → Bots**, choose a Bot, then use **Connect a platform**. QQ offers official and NapCat connection methods; Weixin uses QR authorization. See the [platform setup guide](im-platforms.md) for credentials, permissions and actual protocol limits. Each platform navigation item opens its account list; each account has its own credentials, access settings and chats. New accounts use **Only specified chats** with an empty list; add a chat and choose a new or existing unbound Session. **All chats the account can access** activates private chats on their first message and groups on their first mention/reply. Activation starts at that message without importing earlier history. Scope changes retain bindings and per-chat settings; changing a binding stops both Sessions first.

Bound chats continuously contribute context and compact as needed. While the Agent is working, new IM messages are observations without a separate IM Steer. Idle private messages, mentions and replies wake it directly. Web input follows the Owner's running-input preference, defaulting to FollowUp, with explicit Steer available in both ordinary and bound Sessions. **Proactive mode** is off by default; when enabled for an idle group, one LLM Probe decides after 10 seconds of quiet or a 60-second window. It returns only `decide({participate:boolean})`, never a reason or an instruction. Probe follows the Session’s current model unless the Agent selects an override.

A bound Session is fully usable in Web. Assistant text is displayed there; IM messages are sent only by the bound `send_message` tool or an explicit Computer API call. File sends and text sends retain stable targets, source files and persisted receipts. Unknown delivery is inspected before retry. Received IM images are processed in memory into frozen native model payloads; other attachments remain metadata until downloaded. Original bytes are available from the current Computer API, without creating a Computer file during reception.

Start Computer API discovery at `local_api.capabilities_url`. The index links to focused instructions, examples and runtime-generated schemas for each capability, plus the complete OpenAPI. The API exposes owned accounts, authorized chat resolution, filtered message pagination, message-ID lookup, original attachment streaming, send receipts and each account's platform-operation catalog. Telegram and NapCat operations include member/profile queries and group management; NapCat also exposes notices, group files and rich essence entries with independently authorized media reads and Computer downloads. See [the IM API guide](im.md).

## Execution and retries

Core `Session` owns execution, queues, cancellation, compaction and model retries. Each input has a caller-supplied stable ID and an indexed durable receipt; resubmitting it returns the same receipt without reloading attachments or starting another run. Astral queues Web/IM input as Steer or FollowUp, then explicitly calls `Continue`. Queuing alone does not start execution. Stop and failure leave pending inputs for explicit continuation. Normal completion without an explicit send leaves IM silent. Passive maintenance uses Core’s ordinary compaction and drains accumulated input batches without starting the main Agent.

Normal generation and summaries use the same retry policy: at most three total attempts, a four-minute timeout per attempt, and cancellable exponential waits starting at one second and capped at five seconds unless a valid longer Retry-After applies. Retries reuse the frozen request. Failed stream fragments and retry errors remain execution records and never become model conversation. The Web UI shows attempt and retry progress; reconnecting only restores the event stream.
