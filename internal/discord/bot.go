// Package discord implements the Thanos Discord bot: persistent status embed,
// event notifications, and slash commands.
package discord

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"thanos/internal/config"
	"thanos/internal/orchestrator"
)

// Bot wraps the discordgo session and Thanos integration.
type Bot struct {
	cfg  *config.Config
	orch *orchestrator.Orchestrator

	mu      sync.Mutex
	session *discordgo.Session

	// embedMu serializes refreshStatusEmbed calls so that concurrent
	// watcher notifications don't create duplicate embed messages.
	embedMu sync.Mutex

	// statusMsgID is the ID of the persistent status embed message.
	// Stored in the discord_config table via cfg.DiscordStatusMsgID.
	statusMsgID string

	// stopStatusTicker cancels the periodic status update goroutine.
	stopStatusTicker chan struct{}
}

// New validates the Discord token and creates a Bot.
func New(cfg *config.Config, orch *orchestrator.Orchestrator) (*Bot, error) {
	return &Bot{
		cfg:              cfg,
		orch:             orch,
		statusMsgID:      cfg.DiscordStatusMsgID,
		stopStatusTicker: make(chan struct{}),
	}, nil
}

// Run opens the Discord gateway, registers slash commands, maintains the
// persistent status embed, and listens for state changes until the context
// is cancelled.
func (b *Bot) Run(ctx context.Context) {
	slog.Info("discord bot starting", "guild", b.cfg.DiscordGuildID)

	session, err := discordgo.New("Bot " + b.cfg.DiscordBotToken)
	if err != nil {
		slog.Error("discord session creation failed", "err", err)
		return
	}

	session.Identify.Intents = discordgo.IntentGuildMessages
	session.AddHandler(b.onInteractionCreate)

	if err := session.Open(); err != nil {
		slog.Error("discord gateway connection failed", "err", err)
		return
	}
	defer session.Close()

	b.mu.Lock()
	b.session = session
	b.mu.Unlock()

	// Register slash commands.
	b.registerCommands(session)

	// Register as a state watcher to receive container state changes.
	b.orch.RegisterWatcher(b)

	// Clear previous messages in the status channel before posting the
	// fresh status embed.
	b.clearStatusChannel()

	// Post or update the initial status embed.
	b.refreshStatusEmbed()

	// Start periodic status updates (for uptime refresh).
	go b.statusTicker(ctx)

	slog.Info("discord bot connected", "user", session.State.User.Username)

	<-ctx.Done()
	slog.Info("discord bot shutting down")
	close(b.stopStatusTicker)

	// Save the status message ID for next startup.
	if b.statusMsgID != "" && b.statusMsgID != b.cfg.DiscordStatusMsgID {
		b.cfg.DiscordStatusMsgID = b.statusMsgID
		if err := b.cfg.SaveDiscord(); err != nil {
			slog.Warn("failed to save discord status msg id", "err", err)
		}
	}
}

// statusTicker periodically refreshes the status embed to update uptime.
func (b *Bot) statusTicker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.stopStatusTicker:
			return
		case <-ticker.C:
			b.refreshStatusEmbed()
		}
	}
}

// onInteractionCreate handles slash command interactions.
func (b *Bot) onInteractionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}

	switch i.ApplicationCommandData().Name {
	case "status":
		b.cmdStatus(s, i)
	case "start":
		b.cmdStart(s, i)
	case "stop", "snap":
		b.cmdStop(s, i)
	case "config":
		b.cmdConfig(s, i)
	case "setstatuschannel":
		b.cmdSetStatusChannel(s, i)
	case "setlogchannel":
		b.cmdSetLogChannel(s, i)
	}
}

// registerCommands registers all slash commands with the configured guild.
func (b *Bot) registerCommands(session *discordgo.Session) {
	commands := b.slashCommandDefinitions()
	for _, cmd := range commands {
		_, err := session.ApplicationCommandCreate(session.State.User.ID, b.cfg.DiscordGuildID, cmd)
		if err != nil {
			slog.Error("failed to register slash command", "cmd", cmd.Name, "err", err)
		} else {
			slog.Info("registered slash command", "cmd", cmd.Name)
		}
	}
}

// slashCommandDefinitions returns all slash command definitions.
func (b *Bot) slashCommandDefinitions() []*discordgo.ApplicationCommand {
	return []*discordgo.ApplicationCommand{
		{
			Name:        "status",
			Description: "List all managed servers and their current states",
		},
		{
			Name:        "start",
			Description: "Manually wake a dormant server",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "name",
					Description: "Server display name or container name",
					Required:    true,
				},
			},
		},
		{
			Name:        "stop",
			Description: "Force-stop (snap) a running server",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "name",
					Description: "Server display name or container name",
					Required:    true,
				},
			},
		},
		{
			Name:        "snap",
			Description: "Alias for /stop — force-stop a running server",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "name",
					Description: "Server display name or container name",
					Required:    true,
				},
			},
		},
		{
			Name:        "config",
			Description: "Show current Thanos configuration",
		},
		{
			Name:        "setstatuschannel",
			Description: "Set or disable the channel for the persistent status embed",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionChannel,
					Name:        "channel",
					Description: "Channel for the status embed (omit to disable)",
					Required:    false,
				},
			},
		},
		{
			Name:        "setlogchannel",
			Description: "Set or disable the channel for snap/wake event notifications",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionChannel,
					Name:        "channel",
					Description: "Channel for event logs (omit to disable)",
					Required:    false,
				},
			},
		},
	}
}

// ── Slash Command Handlers ──

func (b *Bot) cmdStatus(s *discordgo.Session, i *discordgo.InteractionCreate) {
	embed := BuildStatusEmbed(b.orch.Containers())
	respondEmbed(s, i, embed)
}

func (b *Bot) cmdStart(s *discordgo.Session, i *discordgo.InteractionCreate) {
	name := i.ApplicationCommandData().Options[0].StringValue()
	ci := b.orch.FindByName(name)
	if ci == nil {
		respondText(s, i, fmt.Sprintf("❌ Server \"%s\" not found.", name))
		return
	}
	// Respond immediately — Discord interaction tokens expire after 3s.
	respondText(s, i, fmt.Sprintf("🟡 Waking up \"%s\"...", name))
	go func() {
		if err := b.orch.WakeContainer(context.Background(), ci.ID, "discord_start"); err != nil {
			slog.Warn("discord: failed to start container", "name", name, "err", err)
		}
	}()
}

func (b *Bot) cmdStop(s *discordgo.Session, i *discordgo.InteractionCreate) {
	name := i.ApplicationCommandData().Options[0].StringValue()
	ci := b.orch.FindByName(name)
	if ci == nil {
		respondText(s, i, fmt.Sprintf("❌ Server \"%s\" not found.", name))
		return
	}
	// Respond immediately — Discord interaction tokens expire after 3s.
	respondText(s, i, fmt.Sprintf("🟠 Snapping \"%s\"...", name))
	go func() {
		if err := b.orch.Snap(context.Background(), ci.ID, "discord_stop"); err != nil {
			slog.Warn("discord: failed to stop container", "name", name, "err", err)
		}
	}()
}

func (b *Bot) cmdConfig(s *discordgo.Session, i *discordgo.InteractionCreate) {
	statusCh := b.cfg.DiscordChannelID
	logCh := b.cfg.DiscordLogChannelID
	if statusCh == "" {
		statusCh = "(not set)"
	}
	if logCh == "" {
		logCh = "(not set)"
	}
	embed := &discordgo.MessageEmbed{
		Title: "⚙️ Thanos Configuration",
		Color: 0x8a6fd1,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Network Interface", Value: b.cfg.NetworkInterface, Inline: true},
			{Name: "API Port", Value: fmt.Sprintf("%d", b.cfg.APIPort), Inline: true},
			{Name: "Status Channel", Value: fmt.Sprintf("<#%s>", statusCh), Inline: false},
			{Name: "Log Channel", Value: fmt.Sprintf("<#%s>", logCh), Inline: false},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Use /setstatuschannel and /setlogchannel to configure",
		},
	}
	respondEmbed(s, i, embed)
}

func (b *Bot) cmdSetStatusChannel(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if len(i.ApplicationCommandData().Options) == 0 {
		// Disable status embed.
		if b.statusMsgID != "" && b.cfg.DiscordChannelID != "" {
			_ = s.ChannelMessageDelete(b.cfg.DiscordChannelID, b.statusMsgID)
		}
		b.cfg.DiscordChannelID = ""
		b.statusMsgID = ""
		_ = b.cfg.SaveDiscord()
		respondText(s, i, "✅ Status embed disabled.")
		return
	}

	// ChannelValue may return nil if the channel isn't in the state cache.
	// Fall back to reading the raw channel ID from the option value.
	opt := i.ApplicationCommandData().Options[0]
	channelID := ""
	if opt.ChannelValue(s) != nil {
		channelID = opt.ChannelValue(s).ID
	} else if str, ok := opt.Value.(string); ok {
		channelID = str
	}
	if channelID == "" {
		respondText(s, i, "❌ Invalid channel.")
		return
	}

	// Delete the old status message if it exists.
	if b.statusMsgID != "" && b.cfg.DiscordChannelID != "" {
		_ = s.ChannelMessageDelete(b.cfg.DiscordChannelID, b.statusMsgID)
	}

	b.cfg.DiscordChannelID = channelID
	b.statusMsgID = ""
	_ = b.cfg.SaveDiscord()
	b.refreshStatusEmbed()

	respondText(s, i, fmt.Sprintf("✅ Status embed set to <#%s>.", channelID))
}

func (b *Bot) cmdSetLogChannel(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if len(i.ApplicationCommandData().Options) == 0 {
		b.cfg.DiscordLogChannelID = ""
		_ = b.cfg.SaveKV("discord_log_channel_id", "")
		respondText(s, i, "✅ Log channel disabled.")
		return
	}

	// ChannelValue may return nil if the channel isn't in the state cache.
	// Fall back to reading the raw channel ID from the option value.
	opt := i.ApplicationCommandData().Options[0]
	channelID := ""
	if opt.ChannelValue(s) != nil {
		channelID = opt.ChannelValue(s).ID
	} else if str, ok := opt.Value.(string); ok {
		channelID = str
	}
	if channelID == "" {
		respondText(s, i, "❌ Invalid channel.")
		return
	}

	b.cfg.DiscordLogChannelID = channelID
	_ = b.cfg.SaveKV("discord_log_channel_id", channelID)

	slog.Info("discord: log channel set", "channel_id", channelID)
	respondText(s, i, fmt.Sprintf("✅ Log channel set to <#%s>.", channelID))
}

// ── State Watcher ──

// OnStateChange implements orchestrator.StateWatcher. When a container
// changes state, the bot posts an event notification to the log channel
// and updates the persistent status embed.
func (b *Bot) OnStateChange(ci *orchestrator.ContainerInfo) {
	slog.Info("discord: state change", "container", ci.DisplayName, "state", ci.State)
	b.postEventNotification(ci)
	b.refreshStatusEmbed()
}

// postEventNotification sends a message to the log channel when a container
// is snapped (idle shutdown), is starting up (wake), or crashes.
func (b *Bot) postEventNotification(ci *orchestrator.ContainerInfo) {
	b.mu.Lock()
	session := b.session
	logChannelID := b.cfg.DiscordLogChannelID
	b.mu.Unlock()

	slog.Debug("discord: postEventNotification called",
		"container", ci.DisplayName, "state", ci.State,
		"session_nil", session == nil, "log_channel_id", logChannelID)

	if session == nil || logChannelID == "" {
		return
	}

	switch ci.State {
	case orchestrator.StateStopping:
		activeDuration := "unknown"
		if !ci.StartedAt.IsZero() {
			activeDuration = formatDuration(time.Since(ci.StartedAt))
		}
		var embed *discordgo.MessageEmbed
		if ci.StopReason == "manual_stop" || ci.StopReason == "discord_stop" {
			embed = ManualStopNotification(ci)
		} else {
			embed = IdleShutdownNotification(ci, activeDuration, ci.SnapTimeout)
		}
		if _, err := session.ChannelMessageSendEmbed(logChannelID, embed); err != nil {
			slog.Warn("discord: failed to send snap notification", "err", err)
		}

	case orchestrator.StateStarting:
		embed := WakeNotification(ci)
		if _, err := session.ChannelMessageSendEmbed(logChannelID, embed); err != nil {
			slog.Warn("discord: failed to send wake notification", "err", err)
		}

	case orchestrator.StateCrashed:
		embed := CrashNotification(ci, ci.LastExitCode)
		if _, err := session.ChannelMessageSendEmbed(logChannelID, embed); err != nil {
			slog.Warn("discord: failed to send crash notification", "err", err)
		}
	}
}

// refreshStatusEmbed posts (or edits) the persistent status embed in the
// configured status channel. If no channel is configured, it does nothing.
func (b *Bot) refreshStatusEmbed() {
	b.embedMu.Lock()
	defer b.embedMu.Unlock()

	b.mu.Lock()
	session := b.session
	channelID := b.cfg.DiscordChannelID
	msgID := b.statusMsgID
	b.mu.Unlock()

	if session == nil || channelID == "" {
		return
	}

	embed := BuildStatusEmbed(b.orch.Containers())

	if msgID != "" {
		_, err := session.ChannelMessageEditEmbed(channelID, msgID, embed)
		if err != nil {
			slog.Debug("discord: failed to edit status embed, recreating", "err", err)
			msg, err := session.ChannelMessageSendEmbed(channelID, embed)
			if err != nil {
				slog.Warn("discord: failed to send status embed", "err", err)
				return
			}
			b.mu.Lock()
			b.statusMsgID = msg.ID
			b.mu.Unlock()
		}
	} else {
		msg, err := session.ChannelMessageSendEmbed(channelID, embed)
		if err != nil {
			slog.Warn("discord: failed to send status embed", "err", err)
			return
		}
		b.mu.Lock()
		b.statusMsgID = msg.ID
		b.mu.Unlock()

		b.cfg.DiscordStatusMsgID = msg.ID
		if err := b.cfg.SaveDiscord(); err != nil {
			slog.Warn("discord: failed to save status msg id", "err", err)
		}
	}
}

// ── Helpers ──

// clearStatusChannel removes all messages from the configured status channel.
// This is called on startup so the channel only contains the current status
// embed.
func (b *Bot) clearStatusChannel() {
	b.mu.Lock()
	session := b.session
	channelID := b.cfg.DiscordChannelID
	b.mu.Unlock()

	if session == nil || channelID == "" {
		return
	}

	// Bulk-delete messages from the last 100 (Discord API limit per request).
	// Repeat until the channel is empty.
	for {
		msgs, err := session.ChannelMessages(channelID, 100, "", "", "")
		if err != nil {
			slog.Warn("discord: failed to list status channel messages", "err", err)
			return
		}
		if len(msgs) == 0 {
			return
		}

		ids := make([]string, 0, len(msgs))
		for _, m := range msgs {
			ids = append(ids, m.ID)
		}
		if err := session.ChannelMessagesBulkDelete(channelID, ids); err != nil {
			slog.Warn("discord: failed to bulk-delete messages", "err", err)
			return
		}
		slog.Info("discord: cleared status channel messages", "count", len(ids))

		if len(msgs) < 100 {
			return
		}
	}
}

func respondText(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg},
	})
	if err != nil {
		slog.Warn("discord: failed to respond to interaction", "err", err)
	}
}

func respondEmbed(s *discordgo.Session, i *discordgo.InteractionCreate, embed *discordgo.MessageEmbed) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{embed}},
	})
	if err != nil {
		slog.Warn("discord: failed to respond to interaction", "err", err)
	}
}

// formatDuration renders a duration as a human-readable string like
// "2h 15m" or "45s".
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}