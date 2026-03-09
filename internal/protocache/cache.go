package protocache

import (
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/proto"
)

var ErrCacheMiss = fmt.Errorf("cache miss")

type Cache[T proto.Message] struct {
	// Dir overrides the cache directory. If empty, defaults to the user cache dir.
	Dir string
}

type Key string

func (c *Cache[T]) dir() (string, error) {
	cacheDir := c.Dir
	if cacheDir == "" {
		var err error
		cacheDir, err = os.UserCacheDir()
		if err != nil {
			cacheDir = os.TempDir()
		}
		cacheDir = filepath.Join(cacheDir, "infracost")
	}
	return cacheDir, os.MkdirAll(cacheDir, 0700)
}

func (c *Cache[T]) Load(key Key) (T, error) {
	var zero T
	dir, err := c.dir()
	if err != nil {
		return zero, fmt.Errorf("failed to get cache directory: %w", err)
	}
	cachePath := filepath.Join(dir, string(key))

	if _, err := os.Stat(cachePath); err != nil {
		if os.IsNotExist(err) {
			return zero, ErrCacheMiss
		}
		return zero, fmt.Errorf("failed to stat cache file: %w", err)
	}

	data, err := os.ReadFile(cachePath) //nolint:gosec // G304: cache path is derived from a controlled hash, not user input
	if err != nil {
		return zero, err
	}
	response := zero.ProtoReflect().New().Interface().(T)
	if err := proto.Unmarshal(data, response); err != nil {
		return zero, fmt.Errorf("failed to unmarshal cached response: %w", err)
	}
	return response, nil
}

func (c *Cache[T]) Save(key Key, response T) error {
	dir, err := c.dir()
	if err != nil {
		return fmt.Errorf("failed to get cache directory: %w", err)
	}
	cachePath := filepath.Join(dir, string(key))

	opts := proto.MarshalOptions{Deterministic: true}
	data, err := opts.Marshal(response)
	if err != nil {
		return fmt.Errorf("failed to marshal response for caching: %w", err)
	}
	if err := os.WriteFile(cachePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}
	return nil
}
