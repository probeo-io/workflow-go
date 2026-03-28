package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Store is the persistence interface for workflow state.
type Store interface {
	SaveItem(workflowID string, item *ItemState) error
	GetItem(workflowID, itemID string) (*ItemState, error)
	ListItems(workflowID string) ([]*ItemState, error)
	SaveStepOutput(workflowID, itemID, stepName string, output any) error
	GetStepOutput(workflowID, itemID, stepName string) (any, error)
}

// FileStore is a filesystem-backed store.
// Layout: {baseDir}/{workflowID}/_state/{itemID}.json
//
//	{baseDir}/{workflowID}/{stepName}/{itemID}.json
//
// Step outputs are immutable. Written once, never overwritten.
type FileStore struct {
	baseDir string
}

// NewFileStore creates a FileStore rooted at baseDir.
func NewFileStore(baseDir string) *FileStore {
	if baseDir == "" {
		baseDir = ".workflow"
	}
	return &FileStore{baseDir: baseDir}
}

// stateDir returns the directory for item state files.
func (fs *FileStore) stateDir(workflowID string) string {
	return filepath.Join(fs.baseDir, workflowID, "_state")
}

// stepDir returns the directory for a step's output files.
func (fs *FileStore) stepDir(workflowID, stepName string) string {
	return filepath.Join(fs.baseDir, workflowID, stepName)
}

// statePath returns the file path for an item's state.
func (fs *FileStore) statePath(workflowID, itemID string) string {
	return filepath.Join(fs.stateDir(workflowID), itemID+".json")
}

// stepPath returns the file path for a step output.
func (fs *FileStore) stepPath(workflowID, stepName, itemID string) string {
	return filepath.Join(fs.stepDir(workflowID, stepName), itemID+".json")
}

// ensureDir creates a directory if it does not exist.
func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// SaveItem writes an item's state to disk.
func (fs *FileStore) SaveItem(workflowID string, item *ItemState) error {
	if err := ensureDir(fs.stateDir(workflowID)); err != nil {
		return fmt.Errorf("mkdir state: %w", err)
	}
	return writeJSON(fs.statePath(workflowID, item.ID), item)
}

// GetItem loads an item's state from disk. Returns nil if not found.
func (fs *FileStore) GetItem(workflowID, itemID string) (*ItemState, error) {
	path := fs.statePath(workflowID, itemID)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read item: %w", err)
	}
	return unmarshalItemState(data)
}

// unmarshalItemState parses JSON into an ItemState.
func unmarshalItemState(data []byte) (*ItemState, error) {
	var item ItemState
	if err := json.Unmarshal(data, &item); err != nil {
		return nil, fmt.Errorf("unmarshal item: %w", err)
	}
	if item.StepOutputs == nil {
		item.StepOutputs = make(map[string]any)
	}
	return &item, nil
}

// ListItems returns all item states for a workflow.
func (fs *FileStore) ListItems(workflowID string) ([]*ItemState, error) {
	dir := fs.stateDir(workflowID)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("readdir: %w", err)
	}
	return fs.readItemEntries(workflowID, entries)
}

// readItemEntries reads each .json entry into an ItemState.
func (fs *FileStore) readItemEntries(workflowID string, entries []os.DirEntry) ([]*ItemState, error) {
	var items []*ItemState
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		itemID := e.Name()[:len(e.Name())-5]
		item, err := fs.GetItem(workflowID, itemID)
		if err != nil {
			return nil, err
		}
		if item != nil {
			items = append(items, item)
		}
	}
	return items, nil
}

// SaveStepOutput writes a step output. Immutable: skips if file exists.
func (fs *FileStore) SaveStepOutput(workflowID, itemID, stepName string, output any) error {
	if err := ensureDir(fs.stepDir(workflowID, stepName)); err != nil {
		return fmt.Errorf("mkdir step: %w", err)
	}
	path := fs.stepPath(workflowID, stepName, itemID)
	if fileExists(path) {
		return nil
	}
	return writeJSON(path, output)
}

// GetStepOutput reads a step output from disk. Returns nil if not found.
func (fs *FileStore) GetStepOutput(workflowID, itemID, stepName string) (any, error) {
	path := fs.stepPath(workflowID, stepName, itemID)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read step output: %w", err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("unmarshal step output: %w", err)
	}
	return out, nil
}

// writeJSON marshals v to JSON and writes it to path.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// fileExists reports whether a path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
