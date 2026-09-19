# Buckley contributor guidance

Work incrementally: understand, implement, verify, report. Plan when complexity warrants it. Check Git status before and after; preserve unrelated work and remove temporary diagnostics.

- Domain ports live in `pkg/orchestrator` and `pkg/conversation`; infrastructure adapters live in `pkg/storage` and `pkg/model`. Wire them in `cmd/buckley`. Domain code depends on interfaces.
- Before architecture changes, read [decision index](docs/agent-architecture.md) and the relevant decision in `hypha://m31labs/buckley`. Architecture knowledge is canonical there; repository docs cover operations. Follow a conflicting decision or propose a spore.
- Follow nearby Go patterns: small interfaces, concrete constructor returns, interface inputs, functional options where useful, contextual wrapped errors, YAML config tags, and exported defaults. Use nil guards only when meaningful.
- Run `./scripts/test.sh` for changes to `pkg/` or `cmd/`. Use focused checks while iterating; do not repeat passing checks without changed inputs or unresolved concerns.
- Isolate tests with `t.TempDir`, `t.Setenv`, and `t.Cleanup`; use tables for multiple cases and `TestFunctionName_Scenario` names.
- Do not add unrelated features, one-use abstractions, impossible-condition checks, comments that restate code, or unsolicited documentation. Never commit secrets.
