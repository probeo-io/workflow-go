# workflow-go

Stage-based pipeline engine for AI workloads. Zero dependencies. Code decides what happens. AI does the work.

Most AI pipelines are linear: fetch data, analyze it, enrich it, summarize it. Some stages call LLMs. Some don't. The control flow is deterministic even when AI is involved. You don't need a graph framework for that.

workflow-go gives you per-item concurrency, retry with backoff, filesystem persistence, and resume-after-crash. No graph theory. No dependency tree. No framework lock-in.

## Install

```bash
go get github.com/probeo-io/workflow-go
```

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"io"

	workflow "github.com/probeo-io/workflow-go"
	anymodel "github.com/probeo-io/anymodel-go"
)

type analyzeStep struct{}

func (s *analyzeStep) Name() string              { return "analyze" }
func (s *analyzeStep) Mode() workflow.StepMode    { return workflow.Concurrent }

func (s *analyzeStep) Run(input any, ctx *workflow.StepContext) (any, error) {
	url := input.(string)
	html, err := fetchPage(url)
	if err != nil {
		return nil, err
	}
	ai := ctx.Resources["ai"].(*anymodel.Client)
	resp, err := ai.Chat(ctx.Ctx, &anymodel.ChatRequest{
		Model: "anthropic/claude-sonnet-4-20250514",
		Messages: []anymodel.Message{
			{Role: "user", Content: fmt.Sprintf("Summarize this page:\n\n%s", html[:min(len(html), 5000)])},
		},
	})
	if err != nil {
		return nil, err
	}
	return map[string]string{"title": url, "summary": resp.Message.Content}, nil
}

func fetchPage(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func main() {
	ai := anymodel.New(&anymodel.Config{APIKey: "your-key"})

	wf := workflow.New(&workflow.WorkflowConfig{BaseDir: ".pipeline"})
	wf.Resource("ai", ai).Step(&analyzeStep{})

	items := []workflow.WorkItem{
		{ID: "page-0", Data: "https://example.com"},
		{ID: "page-1", Data: "https://example.com/about"},
	}

	results, err := wf.Run(context.Background(), items, &workflow.Options{
		Concurrency: 10,
		MaxRetries:  2,
	})
	if err != nil {
		panic(err)
	}
	for _, r := range results {
		fmt.Printf("%s: %s\n", r.ID, r.Status)
	}
}
```

## Use Cases

### Content pipeline with anymodel-go

Crawl pages, extract content with an LLM, generate SEO metadata. Each stage is a step. LLM calls happen inside steps, not as routing decisions.

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"

	workflow "github.com/probeo-io/workflow-go"
	anymodel "github.com/probeo-io/anymodel-go"
)

// crawlStep fetches a URL and returns the HTML.
type crawlStep struct{}

func (s *crawlStep) Name() string           { return "crawl" }
func (s *crawlStep) Mode() workflow.StepMode { return workflow.Concurrent }

func (s *crawlStep) Run(input any, ctx *workflow.StepContext) (any, error) {
	url := input.(string)
	html, err := fetchPage(url)
	if err != nil {
		return nil, err
	}
	return map[string]string{"url": url, "html": html}, nil
}

// extractStep uses an LLM to extract structured content from HTML.
type extractStep struct{}

func (s *extractStep) Name() string           { return "extract" }
func (s *extractStep) Mode() workflow.StepMode { return workflow.Concurrent }

func (s *extractStep) Run(input any, ctx *workflow.StepContext) (any, error) {
	page := input.(map[string]any)
	ai := ctx.Resources["ai"].(*anymodel.Client)
	html := page["html"].(string)
	resp, err := ai.Chat(ctx.Ctx, &anymodel.ChatRequest{
		Model: "anthropic/claude-sonnet-4-20250514",
		Messages: []anymodel.Message{
			{Role: "user", Content: fmt.Sprintf(
				"Extract the main content from this HTML. Return JSON with title, description, and bodyText.\n\n%s",
				html[:min(len(html), 8000)],
			)},
		},
	})
	if err != nil {
		return nil, err
	}
	var content map[string]any
	json.Unmarshal([]byte(resp.Message.Content), &content)
	page["content"] = content
	return page, nil
}

// generateMetaStep produces SEO metadata using a lighter model.
type generateMetaStep struct{}

func (s *generateMetaStep) Name() string           { return "generate-meta" }
func (s *generateMetaStep) Mode() workflow.StepMode { return workflow.Concurrent }

func (s *generateMetaStep) Run(input any, ctx *workflow.StepContext) (any, error) {
	page := input.(map[string]any)
	ai := ctx.Resources["ai"].(*anymodel.Client)
	content := page["content"].(map[string]any)
	bodyText := content["bodyText"].(string)
	resp, err := ai.Chat(ctx.Ctx, &anymodel.ChatRequest{
		Model: "openai/gpt-4o-mini",
		Messages: []anymodel.Message{
			{Role: "user", Content: fmt.Sprintf(
				"Write an SEO meta description (under 160 chars) for this content:\n\nTitle: %s\n\n%s",
				content["title"], bodyText[:min(len(bodyText), 2000)],
			)},
		},
	})
	if err != nil {
		return nil, err
	}
	page["meta"] = resp.Message.Content
	return page, nil
}

func main() {
	ai := anymodel.New(&anymodel.Config{APIKey: "your-key"})

	wf := workflow.New(&workflow.WorkflowConfig{BaseDir: ".content-pipeline"})
	wf.Resource("ai", ai).
		Step(&crawlStep{}).
		Step(&extractStep{}).
		Step(&generateMetaStep{})

	urls := []string{"https://example.com", "https://example.com/about"}
	items := make([]workflow.WorkItem, len(urls))
	for i, u := range urls {
		items[i] = workflow.WorkItem{ID: fmt.Sprintf("page-%d", i), Data: u}
	}

	results, err := wf.Run(context.Background(), items, &workflow.Options{
		Concurrency: 5,
		MaxRetries:  2,
		OnProgress: func(p workflow.WorkflowProgress) {
			fmt.Printf("%d/%d (%s)\n", p.Completed, p.Total, p.CurrentStep)
		},
	})
	if err != nil {
		panic(err)
	}
	for _, r := range results {
		fmt.Printf("%s: %s\n", r.ID, r.Status)
	}
}
```

### Research pipeline with anyserp-go + anymodel-go

Search the web for a topic, fetch top results, analyze them with an LLM, then produce a collective summary.

```go
// searchStep searches the web for a query.
type searchStep struct{}

func (s *searchStep) Name() string           { return "search" }
func (s *searchStep) Mode() workflow.StepMode { return workflow.Concurrent }

func (s *searchStep) Run(input any, ctx *workflow.StepContext) (any, error) {
	query := input.(string)
	serp := ctx.Resources["serp"].(*anyserp.Client)
	results, err := serp.Search(ctx.Ctx, &anyserp.SearchRequest{Query: query, Num: 5})
	if err != nil {
		return nil, err
	}
	return map[string]any{"query": query, "results": results.Organic}, nil
}

// analyzeStep uses an LLM to analyze search results.
type analyzeSearchStep struct{}

func (s *analyzeSearchStep) Name() string           { return "analyze" }
func (s *analyzeSearchStep) Mode() workflow.StepMode { return workflow.Concurrent }

func (s *analyzeSearchStep) Run(input any, ctx *workflow.StepContext) (any, error) {
	data := input.(map[string]any)
	ai := ctx.Resources["ai"].(*anymodel.Client)
	results := data["results"].([]any)
	snippets := formatSnippets(results)
	resp, err := ai.Chat(ctx.Ctx, &anymodel.ChatRequest{
		Model: "anthropic/claude-sonnet-4-20250514",
		Messages: []anymodel.Message{
			{Role: "user", Content: fmt.Sprintf(
				"Analyze these search results for %q. Key themes and findings?\n\n%s",
				data["query"], snippets,
			)},
		},
	})
	if err != nil {
		return nil, err
	}
	data["analysis"] = resp.Message.Content
	return data, nil
}

// summarizeStep produces a collective summary from all analyses.
type summarizeStep struct{}

func (s *summarizeStep) Name() string           { return "summarize" }
func (s *summarizeStep) Mode() workflow.StepMode { return workflow.Collective }

func (s *summarizeStep) Run(input any, ctx *workflow.StepContext) (any, error) {
	items := input.([]workflow.CollectiveItem)
	ai := ctx.Resources["ai"].(*anymodel.Client)
	analyses := formatAnalyses(items)
	resp, err := ai.Chat(ctx.Ctx, &anymodel.ChatRequest{
		Model: "anthropic/claude-sonnet-4-20250514",
		Messages: []anymodel.Message{
			{Role: "user", Content: fmt.Sprintf(
				"Synthesize these research analyses into a single brief:\n\n%s", analyses,
			)},
		},
	})
	if err != nil {
		return nil, err
	}
	result := make([]any, len(items))
	for i, item := range items {
		result[i] = map[string]string{"id": item.ID, "summary": resp.Message.Content}
	}
	return result, nil
}
```

### Batch processing with resume

Process 10,000 items. If it crashes at item 6,000, restart and it picks up where it left off. FileStore writes are immutable. Completed steps are never re-run.

```go
items := make([]workflow.WorkItem, 10000)
for i := range items {
	items[i] = workflow.WorkItem{ID: fmt.Sprintf("item-%d", i), Data: i}
}

wf := workflow.New(&workflow.WorkflowConfig{
	ID:      "batch-2026-03-28", // Fixed ID enables resume
	BaseDir: ".batch-data",
})
wf.Step(&fetchStep{}).Step(&transformStep{}).Step(&enrichStep{})

// First run: processes all 10,000
wf.Run(context.Background(), items, &workflow.Options{Concurrency: 20})

// Crashes at item 6,000. Restart:
// Items 1-6,000 are skipped (outputs exist on disk).
// Items 6,001-10,000 are processed.
wf.Run(context.Background(), items, &workflow.Options{Concurrency: 20})
```

## Concepts

- **Step** -- a named unit of work with a `Run()` method. Each step declares a `Mode()`:
  - `Concurrent` -- runs per-item, many in parallel (up to the concurrency limit)
  - `Collective` -- waits for all items, runs once with the full set (useful for summarization, aggregation)
- **Workflow** -- chains steps together. Supports resource injection, progress callbacks, and context cancellation.
- **FileStore** -- filesystem persistence with immutable write-once outputs. Enables resume after crash.

## API

### `workflow.New(cfg *WorkflowConfig) *Workflow`

| Field | Type | Default | Description |
|---|---|---|---|
| `ID` | `string` | auto-generated | Workflow run identifier. Use a fixed ID to enable resume. |
| `Store` | `Store` | `FileStore` | Persistence backend |
| `BaseDir` | `string` | `".workflow"` | Base directory for `FileStore` |

### `.Resource(name string, value any) *Workflow`

Register a shared resource available to all steps via `ctx.Resources`.

### `.Step(step Step) *Workflow`

Add a step to the pipeline. Steps run in the order they are added.

### `.Run(ctx context.Context, items []WorkItem, opts *Options) ([]*ItemState, error)`

Run all items through the pipeline. Returns a slice of `*ItemState`.

| Field | Type | Default | Description |
|---|---|---|---|
| `Concurrency` | `int` | `5` | Max parallel items for concurrent steps |
| `MaxRetries` | `int` | `2` | Retry attempts per item per step (exponential backoff) |
| `OnProgress` | `func(WorkflowProgress)` | -- | Progress callback |

Cancellation is handled via `context.Context` (the first argument).

### `.RunOne(ctx context.Context, item WorkItem, opts *Options) (*ItemState, error)`

Run a single item through the full pipeline. Returns one `*ItemState`.

### `Step` interface

```go
type Step interface {
    Name() string
    Mode() StepMode
    Run(input any, ctx *StepContext) (any, error)
}
```

### `StepContext`

Every step receives a context:

| Field | Type | Description |
|---|---|---|
| `ItemID` | `string` | Current item's ID |
| `Resources` | `map[string]any` | Shared resources registered via `.Resource()` |
| `Log` | `Logger` | Logger scoped to this item + step |
| `GetCache` | `func(string) (any, error)` | Read a previous step's cached output |
| `Ctx` | `context.Context` | Context for cancellation |

### `FileStore`

Filesystem-backed store. Step outputs are immutable. Once written, never overwritten. Safe to resume after interruption.

```go
store := workflow.NewFileStore(".my-pipeline")
```

### `Store` interface

```go
type Store interface {
    SaveItem(workflowID string, item *ItemState) error
    GetItem(workflowID, itemID string) (*ItemState, error)
    ListItems(workflowID string) ([]*ItemState, error)
    SaveStepOutput(workflowID, itemID, stepName string, output any) error
    GetStepOutput(workflowID, itemID, stepName string) (any, error)
}
```

## Why not Temporal / Hatchet / go-workflows?

Those are distributed workflow orchestration frameworks. They require infrastructure: task queues, databases, workers, retry policies defined in YAML. They solve a real problem when you need distributed, durable execution across services.

But most AI workloads are pipelines running on a single machine. Items flow through stages. Some stages call LLMs. The control flow is deterministic. For that, a distributed orchestration framework adds infrastructure without value:

| | workflow-go | Temporal / Hatchet |
|---|---|---|
| Dependencies | 0 | Message broker, database, worker runtime |
| Mental model | Steps in order | Activities, workflows, task queues, workers |
| Concurrency | Goroutines + semaphore | Worker pools, task routing |
| Persistence | FileStore (local filesystem) | Database (Postgres, MySQL) |
| Resume | Immutable step outputs | Event sourcing / checkpoints |
| Infrastructure | `go run main.go` | Temporal server, or Hatchet cloud |
| Lock-in | None | Framework-specific SDKs |

If you need distributed execution across machines, use Temporal. If you need a pipeline engine that runs in a single binary with zero infrastructure, use this.

## See Also

| Package | Description |
|---|---|
| [@probeo/workflow](https://github.com/probeo-io/workflow) | TypeScript version of this package |
| [workflow-py](https://github.com/probeo-io/workflow-py) | Python version of this package |
| [anymodel-go](https://github.com/probeo-io/anymodel-go) | Unified LLM router for Go |
| [anyserp-go](https://github.com/probeo-io/anyserp-go) | Unified SERP API router for Go |

## License

MIT
