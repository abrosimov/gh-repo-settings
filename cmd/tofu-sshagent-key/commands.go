package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh/agent"

	"github.com/abrosimov/gh-repo-settings/internal/envelope"
	"github.com/abrosimov/gh-repo-settings/internal/tofuprotocol"
)

const agentDialTimeout = 5 * time.Second

var errAgentUnset = errors.New("SSH_AUTH_SOCK is not set: start ssh-agent and add an enrolled ed25519 key")

func runKey(ctx context.Context, inv *invocation) error {
	file, err := envelope.Load(inv.recipients)
	if err != nil {
		return err
	}
	var dataKey []byte
	err = withAgent(ctx, func(a envelope.Agent) error {
		var unwrapErr error
		dataKey, unwrapErr = envelope.UnwrapDataKey(a, file)
		return unwrapErr
	})
	if errors.Is(err, envelope.ErrNoEnrolledKey) {
		return fmt.Errorf("%w: add one of the keys shown by '%s list' to ssh-agent", err, programName)
	}
	if err != nil {
		return err
	}
	return tofuprotocol.Serve(inv.stdin, inv.stdout, dataKey)
}

func runInit(ctx context.Context, inv *invocation) error {
	var added []envelope.Recipient
	err := withAgent(ctx, func(a envelope.Agent) error {
		var err error
		added, err = envelope.Init(a, inv.recipients)
		return err
	})
	if err != nil {
		return err
	}
	return write(inv.stdout, fmt.Sprintf("Created %s with %d recipient(s):\n%s", inv.recipients, len(added), describe(added)))
}

func runEnroll(ctx context.Context, inv *invocation) error {
	var added []envelope.Recipient
	err := withAgent(ctx, func(a envelope.Agent) error {
		var err error
		added, err = envelope.Enroll(a, inv.recipients)
		return err
	})
	if err != nil {
		return err
	}
	if len(added) == 0 {
		return write(inv.stdout, "Every ed25519 key in ssh-agent is already enrolled; nothing to do.\n")
	}
	return write(inv.stdout, fmt.Sprintf("Enrolled %d new recipient(s) in %s:\n%s", len(added), inv.recipients, describe(added)))
}

func runList(ctx context.Context, inv *invocation) error {
	file, err := envelope.Load(inv.recipients)
	if err != nil {
		return err
	}

	var present map[string]struct{}
	agentErr := withAgent(ctx, func(a envelope.Agent) error {
		var listErr error
		present, listErr = envelope.AgentFingerprints(a)
		return listErr
	})
	if agentErr != nil {
		if err := write(inv.stderr, fmt.Sprintf("warning: cannot query ssh-agent (%v); presence is unknown\n", agentErr)); err != nil {
			return err
		}
	}

	lines := make([]string, 0, len(file.Recipients))
	for i := range file.Recipients {
		r := &file.Recipients[i]
		status := "absent"
		if agentErr != nil {
			status = "unknown"
		} else if _, ok := present[r.Fingerprint]; ok {
			status = "present"
		}
		lines = append(lines, fmt.Sprintf("%s  %-7s  %s", r.Fingerprint, status, r.Comment))
	}
	return write(inv.stdout, strings.Join(lines, "\n")+"\n")
}

func runRevoke(_ context.Context, inv *invocation) error {
	revoked, err := envelope.Revoke(inv.recipients, inv.args[0])
	if err != nil {
		return err
	}
	return write(inv.stdout, fmt.Sprintf(
		"Revoked %s (%s) from %s.\nThis does not re-key: rotate the data key through OpenTofu's fallback block.\n",
		revoked.Fingerprint, revoked.Comment, inv.recipients))
}

func withAgent(ctx context.Context, use func(envelope.Agent) error) (err error) {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return errAgentUnset
	}
	dialer := net.Dialer{Timeout: agentDialTimeout}
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("connecting to ssh-agent at %s: %w", socket, err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing ssh-agent connection: %w", closeErr))
		}
	}()
	return use(agent.NewClient(conn))
}

func describe(recipients []envelope.Recipient) string {
	lines := make([]string, 0, len(recipients))
	for i := range recipients {
		lines = append(lines, fmt.Sprintf("  %s  %s\n", recipients[i].Fingerprint, recipients[i].Comment))
	}
	return strings.Join(lines, "")
}

func write(w io.Writer, text string) error {
	if _, err := io.WriteString(w, text); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}
	return nil
}
