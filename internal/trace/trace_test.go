package trace

import (
	"regexp"
	"testing"
)

func TestTraceIdFormat(t *testing.T) {
	re := regexp.MustCompile(`^infracost-cliv2-[a-z0-9]{8}-[a-z0-9]{8}$`)
	if !re.MatchString(ID) {
		t.Errorf("TraceId %s does not match expected format ^infracost-cliv2-[a-z0-9]{8}-[a-z0-9]{8}$", ID)
	}
}
