package workflow

import (
	"testing"
)

// TestSaveAndLoadItem verifies round-trip persistence of ItemState.
func TestSaveAndLoadItem(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	item := newItemState("item-1", "step-a")
	item.Status = StatusRunning

	assertNoError(t, store.SaveItem("wf-1", item))

	loaded, err := store.GetItem("wf-1", "item-1")
	assertNoError(t, err)
	if loaded == nil {
		t.Fatal("expected item, got nil")
	}
	if loaded.Status != StatusRunning {
		t.Errorf("expected running, got %s", loaded.Status)
	}
}

// TestSaveAndLoadStepOutput verifies step output persistence.
func TestSaveAndLoadStepOutput(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)

	output := map[string]any{"score": 42.0}
	assertNoError(t, store.SaveStepOutput("wf-1", "item-1", "analyze", output))

	loaded, err := store.GetStepOutput("wf-1", "item-1", "analyze")
	assertNoError(t, err)
	if loaded == nil {
		t.Fatal("expected output, got nil")
	}
	m, ok := loaded.(map[string]any)
	if !ok {
		t.Fatal("expected map output")
	}
	if m["score"] != 42.0 {
		t.Errorf("expected 42, got %v", m["score"])
	}
}

// TestImmutability verifies saving same step output twice does not overwrite.
func TestImmutability(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)

	assertNoError(t, store.SaveStepOutput("wf-1", "item-1", "step-a", "first"))
	assertNoError(t, store.SaveStepOutput("wf-1", "item-1", "step-a", "second"))

	loaded, err := store.GetStepOutput("wf-1", "item-1", "step-a")
	assertNoError(t, err)
	if loaded != "first" {
		t.Errorf("expected 'first' (immutable), got '%v'", loaded)
	}
}

// TestListItems verifies listing all items in a workflow.
func TestListItems(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)

	for _, id := range []string{"a", "b", "c"} {
		item := newItemState(id, "step-1")
		assertNoError(t, store.SaveItem("wf-list", item))
	}

	items, err := store.ListItems("wf-list")
	assertNoError(t, err)
	if len(items) != 3 {
		t.Errorf("expected 3 items, got %d", len(items))
	}
}

// TestGetNonExistentItem returns nil for missing item.
func TestGetNonExistentItem(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)

	item, err := store.GetItem("wf-missing", "ghost")
	assertNoError(t, err)
	if item != nil {
		t.Error("expected nil for non-existent item")
	}
}

// TestGetNonExistentStepOutput returns nil for missing step output.
func TestGetNonExistentStepOutput(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)

	out, err := store.GetStepOutput("wf-missing", "ghost", "step-x")
	assertNoError(t, err)
	if out != nil {
		t.Error("expected nil for non-existent step output")
	}
}

// TestDirectoryCreation verifies dirs are created automatically.
func TestDirectoryCreation(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir + "/nested/deep")

	item := newItemState("item-1", "step-1")
	assertNoError(t, store.SaveItem("wf-1", item))

	loaded, err := store.GetItem("wf-1", "item-1")
	assertNoError(t, err)
	if loaded == nil {
		t.Fatal("expected item after auto dir creation")
	}
}

// TestMultipleWorkflowIsolation verifies isolation between workflow IDs.
func TestMultipleWorkflowIsolation(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)

	item1 := newItemState("shared-id", "step-1")
	item1.Status = StatusCompleted
	assertNoError(t, store.SaveItem("wf-alpha", item1))

	item2 := newItemState("shared-id", "step-1")
	item2.Status = StatusFailed
	assertNoError(t, store.SaveItem("wf-beta", item2))

	loaded1, err := store.GetItem("wf-alpha", "shared-id")
	assertNoError(t, err)
	loaded2, err := store.GetItem("wf-beta", "shared-id")
	assertNoError(t, err)

	if loaded1.Status != StatusCompleted {
		t.Errorf("wf-alpha: expected completed, got %s", loaded1.Status)
	}
	if loaded2.Status != StatusFailed {
		t.Errorf("wf-beta: expected failed, got %s", loaded2.Status)
	}
}

// TestListItemsEmptyWorkflow returns nil for nonexistent workflow.
func TestListItemsEmptyWorkflow(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)

	items, err := store.ListItems("nonexistent")
	assertNoError(t, err)
	if items != nil {
		t.Errorf("expected nil, got %v", items)
	}
}

// TestNewFileStoreDefaultDir verifies default base dir.
func TestNewFileStoreDefaultDir(t *testing.T) {
	store := NewFileStore("")
	if store.baseDir != ".workflow" {
		t.Errorf("expected .workflow, got %s", store.baseDir)
	}
}

// TestStepOutputIsolationAcrossItems verifies items don't share outputs.
func TestStepOutputIsolationAcrossItems(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)

	assertNoError(t, store.SaveStepOutput("wf-1", "item-a", "step-1", "output-a"))
	assertNoError(t, store.SaveStepOutput("wf-1", "item-b", "step-1", "output-b"))

	outA, err := store.GetStepOutput("wf-1", "item-a", "step-1")
	assertNoError(t, err)
	outB, err := store.GetStepOutput("wf-1", "item-b", "step-1")
	assertNoError(t, err)

	if outA != "output-a" {
		t.Errorf("expected output-a, got %v", outA)
	}
	if outB != "output-b" {
		t.Errorf("expected output-b, got %v", outB)
	}
}

// TestSaveItemOverwrites verifies item state can be updated.
func TestSaveItemOverwrites(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)

	item := newItemState("item-1", "step-1")
	item.Status = StatusPending
	assertNoError(t, store.SaveItem("wf-1", item))

	item.Status = StatusCompleted
	assertNoError(t, store.SaveItem("wf-1", item))

	loaded, err := store.GetItem("wf-1", "item-1")
	assertNoError(t, err)
	if loaded.Status != StatusCompleted {
		t.Errorf("expected completed after overwrite, got %s", loaded.Status)
	}
}
