package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/renewelches/confkoffer/internal/config"
)

func addInit(root *cobra.Command) {
	c := &cobra.Command{
		Use:          "init",
		Short:        "Scaffold a .confkoffer.yaml in the current directory.",
		SilenceUsage: true,
		RunE:         runInit,
	}
	c.Flags().Bool("force", false, "overwrite an existing .confkoffer.yaml")
	root.AddCommand(c)
}

func runInit(cmd *cobra.Command, _ []string) error {
	force, _ := cmd.Flags().GetBool("force")
	path := config.DefaultConfigPath
	if rootOpts.Config != "" {
		path = rootOpts.Config
	}

	if _, err := os.Stat(path); err == nil {
		if !force {
			return configError{fmt.Errorf("%s already exists; pass --force to overwrite", path)}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "wrote %s\n\nNext steps:\n", path)
	fmt.Fprintln(os.Stdout, "  1. Edit the file: set 'name' and review patterns/include/exclude.")
	fmt.Fprintln(os.Stdout, "  2. Export S3 creds: AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_ENDPOINT.")
	fmt.Fprintln(os.Stdout, "  3. Run: confkoffer pack")
	return nil
}

const template = `# .confkoffer.yaml — committed to your project root.
# Required: name, storage.endpoint. Other fields are optional.

name: my-project                # alphanumeric + dashes; "/" allowed for nesting
                                # e.g. prod/aws/useast or marketing/mailchimp/prod

storage:
  bucket: confkoffer            # S3 bucket name (default if omitted: 'confkoffer')
  endpoint: s3.amazonaws.com    # set to MinIO's endpoint for local testing
  # region: eu-central-1        # default: us-east-1

# crypto:                       # uncomment to override Argon2id defaults
#   argon2id:                   # OWASP first-choice (stronger):
#     memory_kib: 47104
#     time: 1
#     threads: 1

patterns:
  include:
    - "**/*.tf"
    - "**/*.tfvars"
    - "secrets/prod.env"        # literal paths also work
  exclude:
    - "**/*.tfstate"
    - ".terraform/**"

# password:                     # default: chain flag -> env -> prompt
#   source: pass                # one of: prompt | env | flag | pass | command
#   pass:
#     path: backups/confkoffer/my-project
#   # OR universal exec source (Vault wrapper, 1Password, Bitwarden, etc.):
#   # source: command
#   # command:
#   #   argv: ["op", "read", "op://Personal/confkoffer/password"]
#   #   timeout: 10s
`
