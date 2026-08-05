// Package inventory loads and queries host inventory files.
package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/tackhq/tack/internal/playbook"
)

// HostEntry defines a single host in the inventory.
type HostEntry struct {
	// SSH holds per-host SSH connection overrides.
	SSH *playbook.SSHConfig `yaml:"ssh" json:"ssh,omitempty"`

	// Vars holds per-host variables available in task templates.
	Vars map[string]any `yaml:"vars" json:"vars,omitempty"`
}

// GroupSSMConfig holds SSM settings for a group.
type GroupSSMConfig struct {
	Region string `yaml:"region" json:"region,omitempty"`
	Bucket string `yaml:"bucket" json:"bucket,omitempty"`

	// Instances lists EC2 instance IDs to target.
	// Convenience alias: these are merged with the group's hosts list.
	Instances []string `yaml:"instances" json:"instances,omitempty"`

	// Tags selects EC2 instances by tag at runtime (mutually exclusive with Instances).
	Tags map[string]string `yaml:"tags" json:"tags,omitempty"`

	// AttachS3Policy temporarily grants the instance's IAM role S3 access
	// to the transfer bucket for the duration of the play. Mirrors
	// playbook.SSMConfig.AttachS3Policy.
	AttachS3Policy bool `yaml:"attach_s3_policy" json:"attach_s3_policy,omitempty"`
}

// GroupEntry defines a named group of hosts.
type GroupEntry struct {
	// Connection overrides the play's connection type for hosts in this group.
	Connection string `yaml:"connection" json:"connection,omitempty"`

	// SSH holds group-level SSH defaults (overridden by per-host SSH config).
	SSH *playbook.SSHConfig `yaml:"ssh" json:"ssh,omitempty"`

	// SSM holds group-level SSM defaults.
	SSM *GroupSSMConfig `yaml:"ssm" json:"ssm,omitempty"`

	// Hosts lists the host names (or instance IDs) that belong to this group.
	Hosts []string `yaml:"hosts" json:"hosts,omitempty"`

	// Vars holds group-level variables (lower priority than per-host vars).
	Vars map[string]any `yaml:"vars" json:"vars,omitempty"`
}

// Inventory holds the parsed inventory.
type Inventory struct {
	// Hosts maps host names to their entries.
	Hosts map[string]*HostEntry `yaml:"hosts" json:"hosts,omitempty"`

	// Groups maps group names to their entries.
	Groups map[string]*GroupEntry `yaml:"groups" json:"groups,omitempty"`
}

// Load reads and parses an inventory from path. It routes to the appropriate
// handler based on the input:
//  1. If path is an executable file → script plugin
//  2. If parsed YAML has a "plugin" key → named plugin from registry
//  3. Otherwise → static YAML parse (original behavior)
//
// The ctx parameter controls timeouts for plugin execution.
func Load(path string) (*Inventory, error) {
	return LoadWithContext(context.Background(), path)
}

// LoadWithContext reads and parses an inventory from path using the given context
// for plugin timeouts.
func LoadWithContext(ctx context.Context, path string) (*Inventory, error) {
	// Check if path is an executable file
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to stat inventory file: %w", err)
	}
	if !info.IsDir() && info.Mode()&0111 != 0 {
		// Executable file → script plugin
		p, err := GetPlugin("script")
		if err != nil {
			return nil, fmt.Errorf("executable inventory file requires script plugin: %w", err)
		}
		return p.Load(ctx, map[string]any{"path": path})
	}

	// Read file content
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read inventory file: %w", err)
	}

	// Check for plugin key in YAML
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse inventory file: %w", err)
	}

	if pluginName, ok := raw["plugin"].(string); ok {
		p, err := GetPlugin(pluginName)
		if err != nil {
			return nil, err
		}
		return p.Load(ctx, raw)
	}

	// Static YAML parse (original behavior)
	inv := &Inventory{}
	if err := yaml.Unmarshal(data, inv); err != nil {
		return nil, fmt.Errorf("failed to parse inventory file: %w", err)
	}

	return inv, nil
}

// ParseInventoryData parses inventory data from bytes (JSON or YAML) into an Inventory.
func ParseInventoryData(data []byte) (*Inventory, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, fmt.Errorf("empty inventory data")
	}

	inv := &Inventory{}

	// Detect JSON vs YAML by first non-whitespace character
	if trimmed[0] == '{' || trimmed[0] == '[' {
		if err := json.Unmarshal(data, inv); err != nil {
			return nil, fmt.Errorf("failed to parse JSON inventory: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(data, inv); err != nil {
			return nil, fmt.Errorf("failed to parse YAML inventory: %w", err)
		}
	}

	return inv, nil
}

// ExpandGroup resolves a name to a list of host strings and the matching group
// entry (if any).
//
//   - If name is a known group: returns (group.Hosts, group, true).
//   - If name is a known host (but not a group): returns ([name], nil, true).
//   - Otherwise: returns (nil, nil, false) — caller should pass the name through as-is.
func (inv *Inventory) ExpandGroup(name string) ([]string, *GroupEntry, bool) {
	if inv == nil {
		return nil, nil, false
	}
	if g, ok := inv.Groups[name]; ok {
		// Merge hosts list with ssm.instances (both are valid ways to list targets).
		hosts := append([]string{}, g.Hosts...)
		if g.SSM != nil {
			hosts = append(hosts, g.SSM.Instances...)
		}
		return hosts, g, true
	}
	if _, ok := inv.Hosts[name]; ok {
		return []string{name}, nil, true
	}
	return nil, nil, false
}

// GetHost returns the HostEntry for the given host name, or nil if not defined.
func (inv *Inventory) GetHost(name string) *HostEntry {
	if inv == nil {
		return nil
	}
	return inv.Hosts[name]
}

// AllHosts expands every group, merges top-level hosts, and returns a
// deduplicated list of all host names in the inventory.
func (inv *Inventory) AllHosts() []string {
	if inv == nil {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	add := func(h string) {
		if !seen[h] {
			seen[h] = true
			result = append(result, h)
		}
	}

	for _, g := range inv.Groups {
		for _, h := range g.Hosts {
			add(h)
		}
		if g.SSM != nil {
			for _, h := range g.SSM.Instances {
				add(h)
			}
		}
	}
	for name := range inv.Hosts {
		add(name)
	}
	return result
}

// GetHostGroups returns all GroupEntry values that contain the given host name.
func (inv *Inventory) GetHostGroups(host string) []*GroupEntry {
	if inv == nil {
		return nil
	}
	var groups []*GroupEntry
	for _, g := range inv.Groups {
		for _, h := range g.Hosts {
			if h == host {
				groups = append(groups, g)
				break
			}
		}
	}
	return groups
}
