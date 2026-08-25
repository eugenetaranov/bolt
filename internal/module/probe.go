package module

import (
	"context"
	"fmt"
	"strings"

	"github.com/tackhq/tack/internal/connector"
)

// sudoAuthMarkers are substrings (matched case-insensitively) that identify a
// sudo authentication failure in a command's stderr, as opposed to the command
// itself being missing or exiting non-zero for benign reasons.
var sudoAuthMarkers = []string{
	"sudo:",
	"password is required",
	"no tty present",
	"askpass",
	"incorrect password",
	"try again",
}

// sudoFailureHint returns an actionable hint when stderr looks like a sudo
// authentication failure, or "" otherwise.
func sudoFailureHint(stderr string) string {
	lower := strings.ToLower(stderr)
	for _, marker := range sudoAuthMarkers {
		if strings.Contains(lower, marker) {
			return "sudo needs a password on this host — re-run with --sudo-password " +
				"(or -s/--sudo to be prompted), or configure passwordless sudo (NOPASSWD)"
		}
	}
	return ""
}

// CommandAvailable reports whether name exists on the target via `command -v`.
//
// The probe is deliberately run through the connector as-is, so when sudo is
// enabled it is sudo-wrapped like any other command. This surfaces sudo
// authentication problems early with an actionable hint instead of letting
// callers mislabel them as "not a <platform> system".
//
//   - exit 0                         → (true, nil)
//   - sudo-auth failure on the probe → (false, err) carrying the real stderr + hint
//   - any other non-zero exit        → (false, nil) so callers keep their own
//     "not installed" / "not a <platform> system" message
func CommandAvailable(ctx context.Context, conn connector.Connector, name string) (bool, error) {
	res, err := conn.Execute(ctx, "command -v "+name)
	if err != nil {
		return false, fmt.Errorf("probing for %s: %w", name, err)
	}
	if res.ExitCode == 0 {
		return true, nil
	}
	if hint := sudoFailureHint(res.Stderr); hint != "" {
		return false, fmt.Errorf("cannot check for %s: %s\n  %s",
			name, strings.TrimSpace(res.Stderr), hint)
	}
	return false, nil
}
