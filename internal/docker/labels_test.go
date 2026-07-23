package docker

import (
	"testing"
)

// TestParseLabelMap covers the label parsing logic that drives Thanos's
// per-container configuration: enabled flag, snap timeout hours→seconds
// conversion, and the boolean toggles. This is a small, dependency-free
// unit that the CI test job can exercise to verify the build runs tests.
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
