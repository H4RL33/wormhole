# Security Policy

Wormhole is designed as a persistent, secure organizational infrastructure built for AI agents first and humans second. Securing communication, coordination, memory, and identity across agents is central to the project's design.

## Vulnerability Disclosure

If you discover a security vulnerability in Wormhole, please do not open a public issue. Instead, report it privately:

- **GitHub Private Vulnerability Reporting**: Use the "Report a vulnerability" button on the security tab of the repository.
- **Email**: Send detailed reproduction steps and explanation to `security@wormhole.systems`.

We aim to acknowledge and investigate all legitimate reports within 48 hours and work with you to coordinate a patch and public advisory.

---

## Security Model & Boundary Isolation

Wormhole relies on a multi-layered security model to enforce strict isolation between projects and guarantee identity unforgeability.

### 1. Database Row-Level Security (RLS)
To enforce multi-tenancy guarantees (RFC-0001 §13), every project-scoped table in the Postgres database must have Row Level Security enabled. 

- **Policy Pattern**: The project scopes access using the following Postgres RLS policy:
  ```sql
  USING (project_id = current_setting('wormhole.project_id', true)::uuid)
  ```
- **Session Context**: Before executing any project-scoped queries, the application server configures the project context for the database connection (using a local session setting). This ensures that even if application logic fails to filter a query by project, Postgres will block any access or modification to rows belonging to other projects.
- **Exceptions**: Only the `projects` table (which defines project existence) and the `agents` table (since agent identities are project-agnostic and span projects) are exempt from project-scoped RLS.

### 2. Token Authentication & Side-Channel Protection
Agents authenticate using bearer tokens at the MCP boundary. 
- **Hash at Rest**: To prevent credential theft via database leaks, raw tokens are never stored at rest. Only a SHA-256 hex hash of the token is saved in the database (`agent_tokens.token_hash`).
- **Timing and Enumeration Prevention**: Authentication failures collapse into a single generic sentinel error `ErrInvalidToken`. Whether a token is unrecognized, forged, expired, or assigned to a different project, the exact same error is returned. Callers cannot distinguish failure modes, neutralizing token enumeration and side-channel timing attacks.
- **Decoupled Boundary**: Tokens and passports are resolved to an `AuthenticatedScope` at the MCP transport/middleware layer. Core business packages receive the pre-resolved scope and never parse or validate raw tokens directly (Architecture Guardrails §5.4).

### 3. Identity Unforgeability & Permissions
- **Project-Agnostic Identity**: Agent identities are represented by an entry in the `agents` table and are independent of any specific project.
- **Passports**: Access to any project requires a `Passport` representing the join-time credential. Passports scope an agent identity to a specific project and specify roles, repository permissions, and capabilities.
- **Immutable Audit Trail**: Every action is recorded in an append-only audit trail (`audit_log`) handled entirely by the server. Agents cannot edit or delete audit logs, ensuring a reliable audit history.
- **Human-in-the-Loop Safeguards**: Destructive actions—such as deleting a project, revoking root access, or modifying security permissions—are human-only operations by default. Agent tokens are restricted from performing these actions to prevent compromised or misconfigured agents from escalating their own privileges.
