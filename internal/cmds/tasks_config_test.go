package cmds

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadConfigBlobUnwrapsEnvelopeAndType(t *testing.T) {
	// `preview-fix` prints {type, config}; create-fix needs the inner blob,
	// and the type so a ticket draft isn't submitted as a PR.
	raw, draftedType, err := readConfigBlob("-", strings.NewReader(
		`{"type":"create_ticket","config":{"title":"Tag the bucket","body":"…"}}`))
	require.NoError(t, err)
	assert.Equal(t, "create_ticket", draftedType)
	assert.JSONEq(t, `{"title":"Tag the bucket","body":"…"}`, string(raw))
}

func TestReadConfigBlobBareConfigHasNoType(t *testing.T) {
	// A hand-written bare config carries no type; the caller then falls back
	// to its own default rather than inventing one.
	raw, draftedType, err := readConfigBlob("-", strings.NewReader(`{"branch":"x"}`))
	require.NoError(t, err)
	assert.Empty(t, draftedType)
	assert.JSONEq(t, `{"branch":"x"}`, string(raw))
}

func TestReadConfigBlobRejectsEmptyAndInvalid(t *testing.T) {
	_, _, err := readConfigBlob("-", strings.NewReader(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config is empty")

	_, _, err = readConfigBlob("-", strings.NewReader("not json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
}
