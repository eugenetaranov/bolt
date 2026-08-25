package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderMulti(plans []PlannedTask, hosts []string, dryRun bool) string {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)
	o.DisplayMultiHostPlan(plans, hosts, dryRun)
	return buf.String()
}

func TestDisplayMultiHostPlan_ThreeHostsMixed(t *testing.T) {
	hosts := []string{"web1", "web2", "web3"}
	plans := []PlannedTask{
		{Host: "web1", Name: "install nginx", Module: "apt", Status: "will_change"},
		{Host: "web2", Name: "rotate cert", Module: "command", Status: "always_runs"},
		// web3 has only no-op tasks → should not render any line for it.
		{Host: "web3", Name: "ensure file present", Module: "file", Status: "no_change"},
	}

	out := renderMulti(plans, hosts, false)

	// Body content
	assert.Contains(t, out, "web1: ")
	assert.Contains(t, out, "web2: ")
	assert.NotContains(t, out, "web3: ", "no-op host should not contribute body lines")

	// Indicators
	assert.Contains(t, out, "+ apt: install nginx", "will_change uses + indicator")
	assert.Contains(t, out, "~ command: rotate cert", "always_runs uses ~ indicator")

	// Footer
	assert.Contains(t, out, "across 3 hosts")
	assert.Contains(t, out, "(1 unchanged)")
	assert.Contains(t, out, "1 to change")
	assert.Contains(t, out, "1 to run")
	assert.Contains(t, out, "1 ok")
}

func TestDisplayMultiHostPlan_FiftyHostsMostlyNoOp(t *testing.T) {
	hosts := make([]string, 50)
	for i := range hosts {
		hosts[i] = strings.Repeat("a", 0) + "host" + itoa(i+1)
	}

	var plans []PlannedTask
	// 3 changing hosts
	plans = append(plans, PlannedTask{Host: "host1", Name: "install nginx", Module: "apt", Status: "will_change"})
	plans = append(plans, PlannedTask{Host: "host2", Name: "restart redis", Module: "service", Status: "always_runs"})
	plans = append(plans, PlannedTask{Host: "host3", Name: "rotate cert", Module: "command", Status: "will_run"})
	// 47 no-op hosts each contribute one no_change task
	for i := 4; i <= 50; i++ {
		plans = append(plans, PlannedTask{
			Host: "host" + itoa(i), Name: "ensure", Module: "file", Status: "no_change",
		})
	}

	out := renderMulti(plans, hosts, false)

	// Only the 3 changing hosts get body lines
	for _, h := range []string{"host1", "host2", "host3"} {
		assert.Contains(t, out, h, "expected changing host %s in body", h)
	}
	assert.NotContains(t, out, "host4 ")
	assert.NotContains(t, out, "host50 ")

	// Footer reflects 47 unchanged
	assert.Contains(t, out, "across 50 hosts (47 unchanged)")
}

func TestDisplayMultiHostPlan_LongHostnameTruncation(t *testing.T) {
	long := strings.Repeat("h", 35)
	short := "web1"
	hosts := []string{long, short}
	plans := []PlannedTask{
		{Host: long, Name: "install pkg", Module: "apt", Status: "will_change"},
		{Host: short, Name: "install pkg", Module: "apt", Status: "will_change"},
	}

	out := renderMulti(plans, hosts, false)

	// 35-char hostname truncates to 29 chars + "…", followed by ":".
	wantPrefix := strings.Repeat("h", 29) + "…:"
	assert.Contains(t, out, wantPrefix,
		"expected truncated long hostname prefix; got: %q", out)

	// Short host is padded to colWidth (=hostColumnMax=30) so its prefix is
	// "web1" + 26 spaces + ":".
	wantShort := "web1" + strings.Repeat(" ", 26) + ":"
	assert.Contains(t, out, wantShort,
		"expected padded short hostname; got: %q", out)
}

func TestDisplayMultiHostPlan_SSMInstanceIDsAlign(t *testing.T) {
	// 19-char instance IDs → all hostnames same length → colWidth = 19.
	hosts := []string{
		"i-0817eea131fa23c39",
		"i-0a7b29ada0a9bc187",
	}
	plans := []PlannedTask{
		{Host: hosts[0], Name: "install nginx", Module: "apt", Status: "will_change"},
		{Host: hosts[1], Name: "rotate cert", Module: "command", Status: "always_runs"},
	}

	out := renderMulti(plans, hosts, false)

	// Each host appears prefixed with its full ID + ": "
	for _, h := range hosts {
		assert.Contains(t, out, h+": ",
			"expected host %s prefix in output: %q", h, out)
	}
}

func TestDisplayMultiHostPlan_AllHostsNoOp(t *testing.T) {
	hosts := []string{"a", "b", "c"}
	plans := []PlannedTask{
		{Host: "a", Name: "ensure", Module: "file", Status: "no_change"},
		{Host: "b", Name: "ensure", Module: "file", Status: "no_change"},
		{Host: "c", Name: "ensure", Module: "file", Status: "no_change"},
	}

	out := renderMulti(plans, hosts, false)

	// No host body lines.
	assert.NotContains(t, out, "a: ")
	assert.NotContains(t, out, "b: ")
	assert.NotContains(t, out, "c: ")

	// All counted as unchanged.
	assert.Contains(t, out, "across 3 hosts (3 unchanged)")
}

func TestDisplayMultiHostPlan_DryRunLabel(t *testing.T) {
	hosts := []string{"a", "b"}
	plans := []PlannedTask{
		{Host: "a", Name: "x", Module: "command", Status: "will_run"},
	}

	out := renderMulti(plans, hosts, true)
	assert.Contains(t, out, "PLAN (dry run)")
}

func TestFormatHostPrefix(t *testing.T) {
	cases := []struct {
		host     string
		col      int
		expected string
	}{
		{"web1", 4, "web1"},
		{"web1", 8, "web1    "}, // padded
		{"i-0817eea131fa23c39", 19, "i-0817eea131fa23c39"},
		{strings.Repeat("a", 35), 30, strings.Repeat("a", 29) + "…"}, // truncated
	}
	for _, tc := range cases {
		got := formatHostPrefix(tc.host, tc.col)
		require.Equal(t, tc.expected, got, "host=%q col=%d", tc.host, tc.col)
	}
}

func TestPlannedTask_HostFieldRoundtrip(t *testing.T) {
	pt := PlannedTask{Host: "web1", Name: "install", Module: "apt", Status: "will_change"}
	require.Equal(t, "web1", pt.Host)
}

// itoa is a tiny helper to avoid importing strconv just for tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestDisplayMultiHostPlan_OmitsTagSkips verifies tag-filtered tasks are omitted
// from the multi-host plan body and reported as "N filtered" in the footer,
// while role-filtered tasks remain visible.
func TestDisplayMultiHostPlan_OmitsTagSkips(t *testing.T) {
	hosts := []string{"web1", "web2"}
	plans := []PlannedTask{
		{Host: "web1", Name: "install nginx", Module: "apt", Status: "will_change"},
		{Host: "web1", Name: "build step", Module: "command", Status: "will_skip", Reason: "skipped (tag)"},
		{Host: "web2", Name: "install nginx", Module: "apt", Status: "will_change"},
		{Host: "web2", Name: "build step", Module: "command", Status: "will_skip", Reason: "skipped (tag)"},
		{Host: "web1", Name: "role only", Module: "apt", Status: "will_skip", Reason: "skipped (role)"},
	}

	out := renderMulti(plans, hosts, false)

	assert.Contains(t, out, "install nginx", "active task should be shown")
	assert.NotContains(t, out, "build step", "tag-filtered task should be omitted")
	assert.Contains(t, out, "role only", "role-filtered task should still be shown")
	assert.Contains(t, out, "2 filtered", "footer should report the filtered count across hosts")
}
