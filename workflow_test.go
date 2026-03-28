package workflow

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// testStep is a simple Step implementation for tests.
type testStep struct {
	name    string
	mode    StepMode
	runFunc func(input any, ctx *StepContext) (any, error)
}

func (s *testStep) Name() string   { return s.name }
func (s *testStep) Mode() StepMode { return s.mode }

func (s *testStep) Run(input any, ctx *StepContext) (any, error) {
	return s.runFunc(input, ctx)
}

// newTempDir creates a temporary directory for test stores.
func newTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "workflow-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// newDoubleStep returns a concurrent step that doubles a float64 input.
func newDoubleStep() *testStep {
	return &testStep{
		name: "double",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			return input.(float64) * 2, nil
		},
	}
}

// newSumStep returns a collective step that sums all item data.
func newSumStep() *testStep {
	return &testStep{
		name: "sum",
		mode: Collective,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			return sumCollectiveItems(input.([]CollectiveItem)), nil
		},
	}
}

// TestTwoStepWorkflow tests a concurrent step followed by a collective step.
func TestTwoStepWorkflow(t *testing.T) {
	dir := newTempDir(t)
	wf := New(&WorkflowConfig{ID: "test-two-step", BaseDir: dir})
	wf.Step(newDoubleStep()).Step(newSumStep())

	items := []WorkItem{
		{ID: "a", Data: 1.0},
		{ID: "b", Data: 2.0},
		{ID: "c", Data: 3.0},
	}
	results, err := wf.Run(context.Background(), items, nil)
	assertNoError(t, err)
	assertLen(t, results, 3)
	assertAllCompleted(t, results)
}

// sumCollectiveItems adds up float64 data from collective items.
func sumCollectiveItems(items []CollectiveItem) any {
	total := 0.0
	for _, item := range items {
		total += item.Data.(float64)
	}
	return total
}

// TestResumeAfterCrash verifies cached steps are not re-run.
func TestResumeAfterCrash(t *testing.T) {
	dir := newTempDir(t)
	var runCount atomic.Int32

	counter := &testStep{
		name: "count",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			runCount.Add(1)
			return input, nil
		},
	}

	items := []WorkItem{
		{ID: "x", Data: "hello"},
		{ID: "y", Data: "world"},
	}

	runWorkflowOnce(t, dir, counter, items)
	firstCount := runCount.Load()
	assertEqualInt(t, int(firstCount), 2)

	runWorkflowOnce(t, dir, counter, items)
	secondCount := runCount.Load()
	assertEqualInt(t, int(secondCount), 2)
}

// runWorkflowOnce creates and runs a single-step workflow.
func runWorkflowOnce(t *testing.T, dir string, step Step, items []WorkItem) {
	t.Helper()
	wf := New(&WorkflowConfig{ID: "resume-test", BaseDir: dir})
	wf.Step(step)
	_, err := wf.Run(context.Background(), items, nil)
	assertNoError(t, err)
}

// TestRetryOnFailure verifies retry with exponential backoff.
func TestRetryOnFailure(t *testing.T) {
	dir := newTempDir(t)
	var attempts atomic.Int32

	flaky := &testStep{
		name: "flaky",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			n := attempts.Add(1)
			if n < 3 {
				return nil, fmt.Errorf("transient error")
			}
			return "ok", nil
		},
	}

	wf := New(&WorkflowConfig{ID: "retry-test", BaseDir: dir})
	wf.Step(flaky)

	items := []WorkItem{{ID: "retry-item", Data: "test"}}
	results, err := wf.Run(context.Background(), items, &Options{MaxRetries: 3})
	assertNoError(t, err)
	assertAllCompleted(t, results)

	if attempts.Load() < 3 {
		t.Errorf("expected at least 3 attempts, got %d", attempts.Load())
	}
}

// newSlowStep returns a step that blocks until context cancellation.
func newSlowStep(counter *atomic.Int32) *testStep {
	return &testStep{
		name: "slow",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			counter.Add(1)
			select {
			case <-time.After(5 * time.Second):
				return "done", nil
			case <-ctx.Ctx.Done():
				return nil, ctx.Ctx.Err()
			}
		},
	}
}

// TestCancellationViaContext verifies context cancellation stops processing.
func TestCancellationViaContext(t *testing.T) {
	dir := newTempDir(t)
	var processed atomic.Int32

	wf := New(&WorkflowConfig{ID: "cancel-test", BaseDir: dir})
	wf.Step(newSlowStep(&processed))

	ctx, cancel := context.WithCancel(context.Background())
	go cancelAfterDelay(cancel, 100*time.Millisecond)

	_, err := wf.Run(ctx, makeItems(10), &Options{Concurrency: 2})
	if err == nil {
		t.Log("cancellation occurred between items")
	}
	if processed.Load() > 5 {
		t.Errorf("expected few items processed, got %d", processed.Load())
	}
}

// makeItems creates n work items with sequential IDs.
func makeItems(n int) []WorkItem {
	items := make([]WorkItem, n)
	for i := range n {
		items[i] = WorkItem{ID: fmt.Sprintf("item-%d", i), Data: i}
	}
	return items
}

// cancelAfterDelay cancels a context after a duration.
func cancelAfterDelay(cancel context.CancelFunc, d time.Duration) {
	time.Sleep(d)
	cancel()
}

// TestProgressCallback verifies progress reporting.
func TestProgressCallback(t *testing.T) {
	dir := newTempDir(t)
	var progressCalls atomic.Int32

	noop := &testStep{
		name: "noop",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			return input, nil
		},
	}

	wf := New(&WorkflowConfig{ID: "progress-test", BaseDir: dir})
	wf.Step(noop)

	items := makeItems(5)
	opts := &Options{
		Concurrency: 1,
		OnProgress: func(p WorkflowProgress) {
			progressCalls.Add(1)
		},
	}

	_, err := wf.Run(context.Background(), items, opts)
	assertNoError(t, err)

	if progressCalls.Load() == 0 {
		t.Error("expected progress callback to be called")
	}
}

// TestResourceInjection verifies resources are accessible in steps.
func TestResourceInjection(t *testing.T) {
	dir := newTempDir(t)

	reader := &testStep{
		name: "read-resource",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			val, ok := ctx.Resources["greeting"]
			if !ok {
				return nil, fmt.Errorf("missing resource")
			}
			return val, nil
		},
	}

	wf := New(&WorkflowConfig{ID: "resource-test", BaseDir: dir})
	wf.Resource("greeting", "hello").Step(reader)

	results, err := wf.Run(context.Background(), []WorkItem{{ID: "r1", Data: nil}}, nil)
	assertNoError(t, err)
	assertAllCompleted(t, results)
}

// assertNoError fails the test if err is non-nil.
func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// assertLen fails if the slice length does not match expected.
func assertLen(t *testing.T, results []*ItemState, expected int) {
	t.Helper()
	if len(results) != expected {
		t.Fatalf("expected %d results, got %d", expected, len(results))
	}
}

// assertAllCompleted fails if any result is not completed.
func assertAllCompleted(t *testing.T, results []*ItemState) {
	t.Helper()
	for _, r := range results {
		if r.Status != StatusCompleted {
			t.Errorf("item %s: expected completed, got %s (error: %s)", r.ID, r.Status, r.Error)
		}
	}
}

// assertEqualInt fails if got != want.
func assertEqualInt(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("expected %d, got %d", want, got)
	}
}
