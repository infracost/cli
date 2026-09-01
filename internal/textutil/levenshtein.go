// Package textutil holds small, dependency-free string helpers shared across
// the CLI, such as the edit-distance used for "did you mean" suggestions.
package textutil

// LevenshteinDistance computes the edit distance between two strings — the
// minimum number of single-character insertions, deletions, or substitutions
// needed to turn a into b.
func LevenshteinDistance(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// Single-row DP: prev holds the previous row of distances.
	prev := make([]int, lb+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr := make([]int, lb+1)
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(
				prev[j]+1,      // deletion
				curr[j-1]+1,    // insertion
				prev[j-1]+cost, // substitution
			)
		}
		prev = curr
	}

	return prev[lb]
}
