package workflow

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"
)

// Workflow chains steps together and runs work items through them.
type Workflow struct {
	id        string
	steps     []Step
	store     Store
	resources map[string]any
}

// WorkflowConfig holds options for creating a new Workflow.
type WorkflowConfig struct {
	ID      string
	Store   Store
	BaseDir string
}

// New creates a Workflow with the given configuration.
func New(cfg *WorkflowConfig) *Workflow {
	if cfg == nil {
		cfg = &WorkflowConfig{}
	}
	id := cfg.ID
	if id == "" {
		id = generateID("wf")
	}
	store := cfg.Store
	if store == nil {
		store = NewFileStore(cfg.BaseDir)
	}
	return &Workflow{
		id:        id,
		store:     store,
		resources: make(map[string]any),
	}
}

// generateID creates a unique workflow identifier.
func generateID(prefix string) string {
	ts := time.Now().UnixMilli()
	r := rand.Int63n(1_000_000)
	return fmt.Sprintf("%s-%x-%06x", prefix, ts, r)
}

// Resource registers a shared resource available to all steps.
func (w *Workflow) Resource(name string, value any) *Workflow {
	w.resources[name] = value
	return w
}

// Step adds a step to the pipeline. Steps run in order.
func (w *Workflow) Step(step Step) *Workflow {
	w.steps = append(w.steps, step)
	return w
}

// RunOne runs a single item through the full pipeline.
func (w *Workflow) RunOne(ctx context.Context, item WorkItem, opts *Options) (*ItemState, error) {
	if opts == nil {
		opts = &Options{}
	}
	opts.Concurrency = 1
	results, err := w.Run(ctx, []WorkItem{item}, opts)
	if err != nil {
		return nil, err
	}
	return results[0], nil
}

// Run executes all items through the pipeline steps in order.
func (w *Workflow) Run(ctx context.Context, items []WorkItem, opts *Options) ([]*ItemState, error) {
	if opts == nil {
		opts = &Options{}
	}
	opts.applyDefaults()
	states, err := w.initStates(ctx, items)
	if err != nil {
		return nil, err
	}
	if err := w.processSteps(ctx, states, opts); err != nil {
		return nil, err
	}
	return w.finalizeStates(states)
}

// initStates loads or creates an ItemState for each work item.
func (w *Workflow) initStates(ctx context.Context, items []WorkItem) (map[string]*ItemState, error) {
	states := make(map[string]*ItemState, len(items))
	firstStep := ""
	if len(w.steps) > 0 {
		firstStep = w.steps[0].Name()
	}
	for _, item := range items {
		state, err := w.loadOrCreateState(item, firstStep)
		if err != nil {
			return nil, err
		}
		states[item.ID] = state
	}
	return states, nil
}

// loadOrCreateState checks the store for existing state or creates new.
func (w *Workflow) loadOrCreateState(item WorkItem, firstStep string) (*ItemState, error) {
	existing, err := w.store.GetItem(w.id, item.ID)
	if err != nil {
		return nil, fmt.Errorf("load item %s: %w", item.ID, err)
	}
	if existing != nil && existing.Status == StatusCompleted {
		return existing, nil
	}
	state := existing
	if state == nil {
		state = newItemState(item.ID, firstStep)
	}
	if err := w.store.SaveStepOutput(w.id, item.ID, "_input", item.Data); err != nil {
		return nil, fmt.Errorf("save input %s: %w", item.ID, err)
	}
	return state, nil
}

// processSteps iterates through each step and executes it.
func (w *Workflow) processSteps(ctx context.Context, states map[string]*ItemState, opts *Options) error {
	for _, step := range w.steps {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pending, err := w.filterPending(step, states)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			continue
		}
		if err := w.executeStep(ctx, step, pending, states, opts); err != nil {
			return err
		}
	}
	return nil
}

// filterPending returns items that need processing for a step.
func (w *Workflow) filterPending(step Step, states map[string]*ItemState) ([]*ItemState, error) {
	var pending []*ItemState
	for _, s := range states {
		if s.Status == StatusFailed {
			continue
		}
		needs, err := w.itemNeedsStep(s, step)
		if err != nil {
			return nil, err
		}
		if needs {
			pending = append(pending, s)
		}
	}
	return pending, nil
}

// itemNeedsStep checks if an item still needs to run through a step.
func (w *Workflow) itemNeedsStep(item *ItemState, step Step) (bool, error) {
	cached, err := w.store.GetStepOutput(w.id, item.ID, step.Name())
	if err != nil {
		return false, err
	}
	if cached != nil {
		item.StepOutputs[step.Name()] = cached
		item.CurrentStep = w.nextStepName(step.Name())
		item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return false, w.store.SaveItem(w.id, item)
	}
	return true, nil
}

// executeStep dispatches to concurrent or collective execution.
func (w *Workflow) executeStep(ctx context.Context, step Step, items []*ItemState, states map[string]*ItemState, opts *Options) error {
	if step.Mode() == Collective {
		return w.runCollective(ctx, step, items, opts.MaxRetries)
	}
	return w.runConcurrent(ctx, step, items, opts)
}

// runConcurrent processes items in parallel with a semaphore.
func (w *Workflow) runConcurrent(ctx context.Context, step Step, items []*ItemState, opts *Options) error {
	sem := make(chan struct{}, opts.Concurrency)
	var mu sync.Mutex
	var wg sync.WaitGroup
	completed, failed := 0, 0

	for _, item := range items {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(it *ItemState) {
			defer wg.Done()
			defer func() { <-sem }()
			err := w.processOneItem(ctx, step, it, opts.MaxRetries)
			mu.Lock()
			if err != nil {
				failed++
			} else {
				completed++
			}
			w.emitProgress(opts, items, completed, failed, step)
			mu.Unlock()
		}(item)
	}
	wg.Wait()
	return nil
}

// emitProgress calls the OnProgress callback if set.
func (w *Workflow) emitProgress(opts *Options, items []*ItemState, completed, failed int, step Step) {
	if opts.OnProgress == nil {
		return
	}
	opts.OnProgress(WorkflowProgress{
		Total:       len(items),
		Completed:   completed,
		Failed:      failed,
		Running:     len(items) - completed - failed,
		CurrentStep: step.Name(),
	})
}

// processOneItem runs a single item through a step with retries.
func (w *Workflow) processOneItem(ctx context.Context, step Step, item *ItemState, maxRetries int) error {
	input, err := w.getStepInput(step, item)
	if err != nil {
		return err
	}
	sctx := w.buildStepContext(ctx, step, item)
	w.markRunning(item, step)
	return w.attemptWithRetry(ctx, step, item, input, sctx, maxRetries)
}

// getStepInput loads the previous step's output (or _input) for an item.
func (w *Workflow) getStepInput(step Step, item *ItemState) (any, error) {
	prevName := w.prevStepName(step.Name())
	if prevName != "" {
		return w.store.GetStepOutput(w.id, item.ID, prevName)
	}
	return w.store.GetStepOutput(w.id, item.ID, "_input")
}

// buildStepContext creates a StepContext for an item and step.
func (w *Workflow) buildStepContext(ctx context.Context, step Step, item *ItemState) *StepContext {
	return &StepContext{
		ItemID:    item.ID,
		Resources: w.resources,
		Log:       NewLogger(item.ID, step.Name()),
		GetCache: func(name string) (any, error) {
			return w.store.GetStepOutput(w.id, item.ID, name)
		},
		Ctx: ctx,
	}
}

// markRunning updates an item's state to running.
func (w *Workflow) markRunning(item *ItemState, step Step) {
	item.CurrentStep = step.Name()
	item.Status = StatusRunning
	item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_ = w.store.SaveItem(w.id, item)
}

// attemptWithRetry runs a step with exponential backoff retries.
func (w *Workflow) attemptWithRetry(ctx context.Context, step Step, item *ItemState, input any, sctx *StepContext, maxRetries int) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		item.Attempts = attempt + 1
		output, err := step.Run(input, sctx)
		if err == nil {
			return w.saveSuccess(step, item, output)
		}
		lastErr = err
		if attempt < maxRetries {
			sctx.Log.Warn(fmt.Sprintf("Attempt %d failed, retrying: %v", attempt+1, err))
			w.backoff(ctx, attempt)
		}
	}
	return w.saveFailed(item, lastErr)
}

// backoff sleeps with exponential delay, respecting context cancellation.
func (w *Workflow) backoff(ctx context.Context, attempt int) {
	delay := time.Duration(math.Min(float64(time.Second)*math.Pow(2, float64(attempt)), float64(10*time.Second)))
	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}

// saveSuccess persists a successful step output and advances the item.
func (w *Workflow) saveSuccess(step Step, item *ItemState, output any) error {
	if err := w.store.SaveStepOutput(w.id, item.ID, step.Name(), output); err != nil {
		return err
	}
	item.StepOutputs[step.Name()] = output
	item.CurrentStep = w.nextStepName(step.Name())
	item.Status = StatusPending
	item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return w.store.SaveItem(w.id, item)
}

// saveFailed marks an item as failed and persists its state.
func (w *Workflow) saveFailed(item *ItemState, err error) error {
	item.Status = StatusFailed
	if err != nil {
		item.Error = err.Error()
	}
	item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_ = w.store.SaveItem(w.id, item)
	return err
}

// runCollective runs a collective step against all items at once.
func (w *Workflow) runCollective(ctx context.Context, step Step, items []*ItemState, maxRetries int) error {
	inputs, err := w.gatherCollectiveInputs(step, items)
	if err != nil {
		return err
	}
	sctx := w.buildCollectiveContext(ctx, step)
	return w.attemptCollective(ctx, step, items, inputs, sctx, maxRetries)
}

// gatherCollectiveInputs collects all item inputs for a collective step.
func (w *Workflow) gatherCollectiveInputs(step Step, items []*ItemState) ([]CollectiveItem, error) {
	inputs := make([]CollectiveItem, 0, len(items))
	for _, item := range items {
		input, err := w.getStepInput(step, item)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, CollectiveItem{ID: item.ID, Data: input})
	}
	return inputs, nil
}

// buildCollectiveContext creates a StepContext for a collective step.
func (w *Workflow) buildCollectiveContext(ctx context.Context, step Step) *StepContext {
	return &StepContext{
		ItemID:    "_collective",
		Resources: w.resources,
		Log:       NewLogger("_collective", step.Name()),
		GetCache: func(name string) (any, error) {
			return w.store.GetStepOutput(w.id, "_collective", name)
		},
		Ctx: ctx,
	}
}

// attemptCollective tries a collective step with retries.
func (w *Workflow) attemptCollective(ctx context.Context, step Step, items []*ItemState, inputs []CollectiveItem, sctx *StepContext, maxRetries int) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		output, err := step.Run(inputs, sctx)
		if err == nil {
			return w.saveCollectiveOutputs(step, items, output)
		}
		lastErr = err
		if attempt < maxRetries {
			sctx.Log.Warn(fmt.Sprintf("Attempt %d failed, retrying: %v", attempt+1, err))
			w.backoff(ctx, attempt)
		}
	}
	return w.markAllFailed(items, lastErr)
}

// saveCollectiveOutputs persists outputs from a collective step.
func (w *Workflow) saveCollectiveOutputs(step Step, items []*ItemState, output any) error {
	outputs, ok := output.([]any)
	if ok && len(outputs) == len(items) {
		return w.savePerItemOutputs(step, items, outputs)
	}
	return w.store.SaveStepOutput(w.id, "_collective", step.Name(), output)
}

// savePerItemOutputs saves individual outputs when collective returns a slice.
func (w *Workflow) savePerItemOutputs(step Step, items []*ItemState, outputs []any) error {
	for i, item := range items {
		if err := w.store.SaveStepOutput(w.id, item.ID, step.Name(), outputs[i]); err != nil {
			return err
		}
		item.StepOutputs[step.Name()] = outputs[i]
		item.CurrentStep = w.nextStepName(step.Name())
		item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := w.store.SaveItem(w.id, item); err != nil {
			return err
		}
	}
	return nil
}

// markAllFailed marks every item in the slice as failed.
func (w *Workflow) markAllFailed(items []*ItemState, err error) error {
	for _, item := range items {
		item.Status = StatusFailed
		if err != nil {
			item.Error = err.Error()
		}
		item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		_ = w.store.SaveItem(w.id, item)
	}
	return err
}

// finalizeStates marks non-failed items as completed and returns all states.
func (w *Workflow) finalizeStates(states map[string]*ItemState) ([]*ItemState, error) {
	result := make([]*ItemState, 0, len(states))
	for _, state := range states {
		if state.Status != StatusFailed {
			state.Status = StatusCompleted
			state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			if err := w.store.SaveItem(w.id, state); err != nil {
				return nil, err
			}
		}
		result = append(result, state)
	}
	return result, nil
}

// stepIndex returns the index of a step by name, or -1 if not found.
func (w *Workflow) stepIndex(name string) int {
	for i, s := range w.steps {
		if s.Name() == name {
			return i
		}
	}
	return -1
}

// nextStepName returns the name of the step after the given one.
func (w *Workflow) nextStepName(name string) string {
	idx := w.stepIndex(name)
	if idx >= 0 && idx < len(w.steps)-1 {
		return w.steps[idx+1].Name()
	}
	return "_done"
}

// prevStepName returns the name of the step before the given one.
func (w *Workflow) prevStepName(name string) string {
	idx := w.stepIndex(name)
	if idx > 0 {
		return w.steps[idx-1].Name()
	}
	return ""
}
