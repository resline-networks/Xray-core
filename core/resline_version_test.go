package core_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/xtls/xray-core/core"
)

// `xray version` must say it is a resline build, but the marker has to stay
// out of the version field: 3x-ui takes the second space-separated field as
// the xray version, and the hteppl exporter turns it into a number, so it must
// remain plain X.Y.Z.
func TestVersionStatementCarriesReslineMarkerOutsideVersion(t *testing.T) {
	first := core.VersionStatement()[0]
	fields := strings.Split(first, " ")
	if len(fields) < 2 || fields[1] != core.Version() {
		t.Fatalf("second field of %q is not the version %q", first, core.Version())
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(fields[1]) {
		t.Fatalf("version field %q is not plain X.Y.Z", fields[1])
	}
	if !strings.Contains(first, "resline") {
		t.Fatalf("version statement %q has no resline marker", first)
	}
}
