package stacktrace

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/maruel/panicparse/v2/stack"
)

var goroutineSuffixRegex = regexp.MustCompile(`(goroutine)\s*\d+$`)

// Sanitize processes a raw stack trace to strip sensitive information before
// sending it to logging infrastructure. This removes absolute file paths,
// function arguments, and normalizes goroutine numbers so that equivalent
// stack traces always produce the same string.
//
// importPrefixes are module import path prefixes (e.g.
// "github.com/infracost/cli/") that will be stripped from file references to
// keep the output concise.
func Sanitize(raw []byte, importPrefixes ...string) string {
	sanitized, err := processStack(raw, importPrefixes)
	if err != nil {
		// If we can't parse the stack, return an empty string rather than
		// leaking potentially sensitive path information.
		return ""
	}
	return string(sanitized)
}

func processStack(rawStack []byte, importPrefixes []string) ([]byte, error) {
	stream := bytes.NewReader(rawStack)
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)

	s, suffix, err := stack.ScanSnapshot(stream, io.Discard, stack.DefaultOpts())
	if err != nil && err != io.EOF {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("failed to parse stack trace")
	}

	buckets := s.Aggregate(stack.AnyValue).Buckets

	srcLen := 0
	for _, bucket := range buckets {
		for _, line := range bucket.Stack.Calls {
			if l := len(fmt.Sprintf("%s/%s:%d", stripPrefixes(line.ImportPath, importPrefixes), line.SrcName, line.Line)); l > srcLen {
				srcLen = l
			}
		}
	}

	for _, bucket := range buckets {
		extra := ""
		if s := bucket.SleepString(); s != "" {
			extra += " [" + s + "]"
		}
		if bucket.Locked {
			extra += " [locked]"
		}

		if len(bucket.CreatedBy.Calls) != 0 {
			funcName := bucket.CreatedBy.Calls[0].Func.Name
			funcName = goroutineSuffixRegex.ReplaceAllString(funcName, "$1")
			extra += fmt.Sprintf(" [Created by %s.%s @ %s:%d]", bucket.CreatedBy.Calls[0].Func.DirName, funcName, bucket.CreatedBy.Calls[0].SrcName, bucket.CreatedBy.Calls[0].Line)
		}
		if _, err := fmt.Fprintf(w, "%d: %s%s\n", len(bucket.IDs), bucket.State, extra); err != nil {
			return nil, err
		}

		for _, line := range bucket.Stack.Calls {
			if _, err := fmt.Fprintf(w,
				"   %-*s %s()\n",
				srcLen,
				fmt.Sprintf("%s/%s:%d", stripPrefixes(line.ImportPath, importPrefixes), line.SrcName, line.Line),
				line.Func.Name); err != nil {
				return nil, err
			}
		}
		if bucket.Stack.Elided {
			if _, err := w.WriteString("    (...)\n"); err != nil {
				return nil, err
			}
		}
	}

	if len(suffix) != 0 {
		if _, err := w.Write(suffix); err != nil {
			return nil, err
		}
	}
	if _, err := io.Copy(w, stream); err != nil {
		return nil, err
	}

	if err := w.Flush(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func stripPrefixes(s string, prefixes []string) string {
	for _, prefix := range prefixes {
		s = strings.TrimPrefix(s, prefix)
	}
	return s
}