package workflow

import (
	"context"
	"fmt"
	"log"
	"time"
)

// StepMode determines how a step processes items.
type StepMode string

const (
	// Concurrent runs the step per-item, many in parallel.
	Concurrent StepMode = "concurrent"

	// Collective waits for all items, runs once with the full set.
	Collective StepMode = "collective"
)

// Step is a named unit of work in a pipeline.
// Concurrent steps receive a single item's data.
// Collective steps receive a []CollectiveItem slice.
type Step interface {
	Name() string
	Mode() StepMode
	Run(input any, ctx *StepContext) (any, error)
}

// CollectiveItem wraps an item ID and its data for collective steps.
type CollectiveItem struct {
	ID   string
	Data any
}

// StepContext is passed to each step's Run function.
type StepContext struct {
	ItemID    string
	Resources map[string]any
	Log       Logger
	GetCache  func(stepName string) (any, error)
	Ctx       context.Context
}

// Logger provides scoped logging for a step execution.
type Logger struct {
	prefix string
}

// NewLogger creates a logger scoped to an item and step.
func NewLogger(itemID, stepName string) Logger {
	return Logger{prefix: fmt.Sprintf("[%s:%s]", itemID, stepName)}
}

// Info logs an informational message.
func (l Logger) Info(msg string) {
	log.Printf("%s %s", l.prefix, msg)
}

// Warn logs a warning message.
func (l Logger) Warn(msg string) {
	log.Printf("%s WARN: %s", l.prefix, msg)
}

// Error logs an error message.
func (l Logger) Error(msg string) {
	log.Printf("%s ERROR: %s", l.prefix, msg)
}

// WorkItem is an item flowing through the pipeline.
type WorkItem struct {
	ID   string
	Data any
}

// ItemStatus represents the state of an item in the pipeline.
type ItemStatus string

const (
	StatusPending   ItemStatus = "pending"
	StatusRunning   ItemStatus = "running"
	StatusCompleted ItemStatus = "completed"
	StatusFailed    ItemStatus = "failed"
)

// ItemState tracks the state of a single item in the workflow.
type ItemState struct {
	ID          string            `json:"id"`
	CurrentStep string            `json:"currentStep"`
	Status      ItemStatus        `json:"status"`
	StepOutputs map[string]any    `json:"stepOutputs"`
	Error       string            `json:"error,omitempty"`
	Attempts    int               `json:"attempts"`
	CreatedAt   string            `json:"createdAt"`
	UpdatedAt   string            `json:"updatedAt"`
}

// newItemState creates a fresh ItemState for an item.
func newItemState(id, firstStep string) *ItemState {
	now := time.Now().UTC().Format(time.RFC3339)
	return &ItemState{
		ID:          id,
		CurrentStep: firstStep,
		Status:      StatusPending,
		StepOutputs: make(map[string]any),
		Attempts:    0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// WorkflowProgress reports progress during execution.
type WorkflowProgress struct {
	Total       int
	Completed   int
	Failed      int
	Running     int
	CurrentStep string
}

// Options configures a workflow run.
type Options struct {
	Concurrency int
	MaxRetries  int
	OnProgress  func(WorkflowProgress)
}

// applyDefaults fills in zero-value options with defaults.
func (o *Options) applyDefaults() {
	if o.Concurrency <= 0 {
		o.Concurrency = 5
	}
	if o.MaxRetries < 0 {
		o.MaxRetries = 2
	}
}
