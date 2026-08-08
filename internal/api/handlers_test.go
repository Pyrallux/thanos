package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// TestSafeContainerName verifies that safeContainerName strips the leading
// "/" from Docker container names and handles empty slices. Docker prepends
// "/" to container names in its API responses; without stripping, the UI
// and API would display "/minecraft" instead of "minecraft".
func TestSafeContainerName(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{"empty", []string{}, ""},
		{"nil", nil, ""},
		{"single name", []string{"/minecraft"}, "minecraft"},
		{"name without slash", []string{"minecraft"}, "minecraft"},
		{"multiple names uses first", []string{"/mc", "/mc-alt"}, "mc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeContainerName(tt.names)
			if got != tt.want {
				t.Errorf("safeContainerName(%v) = %q, want %q", tt.names, got, tt.want)
			}
		})
	}
}

// TestComputeStats verifies the CPU and memory percentage calculation from
// Docker stats samples. A regression here would display wrong resource
// usage in the web UI server cards.
func TestComputeStats(t *testing.T) {
	stats := container.StatsResponse{
		CPUStats: container.CPUStats{
			CPUUsage: container.CPUUsage{
				TotalUsage: 2000,
			},
			SystemUsage: 10000,
			OnlineCPUs:  4,
		},
		PreCPUStats: container.CPUStats{
			CPUUsage: container.CPUUsage{
				TotalUsage: 1000,
			},
			SystemUsage: 5000,
		},
		MemoryStats: container.MemoryStats{
			Usage: 512 * 1024 * 1024,  // 512 MB
			Limit: 1024 * 1024 * 1024, // 1 GB
		},
	}

	got := computeStats(stats)

	// CPU: (2000-1000)/(10000-5000) * 4 * 100 = 80%
	if got["cpu"] != "80.0%" {
		t.Errorf("cpu = %v, want 80.0%%", got["cpu"])
	}
	// Memory: 512MB / 1024MB = 50%
	if got["mem_percent"] != "50.0%" {
		t.Errorf("mem_percent = %v, want 50.0%%", got["mem_percent"])
	}
	if !strings.Contains(got["mem"].(string), "512MB") {
		t.Errorf("mem = %v, want contains '512MB'", got["mem"])
	}
}

// TestComputeStatsZeroCPU verifies that when PreCPUStats is empty (the
// first stats sample from Docker), CPU is 0% and there's no division by
// zero. This is the common case for the one-shot stats query.
func TestComputeStatsZeroCPU(t *testing.T) {
	stats := container.StatsResponse{
		CPUStats: container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 1000},
			SystemUsage: 5000,
			OnlineCPUs:  2,
		},
		// PreCPUStats empty — TotalUsage=0
		MemoryStats: container.MemoryStats{
			Usage: 100 * 1024 * 1024,
			Limit: 2048 * 1024 * 1024,
		},
	}
	got := computeStats(stats)
	if got["cpu"] != "0.0%" {
		t.Errorf("cpu = %v, want 0.0%% (no pre-cpu data)", got["cpu"])
	}
}

// TestComputeStatsZeroMemLimit verifies that a zero memory limit doesn't
// cause a division by zero.
func TestComputeStatsZeroMemLimit(t *testing.T) {
	stats := container.StatsResponse{
		MemoryStats: container.MemoryStats{
			Usage: 100 * 1024 * 1024,
			Limit: 0,
		},
	}
	got := computeStats(stats)
	if got["mem_percent"] != "0.0%" {
		t.Errorf("mem_percent = %v, want 0.0%% (zero limit)", got["mem_percent"])
	}
}

// TestStripDockerHeader verifies that the 8-byte multiplexed stream header
// is correctly stripped from Docker log lines. Without this, log output in
// the web UI would contain binary garbage at the start of each line.
func TestStripDockerHeader(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"stdout header", "\x01\x00\x00\x00\x00\x00\x00\x08Hello world", "Hello world"},
		{"stderr header", "\x02\x00\x00\x00\x00\x00\x00\x05Error", "Error"},
		{"no header (plain text)", "just a log line", "just a log line"},
		{"short line", "hi", "hi"},
		{"header-like but wrong padding", "\x01\x00\x01\x00rest", "\x01\x00\x01\x00rest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripDockerHeader(tt.line)
			if got != tt.want {
				t.Errorf("stripDockerHeader = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestWriteJSON verifies that writeJSON sets the correct content type and
// status code, and encodes the body as JSON. This is used by every API
// handler — a regression would break all API responses.
func TestWriteJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusOK, map[string]string{"status": "ok"})

	if rr.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", rr.Header().Get("Content-Type"))
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", rr.Code, http.StatusOK)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("body[status] = %q, want %q", body["status"], "ok")
	}
}

// TestWriteJSONErrorStatus verifies that non-200 status codes are passed
// through correctly.
func TestWriteJSONErrorStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusBadRequest, map[string]string{"error": "bad request"})

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status code = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if body["error"] != "bad request" {
		t.Errorf("body[error] = %q, want %q", body["error"], "bad request")
	}
}
