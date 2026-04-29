package password

import (
	"context"
	"fmt"
	"time"
)

// CommandSource is the universal exec escape-hatch: run any command,
// take stdout as the password. Covers Vault, 1Password (`op`),
// Bitwarden (`bw`), KeePassXC, etc.
//
// Argv is never logged — describeArgv strips it down to the program
// basename for any audit-trail records.
type CommandSource struct {
	Argv    []string
	Timeout time.Duration

	runner execRunner
}

// NewCommandSource is the production constructor.
func NewCommandSource(argv []string, timeout time.Duration) *CommandSource {
	return &CommandSource{Argv: argv, Timeout: timeout, runner: realRunner}
}

func (c *CommandSource) Get(ctx context.Context) ([]byte, error) {
	if len(c.Argv) == 0 {
		return nil, fmt.Errorf("command: empty argv")
	}
	r := c.runner
	if r == nil {
		r = realRunner
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	return runAndCapture(ctx, r, c.Argv)
}

func (c *CommandSource) Name() string { return "command" }
