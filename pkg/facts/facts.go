// Package facts gathers system information from target hosts.
package facts

import (
	"context"
	"runtime"
	"strings"

	"github.com/tackhq/tack/internal/connector"
)

// factsScript is a single shell script that gathers all system facts in one
// invocation. Each fact is emitted as a "KEY=VALUE" line delimited by a
// well-known sentinel so the output can be parsed reliably. Using a single
// command matters for high-latency connectors like SSM where each Execute()
// round-trip takes several seconds.
const factsScript = `
exec 2>/dev/null
echo "TACK_FACT os_type=$(uname -s)"
echo "TACK_FACT architecture=$(uname -m)"
echo "TACK_FACT kernel=$(uname -r)"
echo "TACK_FACT hostname=$(hostname)"
echo "TACK_FACT user=$(whoami)"
echo "TACK_FACT home=$HOME"
if [ "$(uname -s)" = "Darwin" ]; then
  echo "TACK_FACT os_version=$(sw_vers -productVersion)"
  echo "TACK_FACT os_name=$(sw_vers -productName)"
fi
if [ -f /etc/os-release ]; then
  echo "TACK_FACT os_release_start"
  cat /etc/os-release
  echo "TACK_FACT os_release_end"
fi
for v in PATH SHELL LANG LC_ALL TERM EDITOR; do
  eval val=\$$v
  if [ -n "$val" ]; then
    echo "TACK_FACT env_${v}=${val}"
  fi
done
# Network facts
if [ "$(uname -s)" = "Darwin" ]; then
  _def_iface=$(route -n get default 2>/dev/null | awk '/interface:/{print $2}')
  if [ -n "$_def_iface" ]; then
    echo "TACK_FACT default_interface=$_def_iface"
    _def_ip=$(ifconfig "$_def_iface" 2>/dev/null | awk '/inet /{print $2; exit}')
    [ -n "$_def_ip" ] && echo "TACK_FACT default_ipv4=$_def_ip"
  fi
  _all4=$(ifconfig 2>/dev/null | awk '/inet /{if($2!="127.0.0.1")printf sep $2; sep=","}')
  [ -n "$_all4" ] && echo "TACK_FACT all_ipv4=$_all4"
  _all6=$(ifconfig 2>/dev/null | awk '/inet6 /{split($2,a,"%"); if(a[1]!="::1"&&a[1]!="fe80")printf sep a[1]; sep=","}')
  [ -n "$_all6" ] && echo "TACK_FACT all_ipv6=$_all6"
else
  _route=$(ip route get 1 2>/dev/null | head -1)
  if [ -n "$_route" ]; then
    _def_iface=$(echo "$_route" | sed -n 's/.* dev \([^ ]*\).*/\1/p')
    _def_ip=$(echo "$_route" | sed -n 's/.* src \([^ ]*\).*/\1/p')
    [ -n "$_def_iface" ] && echo "TACK_FACT default_interface=$_def_iface"
    [ -n "$_def_ip" ] && echo "TACK_FACT default_ipv4=$_def_ip"
    # Subnet of the default interface (the kernel already computed the network
    # CIDR for its link-scope route, so no host-bit masking is needed here).
    if [ -n "$_def_iface" ]; then
      _subnet=$(ip route show dev "$_def_iface" proto kernel scope link 2>/dev/null | awk '$1 ~ /\// {print $1; exit}')
      if [ -n "$_subnet" ]; then
        echo "TACK_FACT default_ipv4_subnet=$_subnet"
        _prefix=${_subnet#*/}
        [ "$_prefix" != "$_subnet" ] && echo "TACK_FACT default_ipv4_prefix=$_prefix"
      fi
    fi
  fi
  _all4=$(ip -4 addr show 2>/dev/null | awk '/inet /{split($2,a,"/"); if(a[1]!="127.0.0.1")printf sep a[1]; sep=","}')
  [ -n "$_all4" ] && echo "TACK_FACT all_ipv4=$_all4"
  _all6=$(ip -6 addr show scope global 2>/dev/null | awk '/inet6 /{split($2,a,"/"); printf sep a[1]; sep=","}')
  [ -n "$_all6" ] && echo "TACK_FACT all_ipv6=$_all6"
fi
# EC2 detection via IMDSv2
TOKEN=$(curl -sf -X PUT "http://169.254.169.254/latest/api/token" \
  -H "X-aws-ec2-metadata-token-ttl-seconds: 10" --connect-timeout 1 2>/dev/null)
if [ -n "$TOKEN" ]; then
  HDR="X-aws-ec2-metadata-token: $TOKEN"
  echo "TACK_FACT ec2_instance_id=$(curl -sf -H "$HDR" http://169.254.169.254/latest/meta-data/instance-id)"
  echo "TACK_FACT ec2_region=$(curl -sf -H "$HDR" http://169.254.169.254/latest/meta-data/placement/region)"
  echo "TACK_FACT ec2_az=$(curl -sf -H "$HDR" http://169.254.169.254/latest/meta-data/placement/availability-zone)"
  echo "TACK_FACT ec2_instance_type=$(curl -sf -H "$HDR" http://169.254.169.254/latest/meta-data/instance-type)"
  echo "TACK_FACT ec2_ami_id=$(curl -sf -H "$HDR" http://169.254.169.254/latest/meta-data/ami-id)"
  echo "TACK_FACT ec2_private_ip=$(curl -sf -H "$HDR" http://169.254.169.254/latest/meta-data/local-ipv4)"
  echo "TACK_FACT ec2_public_ip=$(curl -sf -H "$HDR" http://169.254.169.254/latest/meta-data/public-ipv4)"
  # Try IMDS tags (requires "allow tags in instance metadata" enabled)
  TAGS=$(curl -sf -H "$HDR" http://169.254.169.254/latest/meta-data/tags/instance/ 2>/dev/null)
  if [ -n "$TAGS" ]; then
    echo "TACK_FACT ec2_tags_start"
    for tag in $TAGS; do
      val=$(curl -sf -H "$HDR" "http://169.254.169.254/latest/meta-data/tags/instance/${tag}")
      echo "${tag}=${val}"
    done
    echo "TACK_FACT ec2_tags_end"
  fi
fi
`

// Gather collects system facts from the target via a single command.
func Gather(ctx context.Context, conn connector.Connector) (map[string]any, error) {
	facts := make(map[string]any)

	// Basic facts from Go runtime (for local)
	facts["go_os"] = runtime.GOOS
	facts["go_arch"] = runtime.GOARCH

	result, err := conn.Execute(ctx, factsScript)
	if err != nil {
		return facts, err
	}

	env := make(map[string]string)
	ec2Tags := make(map[string]string)
	var inOSRelease bool
	var osReleaseLines []string
	var inEC2Tags bool
	var gotIMDSTags bool

	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)

		// Collect /etc/os-release block
		if line == "TACK_FACT os_release_start" {
			inOSRelease = true
			continue
		}
		if line == "TACK_FACT os_release_end" {
			inOSRelease = false
			osRelease := parseOSRelease(strings.Join(osReleaseLines, "\n"))
			applyOSRelease(facts, osRelease)
			continue
		}
		if inOSRelease {
			osReleaseLines = append(osReleaseLines, line)
			continue
		}

		// Collect ec2_tags block
		if line == "TACK_FACT ec2_tags_start" {
			inEC2Tags = true
			gotIMDSTags = true
			continue
		}
		if line == "TACK_FACT ec2_tags_end" {
			inEC2Tags = false
			continue
		}
		if inEC2Tags {
			if idx := strings.Index(line, "="); idx > 0 {
				ec2Tags[line[:idx]] = line[idx+1:]
			}
			continue
		}

		// Parse TACK_FACT lines
		if !strings.HasPrefix(line, "TACK_FACT ") {
			continue
		}
		kv := strings.TrimPrefix(line, "TACK_FACT ")
		idx := strings.Index(kv, "=")
		if idx < 0 {
			continue
		}
		key := kv[:idx]
		value := kv[idx+1:]

		switch key {
		case "os_type":
			facts["os_type"] = value
			applyOSType(facts, value)
		case "architecture":
			facts["architecture"] = value
			facts["arch"] = normalizeArch(value)
		case "kernel":
			facts["kernel"] = value
		case "hostname":
			facts["hostname"] = value
		case "user":
			facts["user"] = value
		case "home":
			facts["home"] = value
		case "os_version":
			facts["os_version"] = value
		case "os_name":
			facts["os_name"] = value
		case "default_ipv4", "default_interface", "default_ipv4_subnet", "default_ipv4_prefix":
			if value != "" {
				facts[key] = value
			}
		case "all_ipv4", "all_ipv6":
			if value != "" {
				facts[key] = strings.Split(value, ",")
			}
		case "ec2_instance_id", "ec2_region", "ec2_az", "ec2_instance_type", "ec2_ami_id",
			"ec2_private_ip", "ec2_public_ip":
			if value != "" {
				facts[key] = value
			}
		default:
			if strings.HasPrefix(key, "env_") {
				envName := strings.TrimPrefix(key, "env_")
				if value != "" {
					env[envName] = value
				}
			}
		}
	}

	if len(env) > 0 {
		facts["env"] = env
	}

	// Set ec2_tags from IMDS or fall back to EC2 API
	if gotIMDSTags {
		facts["ec2_tags"] = ec2Tags
	} else if instanceID, _ := facts["ec2_instance_id"].(string); instanceID != "" {
		region, _ := facts["ec2_region"].(string)
		if region != "" {
			tags, err := gatherEC2Tags(ctx, instanceID, region)
			if err == nil && len(tags) > 0 {
				facts["ec2_tags"] = tags
			}
		}
	}

	return facts, nil
}

// applyOSType sets os_family and pkg_manager for the top-level OS type.
func applyOSType(facts map[string]any, osType string) {
	switch osType {
	case "Darwin":
		facts["os_family"] = "Darwin"
		facts["pkg_manager"] = "brew"
	case "Linux":
		facts["os_family"] = "Linux"
	}
}

// applyOSRelease extracts distribution info and refines os_family/pkg_manager.
func applyOSRelease(facts map[string]any, osRelease map[string]string) {
	if id, ok := osRelease["ID"]; ok {
		facts["distribution"] = id
	}
	if version, ok := osRelease["VERSION_ID"]; ok {
		facts["distribution_version"] = version
	}
	if name, ok := osRelease["PRETTY_NAME"]; ok {
		facts["os_name"] = name
	}

	switch facts["distribution"] {
	case "ubuntu", "debian", "linuxmint", "pop":
		facts["pkg_manager"] = "apt"
		facts["os_family"] = "Debian"
	case "fedora", "rhel", "centos", "rocky", "almalinux":
		facts["pkg_manager"] = "dnf"
		facts["os_family"] = "RedHat"
	case "arch", "manjaro":
		facts["pkg_manager"] = "pacman"
		facts["os_family"] = "Arch"
	case "alpine":
		facts["pkg_manager"] = "apk"
		facts["os_family"] = "Alpine"
	case "opensuse", "sles":
		facts["pkg_manager"] = "zypper"
		facts["os_family"] = "Suse"
	}
}

// normalizeArch maps raw architecture strings to canonical names.
func normalizeArch(arch string) string {
	switch arch {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "armv7l":
		return "arm"
	default:
		return arch
	}
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
