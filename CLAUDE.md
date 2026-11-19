# Guide for AI assistants in this repo

This project is **also a learning project** for its owner. The goal is code that can be understood, not code that looks clever.

Read before writing code: [docs/principles.md](docs/principles.md), [docs/architecture.md](docs/architecture.md), [docs/protocol.md](docs/protocol.md).

## Hard rules
- Work only on the active milestone in [docs/roadmap.md](docs/roadmap.md). Don't add features "while we're at it".
- Stdlib first. A new dependency needs the user's approval and an entry in [docs/decisions.md](docs/decisions.md).
- Don't create an `interface`, layers (handler/service/repository), generics, or a new package without a real need right now.
- Never `exec.Command("sh", "-c", <string built from input>)`. Always pass arguments separately.
- Every endpoint (except login and agent enroll) must check authentication. Every WebSocket from a browser must check `Origin`.
- A protocol change means `docs/protocol.md` is updated in the same commit.
- When you make a non-trivial design decision, add an entry to `docs/decisions.md` and explain it briefly to the user.

## Style
- Everything is in English: code, comments, docs, commit messages.
- Comments explain **why**, not **what**.
- After writing code, briefly explain the flow and the important decisions to the user.
