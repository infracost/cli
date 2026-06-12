package plugins

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagerLoadParserPluginsMissingDir(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "missing"))
	require.NoError(t, err)

	plugins, err := mgr.LoadParserPlugins(context.Background())
	require.NoError(t, err)
	assert.Empty(t, plugins)
}
