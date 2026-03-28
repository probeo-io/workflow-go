# Changelog

## [0.1.0] - 2026-03-28

- Initial Go port of @probeo/workflow
- Stage-based pipeline engine with concurrent and collective step modes
- FileStore for filesystem persistence with immutable write-once outputs
- Resume-after-crash via cached step outputs
- Retry with exponential backoff
- Context-based cancellation
- Resource injection
- Progress callbacks
- Zero external dependencies (stdlib only)
- Expanded test suite (31 tests: workflow, store)
- "See Also" cross-links to related packages
- GitHub Actions CI (Go 1.22-1.26 matrix)
