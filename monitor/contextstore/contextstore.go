package contextstore

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"go.uber.org/zap"
)

type store struct {
	storebasePath string
}

const (
	eventInfoFile = "eventInfo.data"
)

// NewContextStore returns a handle to a new context store
// The store is maintained in a file hierarchy so if the context id
// already exists calling a storecontext with new id will cause an overwrite
func NewContextStore(basePath string) ContextStore {

	_, err := os.Stat(basePath)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(basePath, 0700); err != nil {
			zap.L().Fatal("Failed to create context store directory", zap.Error(err))
		}
	}

	return &store{storebasePath: basePath}
}

// Store context writes to the store the eventInfo which can be used as a event to trireme
func (s *store) StoreContext(contextID string, eventInfo interface{}) error {

	folder := filepath.Join(s.storebasePath, contextID)

	if _, err := os.Stat(folder); os.IsNotExist(err) {
		if err := os.MkdirAll(folder, 0700); err != nil {
			return err
		}
	}

	data, err := json.Marshal(eventInfo)
	if err != nil {
		return err
	}

	if err = ioutil.WriteFile(filepath.Join(folder, eventInfoFile), data, 0600); err != nil {
		return err
	}

	return nil

}

// GetContextInfo the event corresponding to the store
func (s *store) GetContextInfo(contextID string, context interface{}) error {

	folder := filepath.Join(s.storebasePath, contextID)

	if _, err := os.Stat(folder); os.IsNotExist(err) {
		return fmt.Errorf("Unknown ContextID %s", contextID)
	}

	data, err := ioutil.ReadFile(filepath.Join(folder, eventInfoFile))
	if err != nil {
		return fmt.Errorf("Unable to retrieve context from store %s", err.Error())
	}

	if err := json.Unmarshal(data, context); err != nil {
		zap.L().Warn("Found invalid state for context - Cleaning up",
			zap.String("contextID", contextID),
			zap.Error(err),
		)

		if rerr := s.RemoveContext(contextID); rerr != nil {
			return fmt.Errorf("Failed to remove invalide context for %s", rerr.Error())
		}
		return err
	}

	return nil
}

// RemoveContext the context reference from the store
func (s *store) RemoveContext(contextID string) error {

	folder := filepath.Join(s.storebasePath, contextID)
	if _, err := os.Stat(folder); os.IsNotExist(err) {
		return fmt.Errorf("Unknown ContextID %s", contextID)
	}

	return os.RemoveAll(folder)

}

// Destroy will clean up the entire state for all services in the system
func (s *store) DestroyStore() error {

	if _, err := os.Stat(s.storebasePath); os.IsNotExist(err) {
		return fmt.Errorf("Store Not Initialized")
	}

	return os.RemoveAll(s.storebasePath)
}

// WalkStore retrieves all the context store information and returns it in a channel
func (s *store) WalkStore() (chan string, error) {

	contextChannel := make(chan string, 1)

	files, err := ioutil.ReadDir(s.storebasePath)
	if err != nil {
		close(contextChannel)
		return contextChannel, fmt.Errorf("Store is empty")
	}

	go func() {
		i := 0
		for _, file := range files {
			zap.L().Debug("File Name", zap.String("Path", file.Name()))
			contextChannel <- file.Name()
			i++
		}

		contextChannel <- ""
		close(contextChannel)
	}()

	return contextChannel, nil
}
