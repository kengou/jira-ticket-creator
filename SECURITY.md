# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| latest (`main`) | ✅ |

## Reporting a Vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Use [GitHub private vulnerability reporting](https://github.com/kengou/jira-ticket-creator/security/advisories/new) to report a vulnerability confidentially. You will receive a response within 5 business days.

Please include:

- A description of the vulnerability and its potential impact
- Steps to reproduce or a proof-of-concept
- The version or commit affected

## Security Considerations

- The Jira API token is read from the `JIRA_TOKEN` environment variable and is masked in command output
- HTTPS is required for non-loopback Jira URLs; an explicit `http://` scheme is rejected so the Bearer token is never sent in plaintext
- HTTPS→HTTP redirects are refused, and any credentials embedded in the Jira URL (userinfo) are stripped
- The optional `JIRA_ALLOWED_HOSTS` environment variable restricts which Jira hostnames the client may contact
- For a Jira URL whose host is not `*.atlassian.net`, every command first sends one unauthenticated `GET /rest/api/2/serverInfo` to that host to detect the platform: no credentials, no cookies, redirects not followed, 5 s timeout, response capped at 64 KiB
- Credentials follow the `baseUrl` reported by serverInfo only when it is a validated `https://<one-label>.atlassian.net` origin (no userinfo, port 443 only, no path, query or fragment, no punycode label). This is a host-shape check, not a tenant-ownership check: anyone can register an `*.atlassian.net` site
- To pin the tenant, set `JIRA_ALLOWED_HOSTS` to include both the configured host and the derived `*.atlassian.net` host. A derived host outside that list, or a Cloud site that advertises no usable `*.atlassian.net` address, fails closed before any credential is sent, and every command prints the real API target
- API response bodies are capped (10 MiB) to prevent memory exhaustion
- The GitHub Copilot AI provider runs with a deny-all permission handler — no filesystem, shell, or network access is granted to the agent
