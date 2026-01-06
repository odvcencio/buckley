## Contributing to Buckley

Thanks for investing time to improve Buckley! Please read this guide before opening issues or PRs.

### Ground Rules
- Follow the local conventions in `AGENTS.md`; it is the source of truth for workflow and coding norms.
- Keep the tree clean: do not revert user changes, and prefer small, reviewable PRs.
- Add concise comments only when behavior is non-obvious.
- Run formatters (`gofmt`/`go fmt`) on Go changes and equivalent tooling for other languages.

### Development Workflow
1. Clone the repo and install Go 1.25.1+.
2. Export a model provider key (e.g., `OPENROUTER_API_KEY`) for end-to-end runs.
3. Make your changes in a feature branch; include tests where behavior changes.
4. Run the fast suite before opening a PR:
   ```bash
   ./scripts/test.sh
   ```
   Use `GO_TEST_TARGET=all ./scripts/test.sh` or `go test ./...` for broader coverage when needed.
5. Open a PR with a clear description and screenshots/log snippets for UI or UX-affecting changes.

### Issue & PR Guidance
- Bugs: include reproduction steps, expected/actual behavior, environment (OS, Go version), and logs if available.
- Features: describe the problem first, then the proposed solution and alternatives considered.
- Security issues: **do not** open a public issue; follow `SECURITY.md`.

### High-Priority Areas
- Test coverage needs the most help in: `pkg/model`, `pkg/tool`, `pkg/ui`, `pkg/session`, and `pkg/cost`.
- Please prefer small, focused PRs that add tests around existing behavior before refactors.

### Code of Conduct
Be respectful and collaborative. Assume good intent and keep discussions focused on the work. See `CODE_OF_CONDUCT.md`.
