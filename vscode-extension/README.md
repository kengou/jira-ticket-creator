# Jira AI Creator — VS Code Extension

[![VS Code Extension](https://github.com/kengou/jira-ticket-creator/actions/workflows/vscode-extension.yml/badge.svg)](https://github.com/kengou/jira-ticket-creator/actions/workflows/vscode-extension.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

> **Create Jira issues with AI assistance via the `@jira` chat participant** — no terminal required.

A VS Code Chat extension that wraps the [jira-ai-creator](https://github.com/kengou/jira-ticket-creator) CLI, bringing declarative Jira issue creation into the Copilot Chat panel.

## Prerequisites

- **VS Code** 1.100 or later
- **GitHub Copilot** extension (provides the chat panel)
- **jira-ai-creator** binary installed and on your `$PATH` ([install instructions](https://github.com/kengou/jira-ticket-creator#-60-second-quickstart))

## Installation

```bash
code --install-extension jira-ai-creator-vscode-0.2.0.vsix
```

Or from the Extensions sidebar: **Install from VSIX...** and select the `.vsix` file.

## Features

### Chat Participant — `@jira`

Open the Copilot Chat panel and type `@jira` to interact. The participant is **sticky** — once invoked, subsequent messages in the conversation stay directed to `@jira` without re-typing.

### Chat Commands

| Command | Description | Requires Auth |
|---------|-------------|:---:|
| `@jira /validate` | Validate the YAML config open in the active editor | No |
| `@jira /plan` | Preview issue creation order (dry run) | No |
| `@jira /apply` | Create issues in Jira from the active YAML file | Yes |
| `@jira /generate` | Generate a YAML plan from a plain-English description | Yes (AI key) |
| `@jira /epics` | List existing epics in a Jira project | Yes |
| `@jira /fields` | Search Jira field definitions (find custom field IDs) | Yes |
| `@jira /linktypes` | List available issue link types | Yes |

Type a description without a slash command to generate YAML directly:

```
@jira 5 stories for implementing OAuth2 login
```

### VS Code Commands

Available from the Command Palette (`Ctrl+Shift+P` / `Cmd+Shift+P`):

| Command | Description |
|---------|-------------|
| `Jira AI Creator: Set Jira Token` | Store your Jira PAT securely in VS Code's SecretStorage |
| `Jira AI Creator: Set Anthropic API Key` | Store your Anthropic API key securely |
| `Jira AI Creator: Open Generated File` | Open a generated YAML file in the editor |
| `Jira AI Creator: Mark File as Reviewed` | Change `# reviewed: false` to `# reviewed: true` in an AI-generated file |

### Activation

The extension activates lazily — it loads only when you first mention `@jira` in Copilot Chat. No resources are consumed until then.

### Auto-Detection

The extension minimizes what you need to type:

| Value | Auto-detected from | Fallback |
|-------|-------------------|----------|
| Config file (`-f`) | Active editor (if `.yaml` / `.yml`) | Prompted in chat |
| Jira URL | `JIRA_URL` env var or `jiraAiCreator.jiraUrl` setting | Prompted in chat |
| Jira token | `JIRA_TOKEN` env var or SecretStorage | Prompted to configure |
| AI provider | `jiraAiCreator.aiProvider` setting | Prompted in chat |
| Binary path | Resolved from `$PATH` via `which` | `jiraAiCreator.binaryPath` setting |

### Output Formatting

CLI output is reformatted for the chat panel:

- **Validate** renders a summary table with project, schema version, and issue counts
- **Plan** renders a table with issue type, ID, summary, and parent references
- **Apply** renders a results table with creation status per issue
- **Epics / Fields / Link Types** render as clean code blocks
- Emojis and ANSI escape codes are stripped; errors are extracted from CLI noise

### Safety Gates

- **AI-generated files** contain `# reviewed: false` — the `/apply` command blocks until the file is reviewed. A "Mark as Reviewed" button is provided in chat.
- **Credentials** are never passed as CLI arguments (visible in `ps aux`). They flow via environment variables or VS Code SecretStorage.
- **Binary check** on activation — if `jira-ai-creator` is not found, the extension shows a warning with install instructions.

## Configuration

All settings are under the `jiraAiCreator` namespace:

| Setting | Type | Default | Description |
|---------|------|---------|-------------|
| `jiraAiCreator.binaryPath` | `string` | `""` (auto-detect from `$PATH`) | Absolute path to the `jira-ai-creator` binary |
| `jiraAiCreator.jiraUrl` | `string` | `""` | Jira base URL (overrides `JIRA_URL` env var) |
| `jiraAiCreator.cloud` | `boolean` | `false` | **Deprecated** — Jira Cloud (including custom domains) is detected automatically. When `true` the extension still passes `--cloud`, which the CLI accepts with a deprecation warning |
| `jiraAiCreator.aiProvider` | `string` | `""` | Default AI provider: `claude`, `copilot`, or `opencode` |

## Development

```bash
# Install dependencies
npm install

# Build (lint + typecheck + bundle)
npm run build

# Watch mode (rebuild on save)
npm run watch

# Lint only
npm run lint

# Type-check only
npm run typecheck

# Package as .vsix
npm run package

# Bump version and package
npm run release:patch   # 0.2.0 -> 0.2.1
npm run release:minor   # 0.2.0 -> 0.3.0
npm run release:major   # 0.2.0 -> 1.0.0
```

### Debug

1. Open the `vscode-extension/` directory in VS Code
2. Press `F5` to launch the Extension Host
3. Open Copilot Chat in the new window and type `@jira`

### Project Structure

```
vscode-extension/
├── package.json              # Extension manifest
├── tsconfig.json             # TypeScript configuration
├── eslint.config.mjs         # ESLint with strict TypeScript rules
├── esbuild.mjs               # Production bundler
├── icon.png                  # Extension icon
├── LICENSE                   # Apache 2.0
└── src/
    ├── extension.ts          # Activation, binary check, command registration
    ├── participant.ts        # @jira chat handler, command routing, followups
    ├── cli.ts                # Spawn jira-ai-creator binary, stream output
    ├── config.ts             # Auto-detection (file, env vars, settings)
    ├── secrets.ts            # SecretStorage wrapper for tokens
    ├── format.ts             # CLI output -> chat markdown formatter
    └── commands/
        ├── validate.ts       # /validate handler
        ├── plan.ts           # /plan handler
        ├── apply.ts          # /apply handler with review gate
        ├── generate.ts       # /generate + free-form prompt handler
        ├── epics.ts          # /epics handler
        ├── fields.ts         # /fields handler
        └── linktypes.ts      # /linktypes handler
```

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.
