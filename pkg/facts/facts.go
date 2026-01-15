// Package facts gathers system information from target hosts.
package facts

import (
	"context"
	"runtime"
	"strings"

	"github.com/eugenetaranov/bolt/internal/connector"
)

// Gather collects system facts from the target.
func Gather(ctx context.Context, conn connector.Connector) (map[string]any, error) {
	facts := make(map[string]any)

	// Basic facts from Go runtime (for local)
	facts["go_os"] = runtime.GOOS
	facts["go_arch"] = runtime.GOARCH

	// Gather OS information
	osInfo, err := gatherOSInfo(ctx, conn)
	if err == nil {
		for k, v := range osInfo {
			facts[k] = v
		}
	}

	// Gather hostname
	if hostname, err := gatherHostname(ctx, conn); err == nil {
		facts["hostname"] = hostname
	}

	// Gather user info
	if user, err := gatherUser(ctx, conn); err == nil {
		facts["user"] = user
	}

	// Gather home directory
	if home, err := gatherHome(ctx, conn); err == nil {
		facts["home"] = home
	}

	// Gather environment
	if env, err := gatherEnv(ctx, conn); err == nil {
		facts["env"] = env
	}

	return facts, nil
}

// gatherOSInfo gathers operating system information.
func gatherOSInfo(ctx context.Context, conn connector.Connector) (map[string]any, error) {
	info := make(map[string]any)

	// Try to detect OS type
	result, err := conn.Execute(ctx, "uname -s")
	if err != nil {
		return info, err
	}

	osType := strings.TrimSpace(result.Stdout)
	info["os_type"] = osType

	switch osType {
	case "Darwin":
		info["os_family"] = "Darwin"
		info["pkg_manager"] = "brew"

		// Get macOS version
		if result, err := conn.Execute(ctx, "sw_vers -productVersion"); err == nil {
			info["os_version"] = strings.TrimSpace(result.Stdout)
		}

		// Get macOS name
		if result, err := conn.Execute(ctx, "sw_vers -productName"); err == nil {
			info["os_name"] = strings.TrimSpace(result.Stdout)
		}

	case "Linux":
		info["os_family"] = "Linux"

		// Try to get distribution info from /etc/os-release
		if result, err := conn.Execute(ctx, "cat /etc/os-release 2>/dev/null"); err == nil && result.ExitCode == 0 {
			osRelease := parseOSRelease(result.Stdout)
			if id, ok := osRelease["ID"]; ok {
				info["distribution"] = id
			}
			if version, ok := osRelease["VERSION_ID"]; ok {
				info["distribution_version"] = version
			}
			if name, ok := osRelease["PRETTY_NAME"]; ok {
				info["os_name"] = name
			}

			// Set package manager based on distribution
			switch info["distribution"] {
			case "ubuntu", "debian", "linuxmint", "pop":
				info["pkg_manager"] = "apt"
				info["os_family"] = "Debian"
			case "fedora", "rhel", "centos", "rocky", "almalinux":
				info["pkg_manager"] = "dnf"
				info["os_family"] = "RedHat"
			case "arch", "manjaro":
				info["pkg_manager"] = "pacman"
				info["os_family"] = "Arch"
			case "alpine":
				info["pkg_manager"] = "apk"
				info["os_family"] = "Alpine"
			case "opensuse", "sles":
				info["pkg_manager"] = "zypper"
				info["os_family"] = "Suse"
			}
		}
	}

	// Get architecture
	if result, err := conn.Execute(ctx, "uname -m"); err == nil {
		arch := strings.TrimSpace(result.Stdout)
		info["architecture"] = arch

		// Normalize architecture names
		switch arch {
		case "x86_64", "amd64":
			info["arch"] = "amd64"
		case "aarch64", "arm64":
			info["arch"] = "arm64"
		case "armv7l":
			info["arch"] = "arm"
		default:
			info["arch"] = arch
		}
	}

	// Get kernel version
	if result, err := conn.Execute(ctx, "uname -r"); err == nil {
		info["kernel"] = strings.TrimSpace(result.Stdout)
	}

	return info, nil
}

// parseOSRelease parses /etc/os-release format.
func parseOSRelease(content string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, "="); idx > 0 {
			key := line[:idx]
			value := strings.Trim(line[idx+1:], "\"'")
			result[key] = value
		}
	}
	return result
}

// gatherHostname gets the system hostname.
func gatherHostname(ctx context.Context, conn connector.Connector) (string, error) {
	result, err := conn.Execute(ctx, "hostname")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

// gatherUser gets the current user.
func gatherUser(ctx context.Context, conn connector.Connector) (string, error) {
	result, err := conn.Execute(ctx, "whoami")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

// gatherHome gets the home directory.
func gatherHome(ctx context.Context, conn connector.Connector) (string, error) {
	result, err := conn.Execute(ctx, "echo $HOME")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

// gatherEnv gets select environment variables.
func gatherEnv(ctx context.Context, conn connector.Connector) (map[string]string, error) {
	env := make(map[string]string)

	// Get common environment variables
	vars := []string{"PATH", "SHELL", "LANG", "LC_ALL", "TERM", "EDITOR"}
	for _, v := range vars {
		result, err := conn.Execute(ctx, "echo $"+v)
		if err == nil && result.ExitCode == 0 {
			value := strings.TrimSpace(result.Stdout)
			if value != "" {
				env[v] = value
			}
		}
	}

	return env, nil
}
