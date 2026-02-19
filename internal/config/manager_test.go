package config

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestRWMutexManagerGetSet(t *testing.T) {
	initial := &Config{General: General{LogLevel: "info"}}
	mgr := NewRWMutexManager(initial)

	if got := mgr.Get(); got != initial {
		t.Fatalf("expected initial config pointer")
	}

	next := &Config{General: General{LogLevel: "debug"}}
	mgr.Set(next)

	if got := mgr.Get(); got != next {
		t.Fatalf("expected updated config pointer")
	}
}

func TestRWMutexManagerReload(t *testing.T) {
	path := writeTestConfig(t, validConfig)
	mgr := NewRWMutexManager(nil)

	if err := mgr.Reload(path); err != nil {
		t.Fatalf("reload failed: %v", err)
	}

	cfg := mgr.Get()
	if cfg == nil {
		t.Fatal("expected config after reload")
	}

	if cfg.General.LogLevel == "" {
		t.Fatal("expected populated config from file")
	}
}

func TestRWMutexManagerConcurrentReadWithWrites(t *testing.T) {
	mgr := NewRWMutexManager(&Config{General: General{MaxPerTick: 1}})

	const readers = 32
	const readsPerReader = 1000
	const writes = 100

	var wg sync.WaitGroup
	wg.Add(readers + 1)

	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < readsPerReader; j++ {
				cfg := mgr.Get()
				if cfg == nil {
					t.Error("got nil config during concurrent read")
					return
				}
				_ = cfg.General.MaxPerTick
			}
		}()
	}

	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			mgr.Set(&Config{General: General{MaxPerTick: i + 2}})
		}
	}()

	wg.Wait()

	if got := mgr.Get(); got == nil {
		t.Fatal("expected final non-nil config")
	}
}

func BenchmarkRWMutexManagerGet(b *testing.B) {
	mgr := NewRWMutexManager(&Config{General: General{LogLevel: "info"}})
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cfg := mgr.Get()
			if cfg == nil {
				b.Fatal("nil config")
			}
		}
	})
}

func TestRWMutexManagerReloadConcurrentReaders(t *testing.T) {
	cfgTemplate := validConfig
	path := writeTestConfig(t, cfgTemplate)
	mgr := NewRWMutexManager(nil)

	if err := mgr.Reload(path); err != nil {
		t.Fatalf("initial reload failed: %v", err)
	}

	const iterations = 20
	const readers = 8

	var wg sync.WaitGroup
	wg.Add(readers + 1)

	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations*50; j++ {
				cfg := mgr.Get()
				if cfg == nil {
					t.Error("nil config during read")
					return
				}
				_ = cfg.General.MaxPerTick
			}
		}()
	}

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			content := strings.Replace(validConfig, "max_per_tick = 10", fmt.Sprintf("max_per_tick = %d", i+1), 1)

			reloadPath := writeTestConfig(t, content)
			if err := mgr.Reload(reloadPath); err != nil {
				t.Errorf("reload failed: %v", err)
				return
			}
		}
	}()

	wg.Wait()
}
