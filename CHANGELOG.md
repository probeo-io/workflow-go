# Changelog

## Unreleased

- Initial Go port of @probeo/workflow
- Stage-based pipeline engine with concurrent and collective step modes
- FileStore for filesystem persistence with immutable write-once outputs
- Resume-after-crash via cached step outputs
- Retry with exponential backoff
- Context-based cancellation
- Resource injection
- Progress callbacks
- Zero external dependencies (stdlib only)
