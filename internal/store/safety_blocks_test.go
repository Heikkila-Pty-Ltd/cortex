package store

import (
	"path/filepath"
	"testing"
	"time"
)

func tempSafetyStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "safety.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSetGetRemoveBlock(t *testing.T) {
	s := tempSafetyStore(t)

	if err := s.SetBlock("scope-a", "type-a", time.Now().Add(time.Minute), "temporary hold"); err != nil {
		t.Fatalf("SetBlock failed: %v", err)
	}

	block, err := s.GetBlock("scope-a", "type-a")
	if err != nil {
		t.Fatalf("GetBlock failed: %v", err)
	}
	if block == nil {
		t.Fatal("expected block, got nil")
	}
	if block.Scope != "scope-a" || block.BlockType != "type-a" || block.Reason != "temporary hold" {
		t.Fatalf("unexpected block metadata: %#v", block)
	}
	if block.BlockedUntil.IsZero() {
		t.Fatal("expected blocked until to be set")
	}

	if err := s.RemoveBlock("scope-a", "type-a"); err != nil {
		t.Fatalf("RemoveBlock failed: %v", err)
	}
	block, err = s.GetBlock("scope-a", "type-a")
	if err != nil {
		t.Fatalf("GetBlock after remove failed: %v", err)
	}
	if block != nil {
		t.Fatalf("expected block to be removed, got %#v", block)
	}
}

func TestSetBlockWithMetadataRoundTrip(t *testing.T) {
	s := tempSafetyStore(t)

	metadata := map[string]interface{}{
		"attempts": 3,
		"tag":      "circuit",
	}
	if err := s.SetBlockWithMetadata("system", "gateway", time.Now().Add(10*time.Minute), "gateway circuit", metadata); err != nil {
		t.Fatalf("SetBlockWithMetadata failed: %v", err)
	}

	block, err := s.GetBlock("system", "gateway")
	if err != nil {
		t.Fatalf("GetBlock failed: %v", err)
	}
	if block == nil {
		t.Fatal("expected block, got nil")
	}
	if got := block.Metadata["tag"]; got != "circuit" {
		t.Fatalf("metadata tag = %v, want circuit", got)
	}
	if _, ok := block.Metadata["attempts"]; !ok {
		t.Fatalf("expected attempts metadata, got %#v", block.Metadata)
	}
}

func TestGetMissingBlockReturnsNil(t *testing.T) {
	s := tempSafetyStore(t)
	block, err := s.GetBlock("missing", "type")
	if err != nil {
		t.Fatalf("GetBlock failed: %v", err)
	}
	if block != nil {
		t.Fatalf("expected nil block for missing key, got %#v", block)
	}
}

func TestBeadValidatingRoundTrip(t *testing.T) {
	s := tempSafetyStore(t)
	beadID := "bead-validate"

	if err := s.SetBeadValidating(beadID, time.Now().Add(2*time.Minute)); err != nil {
		t.Fatalf("SetBeadValidating failed: %v", err)
	}

	validating, err := s.IsBeadValidating(beadID)
	if err != nil {
		t.Fatalf("IsBeadValidating failed: %v", err)
	}
	if !validating {
		t.Fatalf("expected bead to be validating")
	}

	if err := s.ClearBeadValidating(beadID); err != nil {
		t.Fatalf("ClearBeadValidating failed: %v", err)
	}

	validating, err = s.IsBeadValidating(beadID)
	if err != nil {
		t.Fatalf("IsBeadValidating after clear failed: %v", err)
	}
	if validating {
		t.Fatalf("expected bead to be not validating after clear")
	}
}
