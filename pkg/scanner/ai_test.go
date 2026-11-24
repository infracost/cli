package scanner

import (
	"encoding/json"
	"testing"
)

func TestAIEnabled(t *testing.T) {
	for flags, want := range map[string]bool{
		"":                         false,
		`{"enableAiPlugin":false}`: false,
		`{"enableAiPlugin":true}`:  true,
		`{"enableArmPlugin":true}`: false,
		`{"enableAiPlugin":true,"someNewFlag":true}`: true,
		`{"enableAiPlugin":`:                         false,
	} {
		if got := AIEnabled(json.RawMessage(flags)); got != want {
			t.Errorf("AIEnabled(%q) = %v, want %v", flags, got, want)
		}
	}
}
