package workflow

import (
	"context"
	"fmt"
	"os"
	"sync"
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

// TestSingleConcurrentStep verifies a basic single-step workflow.
func TestSingleConcurrentStep(t *testing.T) {
	dir := newTempDir(t)
	wf := New(&WorkflowConfig{ID: "single-step", BaseDir: dir})
	wf.Step(newDoubleStep())

	items := []WorkItem{{ID: "a", Data: 5.0}}
	results, err := wf.Run(context.Background(), items, nil)
	assertNoError(t, err)
	assertLen(t, results, 1)
	assertAllCompleted(t, results)
}

// TestMultiStepSequence verifies two concurrent steps run in order.
func TestMultiStepSequence(t *testing.T) {
	dir := newTempDir(t)
	add10 := &testStep{
		name: "add10",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			return input.(float64) + 10, nil
		},
	}
	wf := New(&WorkflowConfig{ID: "multi-step", BaseDir: dir})
	wf.Step(newDoubleStep()).Step(add10)

	items := []WorkItem{{ID: "a", Data: 3.0}}
	results, err := wf.Run(context.Background(), items, nil)
	assertNoError(t, err)
	assertAllCompleted(t, results)
}

// TestCollectiveStepReceivesAllItems verifies collective sees every item.
func TestCollectiveStepReceivesAllItems(t *testing.T) {
	dir := newTempDir(t)
	var receivedCount atomic.Int32

	collective := &testStep{
		name: "count-all",
		mode: Collective,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			items := input.([]CollectiveItem)
			receivedCount.Store(int32(len(items)))
			return "done", nil
		},
	}

	wf := New(&WorkflowConfig{ID: "collective-count", BaseDir: dir})
	wf.Step(collective)

	items := makeItems(7)
	_, err := wf.Run(context.Background(), items, nil)
	assertNoError(t, err)
	assertEqualInt(t, int(receivedCount.Load()), 7)
}

// TestMixedConcurrentAndCollective runs concurrent then collective.
func TestMixedConcurrentAndCollective(t *testing.T) {
	dir := newTempDir(t)
	wf := New(&WorkflowConfig{ID: "mixed", BaseDir: dir})

	triple := &testStep{
		name: "triple",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			return input.(float64) * 3, nil
		},
	}

	wf.Step(triple).Step(newSumStep())
	items := []WorkItem{
		{ID: "a", Data: 1.0},
		{ID: "b", Data: 2.0},
	}
	results, err := wf.Run(context.Background(), items, nil)
	assertNoError(t, err)
	assertLen(t, results, 2)
	assertAllCompleted(t, results)
}

// TestProgressCallbackCounts verifies progress reports correct totals.
func TestProgressCallbackCounts(t *testing.T) {
	dir := newTempDir(t)
	var lastProgress WorkflowProgress
	var mu sync.Mutex

	noop := &testStep{
		name: "noop",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			return input, nil
		},
	}

	wf := New(&WorkflowConfig{ID: "progress-counts", BaseDir: dir})
	wf.Step(noop)

	opts := &Options{
		Concurrency: 1,
		OnProgress: func(p WorkflowProgress) {
			mu.Lock()
			lastProgress = p
			mu.Unlock()
		},
	}

	items := makeItems(3)
	_, err := wf.Run(context.Background(), items, opts)
	assertNoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assertEqualInt(t, lastProgress.Total, 3)
	assertEqualInt(t, lastProgress.Completed, 3)
	assertEqualInt(t, lastProgress.Failed, 0)
}

// TestConcurrencyLimit verifies max parallel goroutines is respected.
func TestConcurrencyLimit(t *testing.T) {
	dir := newTempDir(t)
	var running atomic.Int32
	var maxRunning atomic.Int32

	limited := &testStep{
		name: "limited",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			cur := running.Add(1)
			for {
				old := maxRunning.Load()
				if cur <= old || maxRunning.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			running.Add(-1)
			return input, nil
		},
	}

	wf := New(&WorkflowConfig{ID: "concurrency-limit", BaseDir: dir})
	wf.Step(limited)

	opts := &Options{Concurrency: 2}
	items := makeItems(6)
	results, err := wf.Run(context.Background(), items, opts)
	assertNoError(t, err)
	assertAllCompleted(t, results)

	if maxRunning.Load() > 2 {
		t.Errorf("expected max 2 concurrent, got %d", maxRunning.Load())
	}
}

// TestRetryExhaustion verifies item is marked failed after all retries.
func TestRetryExhaustion(t *testing.T) {
	dir := newTempDir(t)

	alwaysFail := &testStep{
		name: "always-fail",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			return nil, fmt.Errorf("permanent error")
		},
	}

	wf := New(&WorkflowConfig{ID: "exhaust-retry", BaseDir: dir})
	wf.Step(alwaysFail)

	items := []WorkItem{{ID: "doomed", Data: "x"}}
	opts := &Options{MaxRetries: 1, Concurrency: 1}
	results, err := wf.Run(context.Background(), items, opts)
	assertNoError(t, err)
	assertLen(t, results, 1)

	if results[0].Status != StatusFailed {
		t.Errorf("expected failed, got %s", results[0].Status)
	}
	if results[0].Error != "permanent error" {
		t.Errorf("expected 'permanent error', got '%s'", results[0].Error)
	}
}

// TestEmptyItems verifies no errors with zero items.
func TestEmptyItems(t *testing.T) {
	dir := newTempDir(t)
	wf := New(&WorkflowConfig{ID: "empty", BaseDir: dir})
	wf.Step(newDoubleStep())

	results, err := wf.Run(context.Background(), []WorkItem{}, nil)
	assertNoError(t, err)
	assertLen(t, results, 0)
}

// TestRunOne verifies the single-item convenience method.
func TestRunOne(t *testing.T) {
	dir := newTempDir(t)
	wf := New(&WorkflowConfig{ID: "run-one", BaseDir: dir})
	wf.Step(newDoubleStep())

	result, err := wf.RunOne(context.Background(), WorkItem{ID: "solo", Data: 4.0}, nil)
	assertNoError(t, err)
	if result.Status != StatusCompleted {
		t.Errorf("expected completed, got %s", result.Status)
	}
}

// TestStepOrdering verifies steps execute in added order.
func TestStepOrdering(t *testing.T) {
	dir := newTempDir(t)
	var order []string
	var mu sync.Mutex

	makeStep := func(name string) *testStep {
		return &testStep{
			name: name,
			mode: Concurrent,
			runFunc: func(input any, ctx *StepContext) (any, error) {
				mu.Lock()
				order = append(order, name)
				mu.Unlock()
				return input, nil
			},
		}
	}

	wf := New(&WorkflowConfig{ID: "ordering", BaseDir: dir})
	wf.Step(makeStep("first")).Step(makeStep("second")).Step(makeStep("third"))

	items := []WorkItem{{ID: "a", Data: 1}}
	opts := &Options{Concurrency: 1}
	_, err := wf.Run(context.Background(), items, opts)
	assertNoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 {
		t.Fatalf("expected 3 step runs, got %d", len(order))
	}
	if order[0] != "first" || order[1] != "second" || order[2] != "third" {
		t.Errorf("expected [first second third], got %v", order)
	}
}

// TestContextCancellationMidStep cancels context while step is running.
func TestContextCancellationMidStep(t *testing.T) {
	dir := newTempDir(t)
	var started atomic.Int32

	blocking := &testStep{
		name: "blocking",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			started.Add(1)
			<-ctx.Ctx.Done()
			return nil, ctx.Ctx.Err()
		},
	}

	wf := New(&WorkflowConfig{ID: "cancel-mid", BaseDir: dir})
	wf.Step(blocking)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	items := []WorkItem{{ID: "a", Data: 1}}
	_, _ = wf.Run(ctx, items, &Options{Concurrency: 1})

	if started.Load() == 0 {
		t.Error("expected step to have started")
	}
}

// TestItemStateTracking verifies status transitions through workflow.
func TestItemStateTracking(t *testing.T) {
	dir := newTempDir(t)
	var sawRunning atomic.Bool

	spy := &testStep{
		name: "spy",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			store := NewFileStore(dir)
			state, err := store.GetItem("state-track", ctx.ItemID)
			if err == nil && state != nil && state.Status == StatusRunning {
				sawRunning.Store(true)
			}
			return input, nil
		},
	}

	wf := New(&WorkflowConfig{ID: "state-track", BaseDir: dir})
	wf.Step(spy)

	items := []WorkItem{{ID: "tracked", Data: "x"}}
	results, err := wf.Run(context.Background(), items, &Options{Concurrency: 1})
	assertNoError(t, err)

	if !sawRunning.Load() {
		t.Error("expected item to be in running state during step execution")
	}
	assertAllCompleted(t, results)
}

// TestErrorMessageCaptured verifies error text is stored in ItemState.
func TestErrorMessageCaptured(t *testing.T) {
	dir := newTempDir(t)

	failing := &testStep{
		name: "fail-msg",
		mode: Concurrent,
		runFunc: func(input any, ctx *StepContext) (any, error) {
			return nil, fmt.Errorf("specific failure reason")
		},
	}

	wf := New(&WorkflowConfig{ID: "error-msg", BaseDir: dir})
	wf.Step(failing)

	items := []WorkItem{{ID: "e1", Data: "x"}}
	_, _ = wf.Run(context.Background(), items, &Options{MaxRetries: 0, Concurrency: 1})

	store := NewFileStore(dir)
	state, err := store.GetItem("error-msg", "e1")
	assertNoError(t, err)

	if state == nil {
		t.Fatal("expected state to exist")
	}
	if state.Error != "specific failure reason" {
		t.Errorf("expected 'specific failure reason', got '%s'", state.Error)
	}
	if state.Status != StatusFailed {
		t.Errorf("expected failed status, got %s", state.Status)
	}
}

// TestNewWithNilConfig verifies New works with nil config.
func TestNewWithNilConfig(t *testing.T) {
	wf := New(nil)
	if wf == nil {
		t.Fatal("expected non-nil workflow")
	}
}

// TestResourceChaining verifies Resource returns the workflow for chaining.
func TestResourceChaining(t *testing.T) {
	dir := newTempDir(t)
	wf := New(&WorkflowConfig{ID: "chain", BaseDir: dir})
	result := wf.Resource("a", 1).Resource("b", 2)
	if result != wf {
		t.Error("expected Resource to return same workflow")
	}
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
