package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// nameSegment is the per-segment allowed shape: lowercase alphanumerics
// and dashes, at least one character.
var nameSegment = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidateName accepts slash-separated names like "myproj" or
// "prod/aws/useast". Each segment must match nameSegment; leading,
// trailing, and double slashes are rejected, as are ".." segments.
//
// The result is used as an S3 prefix, so the rules are deliberately
// strict — easy to type, no surprises in URLs or shell.
func ValidateName(name string) error {
	if name == "" {
		return errors.New("name: must not be empty")
	}
	if strings.HasPrefix(name, "/") {
		return errors.New("name: must not start with '/'")
	}
	if strings.HasSuffix(name, "/") {
		return errors.New("name: must not end with '/'")
	}
	if strings.Contains(name, "//") {
		return errors.New("name: must not contain empty segments ('//')")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("name: forbidden segment %q", seg)
		}
		if !nameSegment.MatchString(seg) {
			return fmt.Errorf("name: segment %q must match [a-z0-9](-[a-z0-9]+)*", seg)
		}
	}
	return nil
}

// validatePasswordSource checks that the requested source name is one
// of the supported values and that the source-specific config is
// populated where required.
func validatePasswordSource(p Password) error {
	switch p.Source {
	case "prompt", "env", "flag":
		// no extra config required
	case "pass":
		if strings.TrimSpace(p.Pass.Path) == "" {
			return errors.New("password.pass.path: required when source=pass")
		}
	case "command":
		if len(p.Command.Argv) == 0 {
			return errors.New("password.command.argv: required when source=command")
		}
	default:
		return fmt.Errorf("password.source: unknown value %q (want one of: prompt, env, flag, pass, command)", p.Source)
	}
	return nil
}
