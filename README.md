# Project Brain MCP

A Model Context Protocol (MCP) server that gives AI assistants **project-scoped knowledge** backed by a GitHub repository. Your project's docs, rules, and conventions live as markdown files in a repo — the model reads them through five purpose-built tools.

No mass GitHub API surface, no unintended writes: the binary is hard-locked to knowledge-base tools only.

---

## What it does

You point this server at **one GitHub repo** (your project's repo or a dedicated KB repo). The server then exposes five tools to your AI client:

| Tool | What it does |
|---|---|
| `kb_config` | Reads `AGENTS.md` at the repo root — defines the KB structure, rules, and documented paths. Call this first in every session. |
| `kb_list` | Lists documents available in the KB. |
| `kb_read` | Fetches a specific document. |
| `kb_search` | Full-text search across the KB. |
| `kb_write` | Creates or updates a KB document (writes a commit to the repo). |

That's the whole tool surface. No `create_pull_request`, no `list_issues`, no `search_code` — if a model tries to call anything else, it's not there.

---

## Install

### Windows (PowerShell)

```powershell
iwr -useb https://neusis-ai-org.github.io/project-brain-mcp/install.ps1 | iex
```

### macOS / Linux

```bash
curl -fsSL https://neusis-ai-org.github.io/project-brain-mcp/install.sh | bash
```

Both scripts auto-detect your architecture, download the matching release from GitHub, and drop `mcp-project-brain` on your PATH. Restart your terminal when it finishes.

**Verify:**
```
mcp-project-brain --version
```

### Manual install

Download the archive for your OS/arch from the [latest release](https://github.com/Neusis-AI-Org/project-brain-mcp/releases/latest), extract, and place `mcp-project-brain` somewhere on your PATH.

---

## Set up your GitHub token

The server needs a GitHub Personal Access Token with access to **only the repo you're targeting**.

1. Go to https://github.com/settings/personal-access-tokens/new
2. Choose **Fine-grained**, set **Repository access** → *Only select repositories* → pick your KB repo
3. Under **Repository permissions** set:
   - **Contents** → Read and write (read-only works if you never use `kb_write`)
   - **Metadata** → Read-only (auto)
4. Generate, copy, and set the env var:

**Windows:**
```powershell
setx GITHUB_PERSONAL_ACCESS_TOKEN "github_pat_..."
```

**macOS / Linux:**
```bash
echo 'export GITHUB_PERSONAL_ACCESS_TOKEN="github_pat_..."' >> ~/.zshrc  # or ~/.bashrc
```

Restart your terminal.

---

## Configure your MCP client

**Replace `YOUR-ORG/YOUR-REPO` with the repo you want the assistant to operate on.** The `--kb-repo` value is required — it tells the server which repo to operate on and the assistant is informed about this repo in its session instructions.

### neusiscode / OpenCode

File: `neusiscode.json` (or `opencode.json`) at the project root.

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "project-brain": {
      "type": "local",
      "command": [
        "mcp-project-brain",
        "stdio",
        "--kb-repo=YOUR-ORG/YOUR-REPO"
      ],
      "enabled": true,
      "environment": {
        "GITHUB_PERSONAL_ACCESS_TOKEN": "{env:GITHUB_PERSONAL_ACCESS_TOKEN}"
      }
    }
  }
}
```

> A ready-to-edit `neusiscode.json` is shipped in the repo root.

### Claude Code / Cursor / generic MCP

File: `.mcp.json` (Claude Code) or `.cursor/mcp.json` (Cursor) at the project root.

```json
{
  "mcpServers": {
    "project-brain": {
      "command": "mcp-project-brain",
      "args": [
        "stdio",
        "--kb-repo=YOUR-ORG/YOUR-REPO"
      ],
      "env": {
        "GITHUB_PERSONAL_ACCESS_TOKEN": "${GITHUB_PERSONAL_ACCESS_TOKEN}"
      }
    }
  }
}
```

---

## Set up your knowledge base

The server reads an `AGENTS.md` file from the root of the target repo. This file describes your KB layout. A minimal example:

```markdown
# Agent Instructions

This repository is the knowledge base for the XYZ project.

## Rules
- All documentation lives under `docs/`.
- Decision records live under `decisions/NNNN-*.md`.
- When updating docs, always commit with a clear message.

## Paths
- `docs/architecture/` — system design and diagrams
- `docs/runbooks/` — operational procedures
- `decisions/` — architectural decision records
```

Commit this file to the root of your target repo. The assistant will call `kb_config` automatically at the start of a session to learn this layout.

---

## Verify it works

After restarting your MCP client:

1. Ask the assistant: *"Call kb_config to see what this project is about."*
2. You should see the contents of your `AGENTS.md` come back, and the assistant will now operate with that context.

---

## Troubleshooting

**`--kb-repo is required`** → You didn't pass `--kb-repo` in the args. Edit your MCP config.

**`GITHUB_PERSONAL_ACCESS_TOKEN not set`** → Env var is missing. Set it and restart your terminal *and* your MCP client.

**`AGENTS.md not found in repository`** → Create one at the root of your target repo. See the example above.

**401 / 403 from GitHub** → Your PAT doesn't have access to the repo. Regenerate with the right repository selection and permissions.

---

## License

MIT — see [LICENSE](./LICENSE).

This project is a fork of [github/github-mcp-server](https://github.com/github/github-mcp-server), rebuilt around a single purpose: giving AI assistants curated, repo-backed knowledge without any of the broader GitHub tool surface.
