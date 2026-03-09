package protocache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func testCache(t *testing.T) Cache[*wrapperspb.StringValue] {
	t.Helper()
	return Cache[*wrapperspb.StringValue]{Dir: t.TempDir()}
}

func TestLoadMiss(t *testing.T) {
	c := testCache(t)
	_, err := c.Load("nonexistent")
	if !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("expected ErrCacheMiss, got %v", err)
	}
}

func TestSaveAndLoad(t *testing.T) {
	c := testCache(t)
	key := Key("test-key")
	original := wrapperspb.String("hello world")

	if err := c.Save(key, original); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := c.Load(key)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !proto.Equal(original, loaded) {
		t.Fatalf("loaded value %v does not match original %v", loaded, original)
	}
}

func TestSaveOverwrite(t *testing.T) {
	c := testCache(t)
	key := Key("overwrite-key")

	if err := c.Save(key, wrapperspb.String("first")); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if err := c.Save(key, wrapperspb.String("second")); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := c.Load(key)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.GetValue() != "second" {
		t.Fatalf("expected 'second', got %q", loaded.GetValue())
	}
}

func TestLoadCorruptedData(t *testing.T) {
	c := testCache(t)
	key := Key("corrupt-key")

	// Write garbage data directly to the cache file
	cachePath := filepath.Join(c.Dir, string(key))
	if err := os.WriteFile(cachePath, []byte("not valid protobuf"), 0600); err != nil {
		t.Fatalf("failed to write corrupt file: %v", err)
	}

	_, err := c.Load(key)
	if err == nil {
		t.Fatal("expected error for corrupted data, got nil")
	}
}

func TestDifferentKeysIndependent(t *testing.T) {
	c := testCache(t)

	if err := c.Save("key-a", wrapperspb.String("alpha")); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if err := c.Save("key-b", wrapperspb.String("beta")); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	a, err := c.Load("key-a")
	if err != nil {
		t.Fatalf("Load key-a failed: %v", err)
	}
	b, err := c.Load("key-b")
	if err != nil {
		t.Fatalf("Load key-b failed: %v", err)
	}

	if a.GetValue() != "alpha" {
		t.Fatalf("expected 'alpha', got %q", a.GetValue())
	}
	if b.GetValue() != "beta" {
		t.Fatalf("expected 'beta', got %q", b.GetValue())
	}
}
