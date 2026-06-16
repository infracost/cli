package cmds

import (
	"io"

	"github.com/infracost/cli/internal/config"
)

// Renderers bundles the three output renderers a structured command produces:
// a human-readable terminal renderer, a JSON renderer for machine consumers,
// and an LLM renderer for token-efficient prompt piping.
//
// Commands following the typed-result pattern build a Renderers[T] for their
// result type and hand it to writeStructured along with the result.
type Renderers[T any] struct {
	Human func(w io.Writer, v T) error
	JSON  func(w io.Writer, v T) error
	LLM   func(w io.Writer, v T) error
}

// writeStructured dispatches v to the renderer matching the active output
// flag: --llm wins over --json, and otherwise falls back to the human
// renderer.
func writeStructured[T any](cfg *config.Config, w io.Writer, v T, r Renderers[T]) error {
	switch {
	case cfg.LLM.Value:
		return r.LLM(w, v)
	case cfg.JSON.Value:
		return r.JSON(w, v)
	default:
		return r.Human(w, v)
	}
}