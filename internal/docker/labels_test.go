package docker

import (
	"testing"
)

// TestParseLabelMap covers the label parsing logic that drives Thanos's
// per-container configuration: enabled flag, snap timeout hours→seconds
// conversion, the boolean toggles, and the TCP/UDP protocol filters.
func TestParseLabelMap(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]string
		want Labels
	}{
		{
			name: "empty map uses defaults",
			in:   map[string]string{},
			want: Labels{
				Enabled:        false,
				SnapTimeout:    int(DefaultSnapTimeoutHours * 3600),
				NotifyDiscord:  true,
				CrashDetection: true,
				WatchTCP:       true,
				WatchUDP:       true,
			},
		},
		{
			name: "enabled true (case-insensitive)",
			in:   map[string]string{LabelEnabled: "TRUE"},
			want: Labels{
				Enabled:        true,
				SnapTimeout:    int(DefaultSnapTimeoutHours * 3600),
				NotifyDiscord:  true,
				CrashDetection: true,
				WatchTCP:       true,
				WatchUDP:       true,
			},
		},
		{
			name: "snap timeout converts hours to seconds",
			in: map[string]string{
				LabelEnabled:     "true",
				LabelSnapTimeout: "2",
			},
			want: Labels{
				Enabled:        true,
				SnapTimeout:    7200, // 2h * 3600
				NotifyDiscord:  true,
				CrashDetection: true,
				WatchTCP:       true,
				WatchUDP:       true,
			},
		},
		{
			name: "snap timeout supports fractional hours",
			in: map[string]string{
				LabelEnabled:     "true",
				LabelSnapTimeout: "0.5",
			},
			want: Labels{
				Enabled:        true,
				SnapTimeout:    1800, // 0.5h * 3600
				NotifyDiscord:  true,
				CrashDetection: true,
				WatchTCP:       true,
				WatchUDP:       true,
			},
		},
		{
			name: "invalid snap timeout falls back to default",
			in: map[string]string{
				LabelEnabled:     "true",
				LabelSnapTimeout: "not-a-number",
			},
			want: Labels{
				Enabled:        true,
				SnapTimeout:    int(DefaultSnapTimeoutHours * 3600),
				NotifyDiscord:  true,
				CrashDetection: true,
				WatchTCP:       true,
				WatchUDP:       true,
			},
		},
		{
			name: "display name and toggles parsed",
			in: map[string]string{
				LabelEnabled:           "true",
				LabelDisplayName:       "My Server",
				LabelKeepRunningOnBoot: "true",
				LabelNotifyDiscord:     "false",
				LabelCrashDetection:    "false",
			},
			want: Labels{
				Enabled:           true,
				SnapTimeout:       int(DefaultSnapTimeoutHours * 3600),
				KeepRunningOnBoot: true,
				DisplayName:       "My Server",
				NotifyDiscord:     false,
				CrashDetection:    false,
				WatchTCP:          true,
				WatchUDP:          true,
			},
		},
		{
			name: "protocol filters parsed",
			in: map[string]string{
				LabelEnabled:  "true",
				LabelWatchTCP: "false",
				LabelWatchUDP: "false",
			},
			want: Labels{
				Enabled:        true,
				SnapTimeout:    int(DefaultSnapTimeoutHours * 3600),
				NotifyDiscord:  true,
				CrashDetection: true,
				WatchTCP:       false,
				WatchUDP:       false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseLabelMap(tt.in)
			if got != tt.want {
				t.Errorf("ParseLabelMap(%v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}
