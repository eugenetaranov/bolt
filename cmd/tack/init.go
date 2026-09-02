package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Scaffold a minimal site.yaml and inventory.yaml to get started",
	Long: `Init writes a minimal site.yaml and inventory.yaml in the current
directory so that a plain "tack run" works with zero arguments.

Existing files are never overwritten.`,
	Args: cobra.NoArgs,
	RunE: runInit,
}

const siteYAMLTemplate = `# site.yaml — your first playbook. Run it with: tack run
- name: Example play
  hosts: localhost
  connection: local
  tasks:
    - name: Say hello
      debug:
        msg: "Tack is working — edit site.yaml to configure your systems."

    - name: Ensure a scratch directory exists
      file:
        path: /tmp/tack-demo
        state: directory
        mode: "0755"
`

const inventoryYAMLTemplate = `# inventory.yaml — hosts tack can target.
# 'localhost' with the local connection needs no SSH setup.
hosts:
  localhost:
    connection: local

# Add remote hosts like this, then run: tack run --hosts web1
# groups:
#   web:
#     hosts: [web1, web2]
# hosts:
#   web1:
#     ssh:
#       user: ubuntu
`

func runInit(cmd *cobra.Command, _ []string) error {
	files := []struct {
		name, content string
	}{
		{"site.yaml", siteYAMLTemplate},
		{"inventory.yaml", inventoryYAMLTemplate},
	}

	var created, skipped []string
	for _, f := range files {
		if _, err := os.Stat(f.name); err == nil {
			skipped = append(skipped, f.name)
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("checking %s: %w", f.name, err)
		}
		if err := os.WriteFile(f.name, []byte(f.content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", f.name, err)
		}
		created = append(created, f.name)
	}

	out := cmd.OutOrStdout()
	for _, name := range created {
		fmt.Fprintf(out, "Created %s\n", name)
	}
	for _, name := range skipped {
		fmt.Fprintf(out, "Skipped %s (already exists)\n", name)
	}

	if len(created) > 0 {
		fmt.Fprintln(out, "\nNext steps:")
		fmt.Fprintln(out, "  tack run            # preview and apply site.yaml")
		fmt.Fprintln(out, "  tack run --check    # preview only (no changes)")
	}
	return nil
}
