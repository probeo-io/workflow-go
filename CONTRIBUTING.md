# Contributing to workflow-go

Thanks for your interest in contributing! Here's how to get started.

## Setup

```bash
git clone https://github.com/probeo-io/workflow-go.git
cd workflow-go
go mod download
```

## Development

```bash
# Run tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Run a specific test
go test -run TestWorkflowRun ./...
```

## Project Structure

```
workflow.go       # Workflow engine, Run(), RunOne()
step.go           # Step interface and step modes
store.go          # FileStore persistence
workflow_test.go  # Workflow tests
store_test.go     # Store tests
```

## Making Changes

1. Fork the repo and create a branch from `main`
2. Make your changes
3. Add or update tests as needed
4. Run `go test ./...` to make sure everything passes
5. Write a clear commit message describing what and why
6. Open a pull request

## Pull Requests

- Keep PRs focused. One feature or fix per PR
- Include tests for new functionality
- Update the README if you're adding user-facing features
- Make sure CI passes before requesting review

## Reporting Issues

Use [GitHub Issues](https://github.com/probeo-io/workflow-go/issues). Include:

- What you expected to happen
- What actually happened
- Steps to reproduce
- Go version and OS

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- No external runtime dependencies (stdlib only)
- Keep things simple. No premature abstractions
