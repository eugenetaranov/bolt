// Package ssh provides a connector for executing commands on remote hosts via SSH.
package ssh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sshconfig "github.com/kevinburke/ssh_config"
	"github.com/pkg/sftp"
	kh "github.com/skeema/knownhosts"
	"github.com/tackhq/tack/internal/connector"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Default SSH settings.
const (
	defaultPort    = 22
	defaultTimeout = 30 * time.Second
)

// defaultKeyFiles are the private key paths to try, relative to ~/.ssh/.
var defaultKeyFiles = []string{
	"id_ed25519",
	"id_rsa",
	"id_ecdsa",
}

// Connector executes commands on remote hosts via SSH.
type Connector struct {
	host            string
	hostname        string
	user            string
	port            int
	keyFile         string
	password        string
	timeout         time.Duration
	sudo            bool
	sudoPassword    string
	becomeMethod    string
	becomeUser      string
	insecureHostKey bool

	// passwordPrompt, if set, is called lazily to obtain a password for
	// SSH auth -- only invoked by the ssh library itself when it actually
	// attempts password authentication (i.e. key/agent auth wasn't
	// available or the server rejected it), never eagerly. Ignored when
	// password is already set explicitly.
	passwordPrompt   func() (string, error)
	promptedPassword string
	promptedOK       bool

	client       *ssh.Client
	sftpClient   *sftp.Client
	authWarnings []string
}

// Option configures the SSH connector.
type Option func(*Connector)

// WithUser overrides the SSH config user.
func WithUser(user string) Option {
	return func(c *Connector) {
		c.user = user
	}
}

// WithPort overrides the SSH config port.
func WithPort(port int) Option {
	return func(c *Connector) {
		c.port = port
	}
}

// WithKeyFile sets an explicit private key path.
func WithKeyFile(path string) Option {
	return func(c *Connector) {
		c.keyFile = path
	}
}

// WithPassword enables password authentication.
func WithPassword(password string) Option {
	return func(c *Connector) {
		c.password = password
	}
}

// WithPasswordPrompt sets a lazy fallback password source, used only if
// no explicit password is set (WithPassword) and the SSH library itself
// attempts password authentication -- i.e. key/agent auth was
// unavailable or the server rejected it. The prompt fires at most once
// per connector, even if the library calls back more than once.
func WithPasswordPrompt(prompt func() (string, error)) Option {
	return func(c *Connector) {
		c.passwordPrompt = prompt
	}
}

// WithTimeout sets the connection timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Connector) {
		c.timeout = d
	}
}

// WithInsecureHostKey skips SSH host key verification.
func WithInsecureHostKey() Option {
	return func(c *Connector) {
		c.insecureHostKey = true
	}
}

// WithSudo enables sudo for command execution.
func WithSudo() Option {
	return func(c *Connector) {
		c.sudo = true
	}
}

// WithSudoPassword sets the sudo password.
func WithSudoPassword(password string) Option {
	return func(c *Connector) {
		c.sudoPassword = password
	}
}

// New creates a new SSH connector for the specified host.
// The host is looked up in ~/.ssh/config to resolve connection parameters.
func New(host string, opts ...Option) *Connector {
	c := &Connector{
		host:    host,
		timeout: defaultTimeout,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Connect establishes an SSH connection to the target host.
func (c *Connector) Connect(ctx context.Context) error {
	// Resolve SSH config for the host alias
	c.resolveSSHConfig()

	// Build authentication methods
	authMethods := c.buildAuthMethods()
	if len(authMethods) == 0 {
		return fmt.Errorf("no SSH authentication methods available for %s", c.host)
	}

	addr := net.JoinHostPort(c.hostname, strconv.Itoa(c.port))

	config := &ssh.ClientConfig{
		User:    c.user,
		Auth:    authMethods,
		Timeout: c.timeout,
	}

	// Build host key verification.
	if c.insecureHostKey {
		config.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	} else {
		cb, algos, err := knownHostsConfig(addr)
		if err != nil {
			// Fail closed: never silently disable host-key verification. A
			// missing or unparseable known_hosts is treated as "cannot verify",
			// exactly as OpenSSH's StrictHostKeyChecking=yes would.
			return fmt.Errorf("cannot verify SSH host key for %s: %w\n"+
				"  tack refuses to connect without host-key verification.\n"+
				"  pin the host first: ssh-keyscan -H %s >> ~/.ssh/known_hosts\n"+
				"  or, only if you accept the MITM risk, connect with --insecure (TACK_SSH_INSECURE=1)",
				addr, err, c.hostname)
		}
		config.HostKeyCallback = cb
		// Restrict host-key negotiation to the algorithms actually pinned
		// for this host, matching OpenSSH. Without this the server may
		// present a key type absent from known_hosts (x/crypto's default
		// order deprioritizes ed25519), causing a spurious "key mismatch".
		if len(algos) > 0 {
			config.HostKeyAlgorithms = algos
		}
	}

	// Dial with context support
	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to dial %s: %w", addr, err)
	}

	// Perform SSH handshake
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		msg := fmt.Sprintf("SSH handshake failed for %s (user=%s): %v", addr, c.user, err)
		if strings.Contains(err.Error(), "knownhosts: key mismatch") {
			msg += fmt.Sprintf("\n  the host key for %s does not match ~/.ssh/known_hosts.\n"+
				"  if you trust this host (e.g. it was reinstalled), remove the stale entry and reconnect:\n"+
				"    ssh-keygen -R %s", c.hostname, c.hostname)
		}
		if len(c.authWarnings) > 0 {
			msg += "\n  auth warnings:"
			for _, w := range c.authWarnings {
				msg += "\n    - " + w
			}
		}
		return fmt.Errorf("%s", msg)
	}

	c.client = ssh.NewClient(sshConn, chans, reqs)
	return nil
}

// Execute runs a command on the remote host and returns the result.
func (c *Connector) Execute(ctx context.Context, cmd string) (*connector.Result, error) {
	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	session, err := c.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	// Build the command with sudo if configured. Any stdin bytes carry the
	// sudo password, fed over the encrypted channel so it never appears in the
	// remote process argv.
	fullCmd, stdin := c.buildCommand(cmd)

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if stdin != nil {
		// Feed the sudo password through an explicit pipe rather than
		// session.Stdin. sudo authenticates and the wrapped command can exit
		// before all of stdin is consumed; the remote then closes the channel
		// and a session.Stdin copy would surface that as an io.EOF error from
		// Run, masking the real exit status. Writing here and swallowing the
		// (expected, benign) write error keeps Run's error strictly about the
		// command itself.
		stdinPipe, err := session.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("failed to open stdin pipe: %w", err)
		}
		go func() {
			_, _ = stdinPipe.Write(stdin)
			_ = stdinPipe.Close()
		}()
	}

	// Run with context cancellation support
	done := make(chan error, 1)
	go func() {
		done <- session.Run(fullCmd)
	}()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return nil, ctx.Err()
	case err := <-done:
		result := &connector.Result{
			Stdout: stdout.String(),
			Stderr: stderr.String(),
		}

		if err != nil {
			if exitErr, ok := err.(*ssh.ExitError); ok {
				result.ExitCode = exitErr.ExitStatus()
			} else {
				return nil, fmt.Errorf("failed to execute command: %w", err)
			}
		}

		return result, nil
	}
}

// Upload copies content from src to a remote file at dst using SFTP.
// When sudo is enabled, uploads to a temp file first, then moves it
// into place via a sudo shell command (SFTP runs as the SSH user).
func (c *Connector) Upload(ctx context.Context, src io.Reader, dst string, mode uint32) error {
	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	sftpClient, err := c.getSFTPClient()
	if err != nil {
		return err
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// When sudo is active, SFTP can't write directly to privileged paths.
	// Upload to a temp file, then move it into place via sudo.
	target := dst
	if c.sudo && c.user != "root" {
		target = fmt.Sprintf("/tmp/tack-upload-%d", time.Now().UnixNano())
	}

	f, err := sftpClient.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("failed to create remote file %s: %w", target, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, src); err != nil {
		return fmt.Errorf("failed to write to remote file %s: %w", target, err)
	}

	if target == dst {
		// Direct write — set permissions via SFTP
		if err := sftpClient.Chmod(dst, os.FileMode(mode)); err != nil {
			return fmt.Errorf("failed to set permissions on %s: %w", dst, err)
		}
		return nil
	}

	// Sudo path: move temp file to destination and set permissions
	modeStr := fmt.Sprintf("%04o", mode)
	cmd := fmt.Sprintf("mv %s %s && chmod %s %s",
		connector.ShellQuote(target), connector.ShellQuote(dst),
		modeStr, connector.ShellQuote(dst))
	if _, err := connector.Run(ctx, c, cmd); err != nil {
		// Clean up temp file
		_, _ = c.Execute(ctx, fmt.Sprintf("rm -f %s", connector.ShellQuote(target)))
		return fmt.Errorf("failed to move uploaded file to %s: %w", dst, err)
	}

	return nil
}

// Download copies content from a remote file at src to dst using SFTP.
func (c *Connector) Download(ctx context.Context, src string, dst io.Writer) error {
	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	sftpClient, err := c.getSFTPClient()
	if err != nil {
		return err
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	f, err := sftpClient.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open remote file %s: %w", src, err)
	}
	defer f.Close()

	if _, err := io.Copy(dst, f); err != nil {
		return fmt.Errorf("failed to read remote file %s: %w", src, err)
	}

	return nil
}

// Close terminates the SFTP client and SSH connection.
func (c *Connector) Close() error {
	var sftpErr error
	if c.sftpClient != nil {
		sftpErr = c.sftpClient.Close()
		c.sftpClient = nil
	}
	if c.client != nil {
		if err := c.client.Close(); err != nil {
			return err
		}
		c.client = nil
	}
	return sftpErr
}

// String returns a human-readable description of the connection.
func (c *Connector) String() string {
	host := c.hostname
	if host == "" {
		host = c.host
	}
	port := c.port
	if port == 0 {
		port = defaultPort
	}
	desc := fmt.Sprintf("ssh://%s@%s:%d", c.user, host, port)
	if c.sudo {
		desc += " (sudo)"
	}
	return desc
}

// resolveSSHConfig reads ~/.ssh/config and resolves connection parameters.
// Explicit options set via WithUser/WithPort/etc. take precedence.
func (c *Connector) resolveSSHConfig() {
	// Load SSH config
	configPath := filepath.Join(homeDir(), ".ssh", "config")
	f, err := os.Open(configPath)
	if err != nil {
		// No SSH config file — use defaults
		c.applyDefaults()
		return
	}
	defer f.Close()

	cfg, err := sshconfig.Decode(f)
	if err != nil {
		c.applyDefaults()
		return
	}

	// Resolve hostname (SSH config HostName directive)
	if c.hostname == "" {
		hostname, _ := cfg.Get(c.host, "HostName")
		if hostname != "" {
			c.hostname = hostname
		} else {
			c.hostname = c.host
		}
	}

	// Resolve user
	if c.user == "" {
		configUser, _ := cfg.Get(c.host, "User")
		if configUser != "" {
			c.user = configUser
		}
	}

	// Resolve port
	if c.port == 0 {
		portStr, _ := cfg.Get(c.host, "Port")
		if portStr != "" {
			if p, err := strconv.Atoi(portStr); err == nil {
				c.port = p
			}
		}
	}

	// Resolve identity file
	if c.keyFile == "" {
		identityFile, _ := cfg.Get(c.host, "IdentityFile")
		if identityFile != "" {
			c.keyFile = expandPath(identityFile)
		}
	}

	c.applyDefaults()
}

// applyDefaults fills in any remaining unset fields with defaults.
func (c *Connector) applyDefaults() {
	if c.hostname == "" {
		c.hostname = c.host
	}
	if c.user == "" {
		if u, err := user.Current(); err == nil {
			c.user = u.Username
		}
	}
	if c.port == 0 {
		c.port = defaultPort
	}
}

// buildAuthMethods constructs SSH authentication methods in priority order.
// When an explicit key file is set, only that key (and password if set) are
// used — the SSH agent and default key files are skipped so that unrelated
// agent keys don't consume the server's MaxAuthTries budget.
func (c *Connector) buildAuthMethods() []ssh.AuthMethod {
	var methods []ssh.AuthMethod
	c.authWarnings = nil

	if c.keyFile != "" {
		// Explicit key — use only this key, skip agent and defaults.
		path := expandPath(c.keyFile)
		signer, err := loadKey(path)
		if err != nil {
			c.authWarnings = append(c.authWarnings, fmt.Sprintf("key %s: %v", path, err))
		} else if signer != nil {
			methods = append(methods, ssh.PublicKeys(signer))
		}
	} else {
		// No explicit key — try agent, then default key files.
		if agentAuth := c.sshAgentAuth(); agentAuth != nil {
			methods = append(methods, agentAuth)
		}
		if keyAuth := c.keyFileAuth(); keyAuth != nil {
			methods = append(methods, keyAuth)
		}
	}

	// Password auth: explicit password if set, otherwise a lazy prompt
	// fallback -- the ssh library only invokes it if key/agent auth was
	// unavailable or the server rejected it, so it never prompts when
	// key-based auth actually works.
	if c.password != "" {
		methods = append(methods, ssh.Password(c.password))
	} else if c.passwordPrompt != nil {
		methods = append(methods, ssh.PasswordCallback(c.cachedPasswordPrompt))
	}

	return methods
}

// cachedPasswordPrompt calls passwordPrompt at most once per connector,
// caching the result (or the error) for any subsequent calls the ssh
// library makes within the same handshake.
func (c *Connector) cachedPasswordPrompt() (string, error) {
	if c.promptedOK {
		return c.promptedPassword, nil
	}
	pw, err := c.passwordPrompt()
	if err != nil {
		return "", err
	}
	c.promptedPassword = pw
	c.promptedOK = true
	return pw, nil
}

// sshAgentAuth returns an SSH agent auth method if SSH_AUTH_SOCK is available.
func (c *Connector) sshAgentAuth() ssh.AuthMethod {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		c.authWarnings = append(c.authWarnings, "SSH agent not available (SSH_AUTH_SOCK not set)")
		return nil
	}

	conn, err := net.Dial("unix", sock)
	if err != nil {
		c.authWarnings = append(c.authWarnings, fmt.Sprintf("SSH agent connection failed: %v", err))
		return nil
	}

	agentClient := agent.NewClient(conn)
	keys, err := agentClient.List()
	if err != nil || len(keys) == 0 {
		conn.Close()
		c.authWarnings = append(c.authWarnings, "SSH agent has no identities (try ssh-add)")
		return nil
	}

	return ssh.PublicKeysCallback(agentClient.Signers)
}

// keyFileAuth returns a public key auth method from default key files.
func (c *Connector) keyFileAuth() ssh.AuthMethod {
	var signers []ssh.Signer

	sshDir := filepath.Join(homeDir(), ".ssh")
	for _, name := range defaultKeyFiles {
		path := filepath.Join(sshDir, name)
		signer, err := loadKey(path)
		if err != nil {
			c.authWarnings = append(c.authWarnings, fmt.Sprintf("key %s: %v", path, err))
		} else if signer != nil {
			signers = append(signers, signer)
		}
	}

	if len(signers) == 0 {
		return nil
	}

	return ssh.PublicKeys(signers...)
}

// loadKey loads a private key from the given path.
// Returns (nil, nil) if the file does not exist.
func loadKey(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read failed: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("passphrase-protected or invalid key")
	}

	return signer, nil
}

// buildCommand wraps the command with sudo if configured, returning any stdin
// bytes (the sudo password) that must be fed to the process.
func (c *Connector) buildCommand(cmd string) (string, []byte) {
	return connector.WrapBecome(cmd, connector.BecomeConfig{
		Enabled:  c.sudo,
		Method:   c.becomeMethod,
		User:     c.becomeUser,
		Password: c.sudoPassword,
	}, c.user == "root")
}

// SetSudo enables or disables sudo for subsequent commands.
func (c *Connector) SetSudo(enabled bool, password string) {
	c.sudo = enabled
	c.sudoPassword = password
}

// SetBecome configures privilege escalation (method + target user) for
// subsequent commands. Implements connector.Becomer.
func (c *Connector) SetBecome(cfg connector.BecomeConfig) {
	c.sudo = cfg.Enabled
	c.sudoPassword = cfg.Password
	c.becomeMethod = cfg.Method
	c.becomeUser = cfg.User
}

// getSFTPClient returns a cached SFTP client or creates a new one.
func (c *Connector) getSFTPClient() (*sftp.Client, error) {
	if c.sftpClient != nil {
		return c.sftpClient, nil
	}

	client, err := sftp.NewClient(c.client)
	if err != nil {
		return nil, fmt.Errorf("failed to create SFTP client: %w", err)
	}

	c.sftpClient = client
	return client, nil
}

// knownHostsConfig returns a host-key callback and the host-key algorithms
// pinned for addr in ~/.ssh/known_hosts. Scoping the ClientConfig's
// HostKeyAlgorithms to these (as OpenSSH does) ensures the server presents the
// key type that is actually pinned, rather than whatever its default order
// prefers. algos is empty when the host is not in known_hosts (first connect).
func knownHostsConfig(addr string) (ssh.HostKeyCallback, []string, error) {
	knownHostsPath := filepath.Join(homeDir(), ".ssh", "known_hosts")
	db, err := kh.NewDB(knownHostsPath)
	if err != nil {
		return nil, nil, err
	}
	return db.HostKeyCallback(), db.HostKeyAlgorithms(addr), nil
}

// homeDir returns the current user's home directory.
func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

// expandPath expands ~ to the home directory.
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir(), path[2:])
	}
	return path
}

// Ensure Connector implements the connector.Connector interface.
var _ connector.Connector = (*Connector)(nil)
