# BOOTSTRAP.md — How To Start Claude Code In This Repo

> This file exists once: at the very start, before any code. After phase 1 begins, this file can be deleted or kept as historical context. Your call.

---

## Step-By-Step For The Human

### 1. Prerequisites on your machine

- **Windows 11 + WSL2 + Docker Desktop** (you have this).
- **Go 1.23+** — install via `winget install GoLang.Go` or download from go.dev. Not strictly required (Docker has Go) but you want it for IDE auto-complete.
- **Git** — already installed.
- **Claude Code** — install per Anthropic docs: https://docs.claude.com/en/docs/claude-code.
- **GitHub CLI** (`gh`) — optional but useful for creating PRs from terminal.

### 2. Create the GitHub repository

1. Go to https://github.com/new
2. Name: `notification-system` (or whatever you prefer — update README references accordingly).
3. Visibility: **Public**.
4. Initialize with: nothing (no README, no .gitignore, no license — we provide our own).
5. Click "Create repository".

### 3. Clone and seed the repo

```bash
# In WSL2 terminal, in a directory where you keep projects
git clone https://github.com/<your-username>/notification-system.git
cd notification-system

# Copy the three bootstrap files I gave you into this directory:
#   CLAUDE.md
#   PLAN.md
#   BOOTSTRAP.md (this file)
# Also copy the brief PDF into docs/brief.pdf (create the directory first)

mkdir -p docs
cp /path/to/Software_Engineer_Assessment_-_Golang.pdf docs/brief.pdf

# Initial commit
git add CLAUDE.md PLAN.md BOOTSTRAP.md docs/brief.pdf
git commit -m "docs: add project operating manual, roadmap, and brief"
git push origin main
```

### 4. Start Claude Code in this directory

```bash
cd notification-system
claude
```

### 5. Paste the bootstrap prompt below as your first message

---

## The Bootstrap Prompt

Copy everything between the `=== START ===` and `=== END ===` markers and paste it as your first message to Claude Code.

```
=== START ===

You are working on a technical assessment submission. Read these three files first, in this order, in full:

1. CLAUDE.md — the project's operating manual. This is your constitution. Follow it.
2. PLAN.md — the phase-by-phase roadmap. We are starting at Phase 1.
3. docs/brief.pdf — the original assessment brief. Use this for context on requirements.

After reading, confirm to me in 5–10 lines:

- Your understanding of the project mission in one sentence.
- The architectural style we are using and why.
- The current phase and what its entry/exit criteria are.
- The commit message format we are using.
- Who runs `git` commands.

Then wait for me to say "go" before beginning Phase 1. Do not write any code or modify any files until I confirm.

When you do begin Phase 1, work one task at a time as listed in PLAN.md. For each task:

1. Tell me what you are about to do.
2. Make the file changes (using your file tools).
3. Run `make lint` and `make test` if applicable, and show the output.
4. Surface the commit message and the exact `git` commands I should run.
5. Wait for me to confirm before moving to the next task.

If at any point you would deviate from CLAUDE.md, PLAN.md, or the brief — stop and ask first.

If you are uncertain about a decision that has not been resolved in CLAUDE.md, PLAN.md, or the brief — stop and ask first.

The brief sentence that drives every decision: "Insider One needs to send millions of notifications daily to users across different channels. The system must handle burst traffic (e.g., flash sales, breaking news), retry failed deliveries intelligently, and provide visibility into delivery status for both internal teams and API consumers."

When in doubt, return to that sentence.

=== END ===
```

---

## What To Expect From The First Session

Claude Code should:

1. Read all three files plus the brief.
2. Reply with the 5–10 line confirmation summary as requested.
3. Wait for your "go".
4. Begin Phase 1, Task 1 (`chore: add gitignore, editorconfig, license`).
5. Show you the file contents it intends to create.
6. Give you a `git add` + `git commit` command sequence.
7. Wait for you to run them.

If Claude Code starts writing code before confirming the summary, **stop it**. Re-paste the bootstrap prompt with emphasis on the "wait for me to say go" line.

---

## Branching Workflow Per Phase

For each phase:

```bash
# At the start of a phase
git checkout main
git pull origin main
git checkout -b feat/phase-N-short-description   # branch name in PLAN.md

# During the phase, after each task Claude Code finishes
git add <files>
git commit -m "<message-from-claude>"

# When the phase is done
git push origin feat/phase-N-short-description
gh pr create --base main --title "<title-from-plan>" --body "Closes Phase N. See PLAN.md."
gh pr merge --merge   # use merge commit, not squash, to preserve TDD history

# Move to next phase
git checkout main
git pull origin main
git checkout -b feat/phase-(N+1)-short-description
```

---

## When Claude Code Goes Off-Track

Symptoms and remedies:

- **Skipping tests and writing implementation directly:** Reply with "stop — TDD. Test first, then implementation, separate commits."
- **Adding features not in PLAN.md:** Reply with "that is not in the current phase — defer it or write a `// FUTURE:` comment."
- **Trying to run `git commit` itself:** Reply with "I run all git commands. Surface the message and command sequence; I will execute."
- **Making decisions without asking:** Reply with "this needs a decision. What are the options and your recommendation? I will decide."
- **Refactoring while implementing:** Reply with "implementation and refactor are separate commits. Finish implementation first, surface the commit, then propose the refactor separately."

The repeated theme: keep Claude Code on a short leash for the first few tasks until you trust its rhythm. Then loosen.

---

## Mid-Session Recovery

If Claude Code crashes, the session ends, or you start a new session for any reason, the first message of the new session should be:

```
=== START RESUME ===

Continuing work on the notification-system project. Read CLAUDE.md and PLAN.md in full to refresh context.

Then check git log and the current branch to determine which phase and which task within the phase we are on.

Tell me what you found and which task is next, then wait for me to say "go".

=== END RESUME ===
```

This gets Claude Code re-oriented without restarting from Phase 1.

---

## Reminder: No `.env` File

This project deliberately has **no `.env` file**. All environment variables live in `docker-compose.yml` with working defaults. If Claude Code ever proposes creating a `.env` file or `.env.example`, refuse it. This is documented in CLAUDE.md §2.7 and §13.4 (last bullet) and in ADR-0010. The quickstart is literally `docker compose up -d` with no setup file dance.

---

## Good luck. Build something worth shipping.
