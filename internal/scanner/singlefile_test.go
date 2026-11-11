package scanner

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSamePath(t *testing.T) {
	t.Run("identical paths match everywhere", func(t *testing.T) {
		assert.True(t, samePath("/work/plan.json", "/work/plan.json"))
	})

	t.Run("different paths never match", func(t *testing.T) {
		assert.False(t, samePath("/work/plan.json", "/work/other.json"))
	})

	t.Run("case follows the filesystem", func(t *testing.T) {
		got := samePath("/Work/Plan.json", "/work/plan.json")
		caseInsensitive := runtime.GOOS == "windows" || runtime.GOOS == "darwin"
		assert.Equal(t, caseInsensitive, got)
	})
}

// utf16LE encodes ASCII as UTF-16LE, the way Windows PowerShell writes a
// redirect.
func utf16LE(s string, bom bool) []byte {
	var out []byte
	if bom {
		out = append(out, 0xFF, 0xFE)
	}
	for _, r := range []byte(s) {
		out = append(out, r, 0x00)
	}
	return out
}

func TestUnscannableFileError(t *testing.T) {
	plan := `{"format_version":"1.2","terraform_version":"1.9.0","planned_values":{}}`

	tests := []struct {
		name    string
		content []byte
		wantIn  []string
	}{
		{
			name:    "binary terraform plan",
			content: []byte("PK\x03\x04rest of the archive"),
			wantIn:  []string{"binary Terraform plan", "terraform show -json"},
		},
		{
			name:    "utf-16le with a BOM",
			content: utf16LE(plan, true),
			wantIn:  []string{"UTF-16", "Set-Content -Encoding utf8"},
		},
		{
			name:    "utf-16be with a BOM",
			content: append([]byte{0xFE, 0xFF}, utf16LE(plan, false)...),
			wantIn:  []string{"UTF-16"},
		},
		{
			name:    "utf-16 without a BOM",
			content: utf16LE(plan, false),
			wantIn:  []string{"is UTF-16 encoded"},
		},
		{
			name:    "utf-8 BOM",
			content: append([]byte{0xEF, 0xBB, 0xBF}, []byte(plan)...),
			wantIn:  []string{"byte-order mark", "utf8NoBOM"},
		},
		{
			name:    "binary that is not a plan",
			content: []byte("\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00"),
			wantIn:  []string{"is not text"},
		},
		{
			name:    "plain text the parsers simply do not handle",
			content: []byte("resource \"aws_instance\" \"web\" {}\n"),
			wantIn:  []string{"not a recognized scannable type"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "plan.json")
			require.NoError(t, os.WriteFile(path, tt.content, 0600))

			err := unscannableFileError(path)

			require.Error(t, err)
			assert.Contains(t, err.Error(), path)
			for _, want := range tt.wantIn {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestUnscannableFileError_UnreadableFile(t *testing.T) {
	err := unscannableFileError(filepath.Join(t.TempDir(), "gone.json"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a recognized scannable type")
}

func TestIsUTF16NoBOM(t *testing.T) {
	t.Run("ascii is not utf-16", func(t *testing.T) {
		assert.False(t, isUTF16NoBOM([]byte(strings.Repeat("a", 64))))
	})

	t.Run("too short to tell", func(t *testing.T) {
		assert.False(t, isUTF16NoBOM(utf16LE("ab", false)))
	})

	t.Run("utf-16le is detected", func(t *testing.T) {
		assert.True(t, isUTF16NoBOM(utf16LE(`{"a":1}`, false)))
	})
}
