package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	wf "github.com/probeo-io/workflow-go"
)

func main() {
	args := os.Args[1:]
	demos := map[string]func(context.Context){
		"simple":     demoSimple,
		"resources":  demoResources,
		"collective": demoCollective,
		"progress":   demoProgress,
	}

	if len(args) == 0 {
		for _, fn := range demos {
			fn(context.Background())
		}
		return
	}

	for _, name := range args {
		fn, ok := demos[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "Unknown demo: %s\nAvailable: %s\n", name, strings.Join(keys(demos), ", "))
			os.Exit(1)
		}
		fn(context.Background())
	}
}

// uppercaseStep converts input strings to uppercase.
type uppercaseStep struct{}

func (s *uppercaseStep) Name() string           { return "uppercase" }
func (s *uppercaseStep) Mode() wf.StepMode      { return wf.Concurrent }
func (s *uppercaseStep) Run(input any, ctx *wf.StepContext) (any, error) {
	return strings.ToUpper(input.(string)), nil
}

// addPrefixStep adds a prefix to the input.
type addPrefixStep struct{}

func (s *addPrefixStep) Name() string           { return "add-prefix" }
func (s *addPrefixStep) Mode() wf.StepMode      { return wf.Concurrent }
func (s *addPrefixStep) Run(input any, ctx *wf.StepContext) (any, error) {
	return fmt.Sprintf("PROCESSED: %s", input.(string)), nil
}

// tagStep uses a shared resource to tag input.
type tagStep struct{}

func (s *tagStep) Name() string           { return "tag" }
func (s *tagStep) Mode() wf.StepMode      { return wf.Concurrent }
func (s *tagStep) Run(input any, ctx *wf.StepContext) (any, error) {
	cfg := ctx.Resources["config"].(map[string]string)
	return fmt.Sprintf("%s%s%s", cfg["prefix"], cfg["separator"], input.(string)), nil
}

// scoreStep returns the length of the input string.
type scoreStep struct{}

func (s *scoreStep) Name() string           { return "score" }
func (s *scoreStep) Mode() wf.StepMode      { return wf.Concurrent }
func (s *scoreStep) Run(input any, ctx *wf.StepContext) (any, error) {
	return len(input.(string)), nil
}

// slowStep simulates work with a delay.
type slowStep struct{}

func (s *slowStep) Name() string           { return "process" }
func (s *slowStep) Mode() wf.StepMode      { return wf.Concurrent }
func (s *slowStep) Run(input any, ctx *wf.StepContext) (any, error) {
	ms := input.(int)
	time.Sleep(time.Duration(ms*100) * time.Millisecond)
	return fmt.Sprintf("done in %dms", ms*100), nil
}

func demoSimple(ctx context.Context) {
	fmt.Println("\n=== Simple Pipeline ===")

	w := wf.New(&wf.WorkflowConfig{BaseDir: ".example-pipeline"})
	w.Step(&uppercaseStep{}).Step(&addPrefixStep{})

	results, err := w.Run(ctx, []wf.WorkItem{
		{ID: "item-1", Data: "hello world"},
		{ID: "item-2", Data: "foo bar"},
		{ID: "item-3", Data: "pipeline test"},
	}, nil)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	for _, r := range results {
		fmt.Printf("  %s: %v\n", r.ID, r.Outputs["add-prefix"])
	}
	fmt.Println()
}

func demoResources(ctx context.Context) {
	fmt.Println("\n=== Shared Resources ===")

	w := wf.New(&wf.WorkflowConfig{BaseDir: ".example-resources"})
	w.Resource("config", map[string]string{"prefix": "v2", "separator": "-"}).
		Step(&tagStep{})

	results, err := w.Run(ctx, []wf.WorkItem{
		{ID: "a", Data: "alpha"},
		{ID: "b", Data: "beta"},
	}, nil)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	for _, r := range results {
		fmt.Printf("  %s: %v\n", r.ID, r.Outputs["tag"])
	}
	fmt.Println()
}

func demoCollective(ctx context.Context) {
	fmt.Println("\n=== Collective Step ===")

	w := wf.New(&wf.WorkflowConfig{BaseDir: ".example-collective"})
	w.Step(&scoreStep{})

	results, err := w.Run(ctx, []wf.WorkItem{
		{ID: "short", Data: "hi"},
		{ID: "medium", Data: "hello world"},
		{ID: "long", Data: "this is a longer string for testing"},
	}, nil)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	for _, r := range results {
		fmt.Printf("  %s: %v\n", r.ID, r.Outputs["score"])
	}
	fmt.Println()
}

func demoProgress(ctx context.Context) {
	fmt.Println("\n=== Progress Tracking ===")

	w := wf.New(&wf.WorkflowConfig{BaseDir: ".example-progress"})
	w.Step(&slowStep{})

	results, err := w.Run(ctx, []wf.WorkItem{
		{ID: "fast", Data: 1},
		{ID: "medium", Data: 3},
		{ID: "slow", Data: 5},
	}, &wf.Options{
		Concurrency: 2,
		OnProgress: func(p wf.WorkflowProgress) {
			fmt.Printf("  Progress: %d/%d (%s)\n", p.Completed, p.Total, p.CurrentStep)
		},
	})
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Println()
	for _, r := range results {
		fmt.Printf("  %s: %v\n", r.ID, r.Outputs["process"])
	}
	fmt.Println()
}

func keys(m map[string]func(context.Context)) []string {
	k := make([]string, 0, len(m))
	for key := range m {
		k = append(k, key)
	}
	return k
}
