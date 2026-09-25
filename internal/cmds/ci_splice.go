package cmds

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ciBlockVersion is the shape of the generated job block. Bump it whenever the
// emitted YAML changes so an upgrade can tell old output from current.
const ciBlockVersion = 1

var (
	ciBlockStart = fmt.Sprintf("# >>> infracost ci setup v%d — managed block, edit outside these markers", ciBlockVersion)
	ciBlockEnd   = "# <<< infracost ci setup"

	ciBlockStartRe = regexp.MustCompile(`^\s*#\s*>>>\s*infracost ci setup\b`)
	ciBlockEndRe   = regexp.MustCompile(`^\s*#\s*<<<\s*infracost ci setup\b`)
	ciVersionRe    = regexp.MustCompile(`infracost ci setup v(\d+)`)
	ciKeyRe        = regexp.MustCompile(`^(\s*)([^\s#][^:]*):(?:\s|$)`)
)

// ciPlaceBlock splices body into repoRoot/relPath — under key when key is set,
// at the top level otherwise — through a temp file and rename. It refuses a
// file that already defines an Infracost job outside the managed block, and
// returns the block unwritten when the file's shape rules out a safe edit.
func ciPlaceBlock(repoRoot, relPath, key, body string) (ciWriteResult, error) {
	path := filepath.Join(repoRoot, filepath.FromSlash(relPath))

	var content string
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // G304: path is a registered config path under the repo root
		content = string(b)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ciWriteResult{}, fmt.Errorf("reading %s: %w", relPath, err)
	}

	block := ciManagedBlock(body)
	if reason := ciCanPlace(content); reason != nil {
		return ciWriteResult{path: relPath, block: block, reason: reason.Error()}, nil
	}
	if reason := ciCanReplaceSpan(content, body); reason != nil {
		return ciWriteResult{path: relPath, block: block, reason: reason.Error()}, nil
	}

	if existing, line := ciExistingInfracostJob(content, key); existing != "" {
		return ciWriteResult{}, fmt.Errorf(
			"%s already defines %q at line %d, outside the Infracost managed block — remove it and re-run, or keep editing it by hand",
			relPath, existing, line)
	}

	var updated string
	if key != "" {
		updated = ciSpliceUnderKey(content, key, block)
	} else {
		updated = ciSpliceTopLevel(content, block)
	}

	// A hand-removed end sentinel makes the splice append a second copy, so
	// never commit a result that no longer parses.
	var check map[any]any
	if err := yaml.Unmarshal([]byte(updated), &check); err != nil {
		return ciWriteResult{path: relPath, block: block, reason: fmt.Sprintf("adding the block would not leave valid YAML: %v", err)}, nil
	}

	created, unchanged, err := writeCIConfigFile(path, updated)
	if err != nil {
		return ciWriteResult{}, err
	}
	return ciWriteResult{path: relPath, created: created, unchanged: unchanged}, nil
}

// ciManagedBlock wraps body in the sentinel comments that make a re-run replace
// the block rather than append a second one.
func ciManagedBlock(body string) string {
	return ciBlockStart + "\n" + strings.TrimRight(body, "\n") + "\n" + ciBlockEnd + "\n"
}

// ciConfigVersion reports the block version marked in content, or 0 when
// content carries no Infracost marker.
func ciConfigVersion(content string) int {
	m := ciVersionRe.FindStringSubmatch(content)
	if m == nil {
		return 0
	}
	var v int
	if _, err := fmt.Sscanf(m[1], "%d", &v); err != nil {
		return 0
	}
	return v
}

// ciCanPlace reports why content is not a file the splice helpers can safely
// edit. Anchors, aliases and custom tags all move content the line-based splice
// cannot see, so the caller prints the block to paste instead of writing.
func ciCanPlace(content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}

	// Every other pass here reads document one, while the splice appends to the
	// last, so a second document would be edited blind.
	var docs []yaml.Node
	dec := yaml.NewDecoder(strings.NewReader(content))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("it is not valid YAML: %w", err)
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		return nil
	}
	if len(docs) > 1 {
		return fmt.Errorf("it holds %d YAML documents", len(docs))
	}

	root := docs[0]
	if len(root.Content) == 0 {
		return nil
	}
	if root.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("its top level is not a mapping")
	}

	var reason error
	walkYAMLNode(&root, func(n *yaml.Node) {
		if reason != nil {
			return
		}
		switch {
		case n.Kind == yaml.AliasNode:
			reason = fmt.Errorf("it uses YAML aliases")
		case n.Anchor != "":
			reason = fmt.Errorf("it uses YAML anchors")
		case strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!"):
			reason = fmt.Errorf("it uses the %s tag", n.Tag)
		}
	})
	return reason
}

// ciCanReplaceSpan reports why the managed span in content cannot be replaced
// wholesale. The span is found by line, so a duplicated marker or one that a
// merge or a block scalar left mispaired can enclose the user's own keys, and
// replacing it would delete them.
func ciCanReplaceSpan(content, body string) error {
	lines := strings.Split(content, "\n")

	starts, ends := 0, 0
	for _, l := range lines {
		switch {
		case ciBlockStartRe.MatchString(l):
			starts++
		case ciBlockEndRe.MatchString(l):
			ends++
		}
	}
	if starts > 1 || ends > 1 {
		return fmt.Errorf("it has %d Infracost start markers and %d end markers, so the managed block is ambiguous", starts, ends)
	}

	start, end, ok := findCIManagedSpan(lines)
	if !ok {
		return nil
	}

	ours := map[string]bool{}
	for _, k := range ciShallowestKeys(strings.Split(body, "\n")) {
		ours[k] = true
	}
	var foreign []string
	for _, k := range ciShallowestKeys(lines[start+1 : end-1]) {
		if !ours[k] && !strings.HasPrefix(strings.ToLower(k), "infracost") {
			foreign = append(foreign, k)
		}
	}
	if len(foreign) > 0 {
		return fmt.Errorf("the Infracost markers enclose %s, which Infracost did not write", strings.Join(foreign, ", "))
	}
	return nil
}

// ciShallowestKeys returns the mapping keys at the smallest indentation in
// lines, which for a managed span are the keys the block defines.
func ciShallowestKeys(lines []string) []string {
	indent := -1
	keys := make([]string, 0, len(lines))
	for _, l := range lines {
		m := ciKeyRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		switch n := len(m[1]); {
		case indent < 0 || n < indent:
			// A shallower level supersedes everything collected below it.
			indent, keys = n, append(keys[:0], m[2])
		case n == indent:
			keys = append(keys, m[2])
		}
	}
	return keys
}

// ciExistingInfracostJob reports an Infracost job defined outside the managed
// block, with the line it sits on. Appending beside it would duplicate the key
// and stop the whole pipeline parsing, so callers refuse rather than merge.
// Only the levels the block is spliced into are checked — a nested
// infracost_settings: elsewhere in the file is not a job.
func ciExistingInfracostJob(content, under string) (string, int) {
	start, end, hasBlock := findCIManagedSpan(strings.Split(content, "\n"))

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil || len(root.Content) == 0 {
		return "", 0
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return "", 0
	}

	inBlock := func(line int) bool { return hasBlock && line-1 >= start && line-1 < end }

	mappings := []*yaml.Node{doc}
	if n := ciNodeUnderKey(doc, under); n != nil {
		switch n.Kind {
		case yaml.MappingNode:
			mappings = append(mappings, n)
		case yaml.SequenceNode:
			// Azure's jobs: and stages: are sequences, so the name is a value
			// on the entry rather than the key it is stored under.
			if name, line := ciInfracostSeqEntry(n, inBlock); name != "" {
				return name, line
			}
		}
	}

	for _, m := range mappings {
		for i := 0; i+1 < len(m.Content); i += 2 {
			k, v := m.Content[i], m.Content[i+1]
			if !strings.HasPrefix(strings.ToLower(k.Value), "infracost") {
				continue
			}
			// A key mapping to a scalar is a variable (INFRACOST_API_KEY), not a job.
			if v.Kind != yaml.MappingNode && v.Kind != yaml.SequenceNode {
				continue
			}
			if inBlock(k.Line) {
				continue
			}
			return k.Value, k.Line
		}
	}
	return "", 0
}

// ciInfracostSeqEntry reports an Infracost-named entry in a sequence of
// mappings, by the fields Azure names a job, stage or deployment with.
func ciInfracostSeqEntry(seq *yaml.Node, inBlock func(int) bool) (string, int) {
	for _, item := range seq.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(item.Content); i += 2 {
			k, v := item.Content[i], item.Content[i+1]
			switch k.Value {
			case "job", "stage", "deployment":
			default:
				continue
			}
			if !strings.HasPrefix(strings.ToLower(v.Value), "infracost") || inBlock(k.Line) {
				continue
			}
			return v.Value, k.Line
		}
	}
	return "", 0
}

// ciMappingUnderKey returns the mapping doc[key] maps to, or nil.
func ciMappingUnderKey(doc *yaml.Node, key string) *yaml.Node {
	n := ciNodeUnderKey(doc, key)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	return n
}

// ciNodeUnderKey returns the value doc[key] maps to, of whatever kind, or nil.
func ciNodeUnderKey(doc *yaml.Node, key string) *yaml.Node {
	if key == "" {
		return nil
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == key {
			return doc.Content[i+1]
		}
	}
	return nil
}

// ciSpliceTopLevel adds block after the last top-level key, replacing an
// existing managed block in place so a re-run is idempotent.
func ciSpliceTopLevel(content, block string) string {
	lines := strings.Split(content, "\n")
	if start, end, ok := findCIManagedSpan(lines); ok {
		return replaceLines(lines, start, end, indentedBlockLines(block, 0))
	}

	body := strings.TrimRight(content, "\n")
	block = strings.TrimRight(block, "\n") + "\n"
	if strings.TrimSpace(body) == "" {
		return block
	}
	return body + "\n\n" + block
}

// ciSpliceUnderKey nests block beneath the top-level key at its children's
// indentation. The key is created at the end of the file when it is absent.
func ciSpliceUnderKey(content, key, block string) string {
	lines := strings.Split(content, "\n")
	if start, end, ok := findCIManagedSpan(lines); ok {
		return replaceLines(lines, start, end, indentedBlockLines(block, leadingSpaces(lines[start])))
	}

	keyLine, endLine, ok := ciTopLevelKeyRange(content, key)
	if !ok {
		return ciSpliceTopLevel(content, key+":\n"+indentLines(block, 2))
	}

	insertAt := min(endLine-1, len(lines))
	for insertAt > keyLine && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}

	indent := 0
	for i := keyLine; i < insertAt; i++ {
		if t := strings.TrimSpace(lines[i]); t != "" && !strings.HasPrefix(t, "#") {
			indent = leadingSpaces(lines[i])
			break
		}
	}
	if indent == 0 {
		indent = 2
	}

	return replaceLines(lines, insertAt, insertAt, indentedBlockLines(block, indent))
}

// writeCIConfigFile replaces path through a temp file and rename, so a crash
// mid-write leaves the original intact. A file that already exists keeps its
// mode, and one that already matches content is left untouched.
func writeCIConfigFile(path, content string) (created, unchanged bool, err error) {
	mode := os.FileMode(0o600)
	created = true
	if info, statErr := os.Stat(path); statErr == nil {
		created = false
		mode = info.Mode().Perm()
		//nolint:gosec // G304: path is a registered config path under the repo root
		if existing, readErr := os.ReadFile(path); readErr == nil && string(existing) == content {
			return false, true, nil
		}
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".infracost-ci-*")
	if err != nil {
		return false, false, fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return false, false, fmt.Errorf("writing %s: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return false, false, fmt.Errorf("closing %s: %w", tmpName, err)
	}
	if err = os.Chmod(tmpName, mode); err != nil {
		return false, false, fmt.Errorf("setting mode on %s: %w", path, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return false, false, fmt.Errorf("replacing %s: %w", path, err)
	}
	return created, false, nil
}

// findCIManagedSpan returns the half-open line range of the managed block.
func findCIManagedSpan(lines []string) (start, end int, ok bool) {
	start = -1
	for i, line := range lines {
		switch {
		case start < 0 && ciBlockStartRe.MatchString(line):
			start = i
		case start >= 0 && ciBlockEndRe.MatchString(line):
			return start, i + 1, true
		}
	}
	return 0, 0, false
}

// ciTopLevelKeyRange returns the 1-indexed line of key and of whatever top-level
// key follows it (one past the last line when it is the final key).
func ciTopLevelKeyRange(content, key string) (keyLine, endLine int, ok bool) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil || len(root.Content) == 0 {
		return 0, 0, false
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return 0, 0, false
	}

	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != key {
			continue
		}
		endLine = len(strings.Split(content, "\n")) + 1
		if i+2 < len(doc.Content) {
			endLine = doc.Content[i+2].Line
		}
		return doc.Content[i].Line, endLine, true
	}
	return 0, 0, false
}

func walkYAMLNode(n *yaml.Node, fn func(*yaml.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for _, c := range n.Content {
		walkYAMLNode(c, fn)
	}
}

func replaceLines(lines []string, start, end int, with []string) string {
	out := make([]string, 0, len(lines)-(end-start)+len(with))
	out = append(out, lines[:start]...)
	out = append(out, with...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}

func indentedBlockLines(block string, indent int) []string {
	return strings.Split(strings.TrimRight(indentLines(block, indent), "\n"), "\n")
}

func indentLines(s string, indent int) string {
	pad := strings.Repeat(" ", indent)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[i] = pad + line
		}
	}
	return strings.Join(lines, "\n")
}

func leadingSpaces(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}
