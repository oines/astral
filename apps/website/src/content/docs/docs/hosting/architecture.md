---
title: "Choose how to run Astral"
description: "Understand Desktop, Server, and computers before choosing a deployment."
---

Astral Server stores Bots, sessions, model connections, and chat accounts, and coordinates work. Computers perform file operations and commands. They may run on the same machine or separate machines.

## Start with Desktop

Desktop manages a local Server and computer connection. It opens directly without requiring a web account first.

Keep the machine awake and running for ongoing tasks. Closing a window and fully quitting the application differ according to the background-running preference.

## Use an always-on Server

Run a Server on a persistent machine when you want the same Bots available from several devices or want chat connections to remain online. Access it with a browser.

A browser is an interface; it does not register its device as a computer. Connect your laptop separately if the Bot should work on it.

The Server needs access to your model, search, and chat services. Each connected computer needs an outbound route to the Server.

## Choose execution environments

| Environment | Use |
| --- | --- |
| Server machine | Files and tools on the machine hosting Astral |
| Remote computer | Another operating system, hardware, or existing project environment |
| Container computer | A separate, persistent task environment |

A session can switch between authorized computers. Files and processes remain on the machine that created them.

## Website hosting and the Astral runtime

The public website and these docs are static and can be hosted on Cloudflare Pages. Astral Server needs a persistent process, stored data, and connections to models and computers; uploading a static site does not run it.

Continue with [Run a Server](/docs/hosting/server/) and [Sign-in and security](/docs/hosting/security/).
