# jira-ai-creator

[![Test](https://github.com/kengou/jira-ticket-creator/actions/workflows/test.yml/badge.svg)](https://github.com/kengou/jira-ticket-creator/actions/workflows/test.yml)
[![Docker](https://github.com/kengou/jira-ticket-creator/actions/workflows/docker.yml/badge.svg)](https://github.com/kengou/jira-ticket-creator/actions/workflows/docker.yml)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/kengou/jira-ticket-creator/badge)](https://securityscorecards.dev/viewer/?uri=github.com/kengou/jira-ticket-creator)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

> **Declarative Jira issue creation from YAML** — define once, create anywhere.

A production-ready CLI tool for creating Jira issues from YAML configuration files. Define Epics, Stories, and Bugs declaratively, run one command, and watch your issues appear in Jira with proper dependencies and relationships.

## ⚡ 60-Second Quickstart

```bash
# 1. Build
cd jira-ai-creator && go build -o jira-ai-creator

# 2. Configure credentials — Jira Cloud (API token from id.atlassian.com)
export JIRA_URL="https://yourcompany.atlassian.net"   # a Cloud custom domain works too
export JIRA_EMAIL="you@yourcompany.com"
export JIRA_TOKEN="your-api-token"
# Jira Data Center: JIRA_URL="https://jira.yourcompany.com" and a personal access token; no JIRA_EMAIL

# 3. Create issues
./jira-ai-creator apply -f docs/examples/stories.yaml
```

## Key Features

| Feature | Description |
|---------|-------------|
| **Declarative YAML** | Define issues as code, version control your backlog |
| **Dependency Ordering** | Automatic topological sort respects parent/child and `dependsOn` |
| **Idempotency** | State file prevents duplicate creation on re-runs |
| **Issue Links** | `blocks`, `relates to`, `duplicates` between issues |
| **Dry Run** | Preview changes without touching Jira |
| **Validation** | Catch errors before hitting the API |
| **Platform detection** | Cloud and Data Center detected automatically, including Cloud sites on a custom domain |

## Commands

```bash
jira-ai-creator check                     # Verify connection, detected platform, auth
jira-ai-creator validate -f issues.yaml   # Check configuration
jira-ai-creator plan     -f issues.yaml   # Preview creation order
jira-ai-creator apply    -f issues.yaml   # Create issues in Jira
jira-ai-creator state    list             # View created issues
```

## Documentation

| Document | Description |
|----------|-------------|
| [**Quickstart**](docs/quickstart.md) | Installation, authentication, first run |
| [**Configuration**](docs/configuration.md) | All config fields with examples |
| [**Schema**](docs/schema.md) | Schema rules, relationships, idempotency |
| [**Troubleshooting**](docs/troubleshooting.md) | Common errors, auth, rate limits |

## Minimal Example

```yaml
schemaVersion: "1.0"

defaults:
  projectKey: MYPROJ

issues:
  - id: EPIC-001
    issueType: Epic
    epicName: "User Authentication"
    summary: "Implement OAuth 2.0 authentication"
    
  - id: STORY-001
    issueType: Story
    parent: EPIC-001
    summary: "Add Google SSO provider"
    description: |
      *As a* user
      *I want to* sign in with Google
      *So that* I don't need another password
```

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.

# jira-ticket-creator
