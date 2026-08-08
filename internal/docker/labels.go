package docker

import (
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// Label keys defined by the Thanos spec.
const (
	LabelEnabled           = "thanos.enabled"
	LabelSnapTimeout       = "thanos.snap_timeout"
	LabelKeepRunningOnBoot = "thanos.keep_running_on_boot"
	LabelDisplayName       = "thanos.display_name"
	LabelNotifyDiscord     = "thanos.notify_discord"
	LabelCrashDetection    = "thanos.crash_detection"
	LabelWatchTCP          = "thanos.watch_tcp"
	LabelWatchUDP          = "thanos.watch_udp"
)

// Labels holds the parsed Thanos configuration for a single container.
type Labels struct {
	Enabled           bool
	SnapTimeout       int // seconds (converted from hours in the label); 0 = never auto-shutdown
	KeepRunningOnBoot bool
	DisplayName       string
	NotifyDiscord     bool
	CrashDetection    bool
	WatchTCP          bool // wake/idle-reset on TCP connections to this container's ports
	WatchUDP          bool // wake/idle-reset on UDP traffic to this container's ports
}

// DefaultSnapTimeoutHours is the default snap timeout in hours.
const DefaultSnapTimeoutHours = 0.25 // 15 minutes

// ParseLabels reads Thanos labels from a container's Labels map.
func ParseLabels(c container.Summary) Labels {
	m := labelMap(c)
	return ParseLabelMap(m)
}

// ParseLabelMap reads Thanos labels from a raw map[string]string.
// The thanos.snap_timeout label is specified in hours (decimals supported)
// and converted to seconds internally.
func ParseLabelMap(m map[string]string) Labels {
	l := Labels{
		Enabled:           strings.EqualFold(m[LabelEnabled], "true"),
		SnapTimeout:       int(DefaultSnapTimeoutHours * 3600),
		KeepRunningOnBoot: strings.EqualFold(m[LabelKeepRunningOnBoot], "true"),
		DisplayName:       m[LabelDisplayName],
		NotifyDiscord:     true,
		CrashDetection:    true,
		WatchTCP:          true,
		WatchUDP:          true,
	}
	if v := m[LabelSnapTimeout]; v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			l.SnapTimeout = int(n * 3600) // convert hours to seconds
		}
	}
	if v := m[LabelNotifyDiscord]; v != "" {
		l.NotifyDiscord = strings.EqualFold(v, "true")
	}
	if v := m[LabelCrashDetection]; v != "" {
		l.CrashDetection = strings.EqualFold(v, "true")
	}
	if v := m[LabelWatchTCP]; v != "" {
		l.WatchTCP = strings.EqualFold(v, "true")
	}
	if v := m[LabelWatchUDP]; v != "" {
		l.WatchUDP = strings.EqualFold(v, "true")
	}
	return l
}

// labelMap returns the effective label map for a container.
func labelMap(c container.Summary) map[string]string {
	if c.Labels != nil {
		return c.Labels
	}
	return map[string]string{}
}