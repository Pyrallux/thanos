package discord

import (
	"fmt"

	"github.com/bwmarrin/discordgo"

	"thanos/internal/orchestrator"
)

// WakeNotification builds the embed for a wake/start event.
func WakeNotification(ci *orchestrator.ContainerInfo) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "Server Starting",
		Description:  fmt.Sprintf("**%s** is starting up.", ci.DisplayName),
		Color:       0xFFFF00,
	}
}

// IdleShutdownNotification builds the embed for an idle shutdown (snap) event.
// The timeout parameter is in seconds; it is displayed in minutes.
func IdleShutdownNotification(ci *orchestrator.ContainerInfo, activeDuration string, timeoutSeconds int) *discordgo.MessageEmbed {
	timeoutMinutes := timeoutSeconds / 60
	return &discordgo.MessageEmbed{
		Title:       "Idle Shutdown",
		Description:  fmt.Sprintf("**%s** has been snapped (idle shutdown).\nActive for %s. No traffic for %dm.", ci.DisplayName, activeDuration, timeoutMinutes),
		Color:       0x808080,
	}
}

// ManualStopNotification builds the embed for a manually triggered stop.
func ManualStopNotification(ci *orchestrator.ContainerInfo) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "Manual Stop",
		Description:  fmt.Sprintf("**%s** has been stopped manually.", ci.DisplayName),
		Color:       0x808080,
	}
}

// CrashNotification builds the embed for a crash event.
func CrashNotification(ci *orchestrator.ContainerInfo, exitCode int) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "Server Crashed",
		Description:  fmt.Sprintf("**%s** has crashed unexpectedly.\nExit Code: %d\nThe server has been left stopped to protect save data.\nUse /start %s to restart manually.", ci.DisplayName, exitCode, ci.DisplayName),
		Color:       0xFF0000,
	}
}