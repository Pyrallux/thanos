package discord

import (
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"

	"thanos/internal/orchestrator"
)

// BuildStatusEmbed creates the persistent status embed message that lists all
// managed servers and their current states with uptime for running servers.
//
// The embed is edited in-place (not re-posted) whenever a state change occurs
// or periodically (every 30s) to refresh uptime.
func BuildStatusEmbed(containers []*orchestrator.ContainerInfo) *discordgo.MessageEmbed {
	fields := []*discordgo.MessageEmbedField{}
	runningCount := 0
	crashedCount := 0
	for _, c := range containers {
		if c.State == orchestrator.StateRunning {
			runningCount++
		}
		if c.State == orchestrator.StateCrashed {
			crashedCount++
		}
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   stateEmoji(c.State) + " " + c.DisplayName,
			Value:  stateDescription(c),
			Inline: false,
		})
	}

	var title string
	if runningCount > 0 {
		title = fmt.Sprintf("Thanos — 🟢 %d of %d running", runningCount, len(containers))
	} else {
		title = fmt.Sprintf("Thanos — 🔴 All %d stopped", len(containers))
	}

	embedColor := 0x808080 // gray
	if crashedCount > 0 {
		embedColor = 0xFF0000 // red
	} else if runningCount > 0 {
		embedColor = 0x00FF00 // green
	}

	return &discordgo.MessageEmbed{
		Title:     title,
		Color:     embedColor,
		Timestamp: time.Now().Format(time.RFC3339),
		Fields:    fields,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Last Update",
		},
	}
}

// stateEmoji returns a unicode emoji representing the container state.
func stateEmoji(s orchestrator.State) string {
	switch s {
	case orchestrator.StateRunning:
		return "🟢"
	case orchestrator.StateDormant:
		return "🔴"
	case orchestrator.StateStarting:
		return "🟡"
	case orchestrator.StateStopping:
		return "🟠"
	case orchestrator.StateCrashed:
		return "⚠️"
	default:
		return "⚫"
	}
}

func stateDescription(c *orchestrator.ContainerInfo) string {
	switch c.State {
	case orchestrator.StateRunning:
		started := "just started"
		if !c.StartedAt.IsZero() {
			started = formatTimeAgo(time.Since(c.StartedAt))
		}
		return fmt.Sprintf("Uptime: %s", started)
	case orchestrator.StateDormant:
		return "Stopped"
	case orchestrator.StateStarting:
		return "Waking up..."
	case orchestrator.StateStopping:
		return "Shutting down..."
	case orchestrator.StateCrashed:
		return fmt.Sprintf("⚠ Crashed (exit %d) · Use /start %s", c.LastExitCode, c.DisplayName)
	default:
		return string(c.State)
	}
}

// formatTimeAgo renders a duration as a compact human-readable string.
func formatTimeAgo(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours()/24), int(d.Hours())%24)
}