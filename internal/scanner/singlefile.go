package scanner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// sniffBytes is how much of a file is read to tell what it is. Every marker
// below is within the first few bytes; the rest is only used for the UTF-8
// check, where a prefix is enough to be sure.
const sniffBytes = 512

// Byte-order marks. PowerShell's `>` redirection writes UTF-16LE with a BOM by
// default on Windows PowerShell 5.1, which is the shell the docs' own
// `terraform show -json > plan.json` is pasted into.
var (
	bomUTF8    = []byte{0xEF, 0xBB, 0xBF}
	bomUTF16LE = []byte{0xFF, 0xFE}
	bomUTF16BE = []byte{0xFE, 0xFF}

	// zipMagic starts a binary Terraform plan file, which is a zip archive.
	// Passing one to `infracost scan` instead of its JSON rendering is the
	// other half of this error.
	zipMagic = []byte{'P', 'K', 0x03, 0x04}
)

// samePath compares two absolute paths the way the filesystem does. Windows
// and macOS are case-insensitive, so a project discovered as C:\Work\plan.json
// must match a target typed as c:\work\plan.json — otherwise single-file mode
// finds the project, discards it, and reports the file as unscannable.
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// unscannableFileError explains why a single file produced no project. The
// bare "not a recognized scannable type" it replaces was the same message for
// a wrong file type, a binary plan and a file in an encoding the parsers do
// not read, and named no way forward for any of them.
//
// UTF-16 and BOM-prefixed plans are read by the parser plugins from the
// release that fixed them, so reaching one of those cases now means the
// installed plugin predates it: updating is the first thing to try.
func unscannableFileError(path string) error {
	head, err := readHead(path)
	if err != nil {
		return fmt.Errorf("file at %q is not a recognized scannable type", path)
	}

	switch {
	case bytes.HasPrefix(head, zipMagic):
		return fmt.Errorf("file at %q is a binary Terraform plan, not its JSON form. "+
			"Render it first:\n  terraform show -json plan.binary > plan.json", path)

	case bytes.HasPrefix(head, bomUTF16LE), bytes.HasPrefix(head, bomUTF16BE), isUTF16NoBOM(head):
		return fmt.Errorf("file at %q is UTF-16 encoded, which this parser plugin cannot read. "+
			"Run 'infracost plugin update', or re-render the file as UTF-8:\n"+
			"  terraform show -json plan.binary | Set-Content -Encoding utf8 plan.json", path)

	case bytes.HasPrefix(head, bomUTF8):
		return fmt.Errorf("file at %q starts with a UTF-8 byte-order mark, which this parser plugin cannot read. "+
			"Run 'infracost plugin update', or re-render the file without one:\n"+
			"  terraform show -json plan.binary | Set-Content -Encoding utf8NoBOM plan.json", path)

	case bytes.IndexByte(head, 0) >= 0:
		return fmt.Errorf("file at %q is not text, so no parser recognizes it. "+
			"If this is a Terraform plan, render it first:\n  terraform show -json plan.binary > plan.json", path)
	}

	return fmt.Errorf("file at %q is not a recognized scannable type", path)
}

// isUTF16NoBOM reports ASCII text encoded as UTF-16 without a byte-order
// mark, which is every other byte NUL. Redirecting through a pipe on Windows
// drops the BOM but keeps the encoding, so this is the shape a plan rendered
// with `terraform show -json | Out-File` arrives in.
func isUTF16NoBOM(head []byte) bool {
	// Four code units is enough to be sure, and short of that the NUL check
	// that follows covers it anyway.
	if len(head) < 8 {
		return false
	}

	// Compare both alignments: little-endian puts the NUL second, big-endian
	// first. An odd length cannot be UTF-16, so the last byte is ignored.
	pairs := len(head) / 2 * 2
	var le, be int
	for i := 0; i < pairs; i += 2 {
		if head[i+1] == 0 && head[i] != 0 {
			le++
		}
		if head[i] == 0 && head[i+1] != 0 {
			be++
		}
	}

	return le == pairs/2 || be == pairs/2
}

// readHead reads the first sniffBytes of a file, or all of it when shorter.
func readHead(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- the path is the scan target the user named
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	head := make([]byte, sniffBytes)
	// ReadFull rather than Read: a file on a network share can return fewer
	// bytes than the sniff needs on the first call.
	n, err := io.ReadFull(f, head)
	// A short file, including an empty one, is a diagnosis in itself rather
	// than a failure to read.
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	if n == 0 && err != nil {
		return nil, err
	}
	return head[:n], nil
}
