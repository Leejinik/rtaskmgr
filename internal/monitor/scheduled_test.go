package monitor

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
)

func TestParseScheduledOutputPreservesEndReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		running    int
		reason     string
		wantStatus string
	}{
		{name: "running", running: 1, wantStatus: "running"},
		{name: "deadline", reason: "deadline\n", wantStatus: "deadline"},
		{name: "low disk", reason: "low-disk\n", wantStatus: "low-disk"},
		{name: "signal", reason: "signal\n", wantStatus: "signal"},
		{name: "missing sidecar", wantStatus: "unknown"},
		{name: "unknown reason", reason: "unexpected\n", wantStatus: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			meta := RecMeta{ID: "rec-1", Status: "running"}
			metaJSON, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			reason := base64.StdEncoding.EncodeToString([]byte(tt.reason))
			out := fmt.Sprintf("STAT|rec-1|123|%d|%s\nMETA|%s\n", tt.running, reason, base64.StdEncoding.EncodeToString(metaJSON))

			got := parseScheduledOutput(out)
			if len(got) != 1 {
				t.Fatalf("parseScheduledOutput returned %d records, want 1", len(got))
			}
			if got[0].Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", got[0].Status, tt.wantStatus)
			}
			if got[0].SizeBytes != 123 {
				t.Fatalf("sizeBytes = %d, want 123", got[0].SizeBytes)
			}
		})
	}
}

func TestParseScheduledOutputTreatsInvalidOrLegacyReasonAsUnknown(t *testing.T) {
	t.Parallel()

	meta := RecMeta{ID: "rec-1", Status: "running"}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	encodedMeta := base64.StdEncoding.EncodeToString(metaJSON)

	tests := []struct {
		name string
		stat string
	}{
		{name: "invalid base64", stat: "STAT|rec-1|123|0|%%%"},
		{name: "legacy stat without reason", stat: "STAT|rec-1|123|0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseScheduledOutput(tt.stat + "\nMETA|" + encodedMeta + "\n")
			if len(got) != 1 {
				t.Fatalf("parseScheduledOutput returned %d records, want 1", len(got))
			}
			if got[0].Status != "unknown" {
				t.Fatalf("status = %q, want unknown", got[0].Status)
			}
		})
	}
}
