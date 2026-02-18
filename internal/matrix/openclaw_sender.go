package matrix

import (
	"context"
	"fmt"
	"strings"
)

// Sender sends outbound Matrix messages.
type Sender interface {
	SendMessage(ctx context.Context, roomID, message string) error
}

// OpenClawSender sends Matrix messages via `openclaw message send`.
type OpenClawSender struct {
	runner  Runner
	account string
}

// NewOpenClawSender constructs a sender with an optional account id.
func NewOpenClawSender(runner Runner, account string) *OpenClawSender {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &OpenClawSender{
		runner:  runner,
		account: strings.TrimSpace(account),
	}
}

// SendMessage sends a message to a Matrix room.
func (s *OpenClawSender) SendMessage(ctx context.Context, roomID, message string) error {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return fmt.Errorf("room id is required")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("message is required")
	}

	args := []string{
		"message", "send",
		"--channel", "matrix",
		"--target", roomID,
		"--message", message,
		"--json",
	}
	if s.account != "" {
		args = append(args, "--account", s.account)
	}

	out, err := s.runner.Run(ctx, "openclaw", args...)
	if err != nil {
		return fmt.Errorf("openclaw message send failed: %w (%s)", err, compactOutput(out))
	}
	return nil
}
