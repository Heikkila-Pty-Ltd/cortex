package config

import (
	"fmt"
	"sync"
)

// ConfigManager provides thread-safe access to live configuration.
type ConfigManager interface {
	Get() *Config
	Set(cfg *Config)
	Reload(path string) error
}

// RWMutexManager provides thread-safe read-heavy config access using RWMutex.
type RWMutexManager struct {
	mu  sync.RWMutex
	cfg *Config
}

// NewManager constructs a manager with an initial config.
func NewManager(initial *Config) *RWMutexManager {
	return &RWMutexManager{cfg: initial}
}

// NewRWMutexManager constructs a manager with an initial config.
func NewRWMutexManager(initial *Config) *RWMutexManager {
	return NewManager(initial)
}

// Get returns the current config pointer under a shared lock.
func (m *RWMutexManager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// Set updates the current config pointer under an exclusive lock.
func (m *RWMutexManager) Set(cfg *Config) {
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
}

// Reload loads config from path and atomically swaps it into place.
func (m *RWMutexManager) Reload(path string) error {
	if path == "" {
		return fmt.Errorf("config reload path is required")
	}

	loaded, err := Load(path)
	if err != nil {
		return err
	}

	m.Set(loaded)
	return nil
}

var _ ConfigManager = (*RWMutexManager)(nil)
