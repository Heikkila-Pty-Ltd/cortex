package matrix

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOpenClawSenderSendMessageIncludesAccount(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"ok":true}`)}
	sender := NewOpenClawSender(runner, "spritzbot")

	if err := sender.SendMessage(context.Background(), "!room:matrix.org", "hello"); err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}

	args := strings.Join(runner.lastArgs, " ")
	if !strings.Contains(args, "--channel matrix") {
		t.Fatalf("expected matrix channel args, got: %s", args)
	}
	if !strings.Contains(args, "--target !room:matrix.org") {
		t.Fatalf("expected target arg, got: %s", args)
	}
	if !strings.Contains(args, "--message hello") {
		t.Fatalf("expected message arg, got: %s", args)
	}
	if !strings.Contains(args, "--account spritzbot") {
		t.Fatalf("expected account arg, got: %s", args)
	}
}

func TestOpenClawSenderSendMessageOmitsEmptyAccount(t *testing.T) {
	runner := &fakeRunner{out: []byte(`{"ok":true}`)}
	sender := NewOpenClawSender(runner, "")

	if err := sender.SendMessage(context.Background(), "!room:matrix.org", "hello"); err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}

	args := strings.Join(runner.lastArgs, " ")
	if strings.Contains(args, "--account") {
		t.Fatalf("did not expect --account arg, got: %s", args)
	}
}

func TestOpenClawSenderSendMessageValidatesInputs(t *testing.T) {
	sender := NewOpenClawSender(&fakeRunner{}, "spritzbot")

	if err := sender.SendMessage(context.Background(), "", "hello"); err == nil {
		t.Fatal("expected error for empty room id")
	}
	if err := sender.SendMessage(context.Background(), "!room:matrix.org", ""); err == nil {
		t.Fatal("expected error for empty message")
	}
}

func TestOpenClawSenderSendMessageHandlesRunnerError(t *testing.T) {
	runner := &fakeRunner{
		out: []byte("matrix unavailable"),
		err: errors.New("exit status 1"),
	}
	sender := NewOpenClawSender(runner, "spritzbot")

	err := sender.SendMessage(context.Background(), "!room:matrix.org", "hello")
	if err == nil {
		t.Fatal("expected runner error")
	}
	if !strings.Contains(err.Error(), "openclaw message send failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
