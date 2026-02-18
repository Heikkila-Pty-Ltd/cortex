package matrix

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
)

const (
	defaultPollInterval = 30 * time.Second
	defaultWorkDir      = "/tmp"
	defaultThinking     = "none"
)

// InboundMessage is a normalized inbound Matrix message.
type InboundMessage struct {
	ID        string
	Project   string
	Room      string
	Sender    string
	Body      string
	Timestamp time.Time
}

// Client reads inbound messages for a Matrix room.
type Client interface {
	ReadMessages(ctx context.Context, roomID string, after string) ([]InboundMessage, string, error)
}

// PollerConfig controls inbound polling and routing behavior.
type PollerConfig struct {
	Enabled       bool
	PollInterval  time.Duration
	BotUser       string
	RoomToProject map[string]string
}

// Poller polls Matrix rooms and routes inbound messages to project scrum agents.
type Poller struct {
	cfg        PollerConfig
	client     Client
	dispatcher dispatch.DispatcherInterface
	logger     *slog.Logger

	mu      sync.Mutex
	cursors map[string]string // room -> last cursor/message id
}

// NewPoller constructs a Matrix poller.
func NewPoller(cfg PollerConfig, client Client, dispatcher dispatch.DispatcherInterface, logger *slog.Logger) *Poller {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.RoomToProject == nil {
		cfg.RoomToProject = make(map[string]string)
	}
	return &Poller{
		cfg:        cfg,
		client:     client,
		dispatcher: dispatcher,
		logger:     logger,
		cursors:    make(map[string]string),
	}
}

// BuildRoomProjectMap builds a room->project map from enabled projects and room config.
func BuildRoomProjectMap(cfg *config.Config) map[string]string {
	out := make(map[string]string)
	if cfg == nil {
		return out
	}

	names := make([]string, 0, len(cfg.Projects))
	for name, project := range cfg.Projects {
		if project.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	for _, name := range names {
		room := strings.TrimSpace(cfg.ResolveRoom(name))
		if room == "" {
			continue
		}
		if _, exists := out[room]; exists {
			continue
		}
		out[room] = name
	}
	return out
}

// Run starts periodic polling until context cancellation.
func (p *Poller) Run(ctx context.Context) {
	if !p.cfg.Enabled {
		p.logger.Info("matrix poller disabled")
		return
	}
	if p.client == nil {
		p.logger.Error("matrix poller disabled: client is nil")
		return
	}
	if p.dispatcher == nil {
		p.logger.Error("matrix poller disabled: dispatcher is nil")
		return
	}

	p.logger.Info("matrix poller started",
		"poll_interval", p.cfg.PollInterval.String(),
		"rooms", len(p.cfg.RoomToProject))

	_ = p.PollOnce(ctx)
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.logger.Info("matrix poller stopped")
			return
		case <-ticker.C:
			_ = p.PollOnce(ctx)
		}
	}
}

// PollOnce executes one polling cycle.
func (p *Poller) PollOnce(ctx context.Context) error {
	if !p.cfg.Enabled || p.client == nil || p.dispatcher == nil {
		return nil
	}

	rooms := make([]string, 0, len(p.cfg.RoomToProject))
	for room := range p.cfg.RoomToProject {
		rooms = append(rooms, room)
	}
	sort.Strings(rooms)

	for _, room := range rooms {
		project := strings.TrimSpace(p.cfg.RoomToProject[room])
		if project == "" {
			p.logger.Warn("matrix room has no project mapping", "room", room)
			continue
		}

		after := p.cursor(room)
		messages, nextCursor, err := p.client.ReadMessages(ctx, room, after)
		if err != nil {
			p.logger.Warn("matrix poll failed", "room", room, "project", project, "error", err)
			continue
		}
		if nextCursor != "" {
			p.setCursor(room, nextCursor)
		}

		for _, msg := range messages {
			if p.isOwnMessage(msg.Sender) {
				continue
			}
			if strings.TrimSpace(msg.Room) == "" {
				msg.Room = room
			}
			msg.Project = project
			if err := p.routeMessage(ctx, msg); err != nil {
				p.logger.Error("failed routing matrix message",
					"project", project,
					"room", msg.Room,
					"sender", msg.Sender,
					"message_id", msg.ID,
					"error", err)
			}
			if msg.ID != "" {
				p.setCursor(room, msg.ID)
			}
		}
	}
	return nil
}

func (p *Poller) routeMessage(ctx context.Context, msg InboundMessage) error {
	agent := fmt.Sprintf("%s-scrum", strings.TrimSpace(msg.Project))
	if agent == "-scrum" {
		agent = "main"
	}

	prompt := fmt.Sprintf(`# Matrix Inbound Message

Project: %s
Room: %s
Sender: %s
Timestamp: %s
Message ID: %s

Message:
%s

You are the project scrum agent. Reply with a concise acknowledgement and the next action for this project.`,
		msg.Project,
		msg.Room,
		msg.Sender,
		msg.Timestamp.UTC().Format(time.RFC3339),
		msg.ID,
		msg.Body,
	)

	p.logger.Info("routing matrix message",
		"project", msg.Project,
		"room", msg.Room,
		"sender", msg.Sender,
		"message_id", msg.ID,
		"agent", agent)

	_, err := p.dispatcher.Dispatch(ctx, agent, prompt, "", defaultThinking, defaultWorkDir)
	if err != nil && agent != "main" {
		p.logger.Warn("matrix routing fallback to main agent", "project", msg.Project, "agent", agent, "error", err)
		_, fallbackErr := p.dispatcher.Dispatch(ctx, "main", prompt, "", defaultThinking, defaultWorkDir)
		if fallbackErr != nil {
			return fmt.Errorf("dispatch fallback failed: %w", fallbackErr)
		}
		return nil
	}
	return err
}

func (p *Poller) isOwnMessage(sender string) bool {
	bot := strings.TrimSpace(p.cfg.BotUser)
	if bot == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(sender), bot)
}

func (p *Poller) cursor(room string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cursors[room]
}

func (p *Poller) setCursor(room, cursor string) {
	if strings.TrimSpace(cursor) == "" {
		return
	}
	p.mu.Lock()
	p.cursors[room] = cursor
	p.mu.Unlock()
}
