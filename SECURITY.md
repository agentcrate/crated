# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| latest  | Yes       |

## Reporting a Vulnerability

We take security seriously. If you discover a security vulnerability in crated, please report it responsibly.

### How to Report

**Do NOT open a public GitHub issue for security vulnerabilities.**

Instead, please email: **security@agentcrate.ai**

Include as much of the following information as possible:

- Type of vulnerability (e.g., injection, privilege escalation, supply chain)
- Full path to the affected source file(s)
- Step-by-step instructions to reproduce
- Impact assessment
- Suggested fix, if any

### Response Timeline

- **Acknowledgment**: Within 48 hours
- **Assessment**: Within 5 business days
- **Fix timeline**: Depends on severity
  - Critical: Patch within 72 hours
  - High: Patch within 1 week
  - Medium/Low: Next scheduled release

### Scope

The following are in scope:

- The `crated` runtime binary
- Model provider registry and initialization
- MCP skill connection handling
- Container entrypoint behavior
- Signal handling and process lifecycle

The following are out of scope for this repository:

- Agentfile parsing (report to agentfile's security process)
- CrateHub (report to CrateHub's security process)
- Third-party MCP servers
- Docker Engine vulnerabilities
- LLM provider APIs

## Security Design Principles

- **Zero secrets in artifacts**: Agentfiles must never contain plaintext secrets
- **Minimal privileges**: Docker containers run with least-privilege policies
- **Signal handling**: Proper PID 1 behavior with tini for graceful shutdown
- **Dependency scanning**: Dependencies are regularly audited for vulnerabilities
