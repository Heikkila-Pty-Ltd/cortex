package matrix

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
)

type fakePollResponse struct {
	messages []InboundMessage
	next     string
}

type fakeClient struct {
	responses map[string]fakePollResponse
	errors    map[string]error
	calls     []string
}

func (c *fakeClient) ReadMessages(_ context.Context, roomID string, _ string) ([]InboundMessage, string, error) {
	c.calls = append(c.calls, roomID)
	if err := c.errors[roomID]; err != nil {
		return nil, "", err
	}
	resp := c.responses[roomID]
	return resp.messages, resp.next, nil
}

type pollDispatchCall struct {
	agent  string
	prompt string
}

type fakeDispatcher struct {
	calls      []pollDispatchCall
	failAgents map[string]bool
}

func (d *fakeDispatcher) Dispatch(_ context.Context, agent, prompt, _ string, _ string, _ string) (int, error) {
	d.calls = append(d.calls, pollDispatchCall{agent: agent, prompt: prompt})
	if d.failAgents != nil && d.failAgents[agent] {
		return 0, errors.New("simulated dispatch failure")
	}
	return len(d.calls), nil
}

func (d *fakeDispatcher) IsAlive(_ int) bool { return false }
func (d *fakeDispatcher) Kill(_ int) error   { return nil }
func (d *fakeDispatcher) GetHandleType() string {
	return "test"
}
func (d *fakeDispatcher) GetSessionName(_ int) string { return "" }
func (d *fakeDispatcher) GetProcessState(_ int) dispatch.ProcessState {
	return dispatch.ProcessState{}
}

func TestPollOnceRoutesMessagesAndSkipsBotSender(t *testing.T) {
	client := &fakeClient{
		responses: map[string]fakePollResponse{
			"!room-a:matrix.org": {
				messages: []InboundMessage{
					{ID: "1", Room: "!room-a:matrix.org", Sender: "@cortex-bot:matrix.org", Body: "self-message"},
					{ID: "2", Room: "!room-a:matrix.org", Sender: "@alice:matrix.org", Body: "hello from a"},
				},
				next: "cursor-a",
			},
			"!room-b:matrix.org": {
				messages: []InboundMessage{
					{ID: "3", Room: "!room-b:matrix.org", Sender: "@bob:matrix.org", Body: "hello from b"},
				},
				next: "cursor-b",
			},
		},
	}
	dispatcher := &fakeDispatcher{}

	poller := NewPoller(PollerConfig{
		Enabled: true,
		BotUser: "@cortex-bot:matrix.org",
		RoomToProject: map[string]string{
			"!room-a:matrix.org": "project-a",
			"!room-b:matrix.org": "project-b",
		},
	}, client, dispatcher, nil)

	if err := poller.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}

	if len(dispatcher.calls) != 2 {
		t.Fatalf("expected 2 routed dispatches, got %d", len(dispatcher.calls))
	}
	if dispatcher.calls[0].agent != "project-a-scrum" {
		t.Fatalf("first dispatch agent = %q, want project-a-scrum", dispatcher.calls[0].agent)
	}
	if dispatcher.calls[1].agent != "project-b-scrum" {
		t.Fatalf("second dispatch agent = %q, want project-b-scrum", dispatcher.calls[1].agent)
	}
	if !strings.Contains(dispatcher.calls[0].prompt, "hello from a") {
		t.Fatalf("first prompt missing message body: %q", dispatcher.calls[0].prompt)
	}
}

func TestPollOnceContinuesOnRoomReadError(t *testing.T) {
	client := &fakeClient{
		responses: map[string]fakePollResponse{
			"!ok:matrix.org": {
				messages: []InboundMessage{
					{ID: "7", Sender: "@person:matrix.org", Body: "ok room message"},
				},
			},
		},
		errors: map[string]error{
			"!fail:matrix.org": errors.New("matrix unavailable"),
		},
	}
	dispatcher := &fakeDispatcher{}

	poller := NewPoller(PollerConfig{
		Enabled: true,
		RoomToProject: map[string]string{
			"!fail:matrix.org": "failing-project",
			"!ok:matrix.org":   "ok-project",
		},
	}, client, dispatcher, nil)

	if err := poller.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}
	if len(dispatcher.calls) != 1 {
		t.Fatalf("expected 1 dispatch call from healthy room, got %d", len(dispatcher.calls))
	}
	if dispatcher.calls[0].agent != "ok-project-scrum" {
		t.Fatalf("routed agent = %q, want ok-project-scrum", dispatcher.calls[0].agent)
	}
}

func TestPollOnceDisabledDoesNothing(t *testing.T) {
	client := &fakeClient{}
	dispatcher := &fakeDispatcher{}

	poller := NewPoller(PollerConfig{
		Enabled: false,
		RoomToProject: map[string]string{
			"!room:matrix.org": "project",
		},
	}, client, dispatcher, nil)

	if err := poller.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("expected no client calls while disabled, got %d", len(client.calls))
	}
	if len(dispatcher.calls) != 0 {
		t.Fatalf("expected no dispatches while disabled, got %d", len(dispatcher.calls))
	}
}

func TestPollOnceFallsBackToMainOnDispatchFailure(t *testing.T) {
	client := &fakeClient{
		responses: map[string]fakePollResponse{
			"!room:matrix.org": {
				messages: []InboundMessage{
					{ID: "10", Sender: "@alice:matrix.org", Body: "needs routing"},
				},
			},
		},
	}
	dispatcher := &fakeDispatcher{
		failAgents: map[string]bool{"project-a-scrum": true},
	}

	poller := NewPoller(PollerConfig{
		Enabled: true,
		RoomToProject: map[string]string{
			"!room:matrix.org": "project-a",
		},
	}, client, dispatcher, nil)

	if err := poller.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}
	if len(dispatcher.calls) != 2 {
		t.Fatalf("expected project + fallback dispatch calls, got %d", len(dispatcher.calls))
	}
	if dispatcher.calls[0].agent != "project-a-scrum" {
		t.Fatalf("first agent = %q, want project-a-scrum", dispatcher.calls[0].agent)
	}
	if dispatcher.calls[1].agent != "main" {
		t.Fatalf("fallback agent = %q, want main", dispatcher.calls[1].agent)
	}
}

func TestBuildRoomProjectMapUsesResolvedRoom(t *testing.T) {
	cfg := &config.Config{
		Reporter: config.Reporter{
			DefaultRoom: "!fallback:matrix.org",
		},
		Projects: map[string]config.Project{
			"project-b": {Enabled: true},
			"project-a": {Enabled: true},
			"project-c": {Enabled: true, MatrixRoom: "!room-c:matrix.org"},
			"project-z": {Enabled: false, MatrixRoom: "!room-z:matrix.org"},
		},
	}

	got := BuildRoomProjectMap(cfg)
	if got["!room-c:matrix.org"] != "project-c" {
		t.Fatalf("room-c mapping = %q, want project-c", got["!room-c:matrix.org"])
	}
	// Duplicate fallback room is assigned deterministically to alphabetically first enabled project.
	if got["!fallback:matrix.org"] != "project-a" {
		t.Fatalf("fallback mapping = %q, want project-a", got["!fallback:matrix.org"])
	}
	if _, ok := got["!room-z:matrix.org"]; ok {
		t.Fatal("disabled project room should not be mapped")
	}
}

func TestPollerRunStopsOnContextCancel(t *testing.T) {
	client := &fakeClient{}
	dispatcher := &fakeDispatcher{}
	poller := NewPoller(PollerConfig{
		Enabled:      true,
		PollInterval: 10 * time.Millisecond,
		RoomToProject: map[string]string{
			"!room:matrix.org": "project-a",
		},
	}, client, dispatcher, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	poller.Run(ctx)
}
