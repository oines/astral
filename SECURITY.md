# Security policy

## Reporting a vulnerability

Use GitHub's private [Report a vulnerability](https://github.com/oines/astral/security/advisories/new) form for suspected security issues. Include the affected commit or version, platform, reproduction steps, expected and actual behavior, and the impact you observed. Use a minimal reproduction with dummy credentials and data; do not include private keys, live tokens or personal conversations.

Do not disclose exploit details or credentials in a public issue or pull request. Ordinary bugs without a security impact can be reported through [GitHub issues](https://github.com/oines/astral/issues). Coordinate public disclosure through the private report while the issue is being investigated.

## Supported versions and deployment boundaries

Astral is pre-release software. Security fixes target the current main branch; there is no maintained long-term-support release line or guaranteed response time yet. Reports affecting earlier commits are still welcome.

An authorized Computer executes commands with the operating-system permissions of its user. AgentHome directories organize files and do not provide an operating-system sandbox. Container isolation depends on the runtime and host configuration. Protect Owner credentials, model and messaging credentials, the server encryption key, and backups. See [deployment and operations](docs/deployment.md) for data locations, access and network configuration.
