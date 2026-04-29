package cmd

import (
	"fmt"

	"confkoffer/internal/config"
	"confkoffer/internal/password"
)

// buildPasswordSource translates the resolved Config into a concrete
// password.Source. Callers pass confirm=true on pack (double-prompt)
// and false on unpack/list.
//
// When cfg.Password.Source is unset the default chain is used:
// flag → env → prompt.
func buildPasswordSource(cfg *config.Config, confirm bool) (password.Source, error) {
	switch cfg.Password.Source {
	case "":
		return defaultChain(cfg, confirm), nil
	case "flag":
		if len(cfg.PasswordOverride) == 0 {
			return nil, fmt.Errorf("password.source=flag but --pass not provided")
		}
		return &password.FlagSource{Value: cfg.PasswordOverride}, nil
	case "env":
		return &password.EnvSource{Var: config.EnvPassword}, nil
	case "prompt":
		return password.NewDefaultPrompter(confirm), nil
	case "pass":
		return password.NewPassSource(cfg.Password.Pass.Path), nil
	case "command":
		return password.NewCommandSource(cfg.Password.Command.Argv, cfg.Password.Command.Timeout), nil
	default:
		return nil, fmt.Errorf("unknown password.source: %q", cfg.Password.Source)
	}
}

func defaultChain(cfg *config.Config, confirm bool) password.Source {
	var srcs []password.Source
	if len(cfg.PasswordOverride) > 0 {
		srcs = append(srcs, &password.FlagSource{Value: cfg.PasswordOverride})
	}
	srcs = append(srcs, &password.EnvSource{Var: config.EnvPassword})
	srcs = append(srcs, password.NewDefaultPrompter(confirm))
	return password.NewChain(srcs...)
}
