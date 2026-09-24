package main

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

// TestAudioStatusReportsOutputLatency checks the JSON contract for output latency:
// whole milliseconds when a native output is active, and absent otherwise.
func TestAudioStatusReportsOutputLatency(t *testing.T) {
	status := AudioSettings{Profile: "Automatic", SampleRate: 48000, BufferMS: 100}.status("auto")
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "output_latency_ms") {
		t.Fatalf("status without a native output reported latency: %s", data)
	}
	status.OutputLatencyMS = 100
	if data, err = json.Marshal(status); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"output_latency_ms":100`) {
		t.Fatalf("status did not report output latency: %s", data)
	}
}
