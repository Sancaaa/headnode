# Principles & Ground Rules

This project is built to be **used** and to be **understood**. When those two goals conflict, choose the one that is easier to understand, as long as it stays secure.

## 1. Design principles

| Principle | What it means here |
|---|---|
| **YAGNI** (*You Aren't Gonna Need It*) | Build only what the active milestone needs. No "let's prepare this for later". |
| **KISS** (*Keep It Simple*) | A solution you can explain in two sentences beats a "more elegant" one. |
| **Rule of three** | Similar code may be written twice. Extract a function or abstraction only when it shows up a third time. |
| **Stdlib first** | A third-party library only when the stdlib clearly isn't enough, and it must be recorded in `decisions.md`. |
| **Boring technology** | Pick mature technology with plenty of documentation. |
| **Make it work → right → fast** | Get it working, then clean it up, then optimize only if it is proven slow. |
| **Secure from the start** | Security is **not** covered by YAGNI. Auth, Origin checks and separate exec arguments exist from day one. |

## 2. Things to avoid (unless truly forced)

- An `interface` without at least two real implementations. (Exception: ones the stdlib already provides, like `io.Reader`.)
- Enterprise layering: `handler → service → repository → model` for logic that fits in one function.
- Dependency injection containers, event buses, plugin systems, generic utility packages (`utils/`, `helpers/`, `common/`).
- A new package just to "tidy up". One package may have many files.
- Config through YAML/TOML files. Flags and environment variables are enough.
- Goroutines and channels without a reason. If `sync.Mutex` + a map is enough, use that.

## 3. Go code style

- `gofmt` and `go vet` must be clean. `staticcheck` is recommended.
- Wrap errors with context: `fmt.Errorf("enroll agent: %w", err)`. Never silently ignore one, unless a comment explains why it's safe.
- Log with `log/slog`, structured: `slog.Info("agent connected", "agent_id", id)`.
- Short functions are good, but a 60-line function you read top to bottom beats 6 tiny functions you have to jump between.
- Comments explain **why**. A good example: `// write to .part first so a half-written file never appears at the target path`.
- Language: English for everything (`D-14`).

## 4. Testing

- Write tests for logic that is **easy to get wrong and costly when wrong**: protocol parsing, `can()` (permissions), path and token validation, session pairing in the relay.
- Use stdlib `testing` + `httptest`. No assertion or mocking frameworks.
- Don't chase a coverage number.
- End-to-end testing is manual, following the "Done when" criteria in the [roadmap](roadmap.md).

## 5. Git

- `main` always builds.
- One commit = one change that makes sense on its own. Commit messages in English, short imperative: `add agent heartbeat`, `fix relay timeout`.
- Protocol changes are committed together with the `docs/protocol.md` update.
- New design decisions are recorded in `docs/decisions.md` in the same commit.

## 6. How to learn from this repo

Suggested reading order:
1. [architecture.md](architecture.md) §3: the two kinds of connection (the most important concept).
2. `server/relay.go`: how two WebSockets get joined.
3. `agent/conn.go`: dial, hello, heartbeat, reconnect.
4. `agent/shell.go`: what a PTY is and why a terminal needs one.
5. `server/auth.go`: cookie sessions, bcrypt, `can()`.

If there's a piece of code you can't explain to someone else, that's a sign the code needs to be simpler or better commented, not a sign that you aren't smart enough.
