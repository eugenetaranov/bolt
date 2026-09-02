// Package vault provides AES-256-GCM encryption and decryption with Argon2id key derivation.
// Vault files use a versioned text format with a magic header followed by base64-encoded ciphertext.
// Secrets are handled as []byte throughout to allow memory zeroing after use.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	// magicPrefix is the versioned identifier for Tack vault files.
	// Format: $TACK_VAULT;<version>;<algorithm>
	magicPrefix = "$TACK_VAULT;1.0;AES256-GCM"

	saltSize  = 16 // bytes — Argon2id salt
	nonceSize = 12 // bytes — AES-GCM standard nonce
	keySize   = 32 // bytes — AES-256 key

	// Argon2id parameters (2026 hardened minimum per RFC 9106 and PITFALLS.md).
	// time=3 is used rather than the RFC absolute minimum of 1.
	argon2Time    = 3
	argon2Memory  = 65536 // KiB = 64 MB
	argon2Threads = 4

	// Upper bounds on KDF parameters read from a (possibly hostile) vault
	// header. Without these, a tampered header — e.g. m=4294967295 — would make
	// argon2.IDKey attempt a multi-terabyte allocation and OOM the process,
	// turning a shared/committed vault into a denial-of-service vector.
	argon2MaxTime    = 10
	argon2MaxMemory  = 1 << 20 // 1 GiB expressed in KiB
	argon2MaxThreads = 8
)

// vaultParams holds the KDF parameters extracted from a vault file header.
// Stored in the header to allow future vaults to use different parameters
// without a file format version bump (D-03).
type vaultParams struct {
	time    uint32
	memory  uint32
	threads uint8
}

// testKeyHook is nil in production. Tests set it to capture the derived key
// slice before it is zeroed, enabling verification of CRYPT-05.
var testKeyHook func([]byte)

// Encrypt encrypts plaintext with AES-256-GCM using a key derived from password via Argon2id.
// Returns the complete vault file content: a header line followed by a base64 ciphertext line,
// each terminated by a newline. A fresh random salt and nonce are generated on every call,
// ensuring two encryptions of the same plaintext produce different ciphertexts (CRYPT-03).
// The derived key is zeroed in memory before the function returns (CRYPT-05).
func Encrypt(plaintext, password []byte) ([]byte, error) {
	// Generate fresh salt and nonce from cryptographically secure random source (D-06).
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("vault: generate salt: %w", err)
	}

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("vault: generate nonce: %w", err)
	}

	// Derive a 32-byte AES-256 key using Argon2id (D-05).
	key := argon2.IDKey(password, salt, argon2Time, argon2Memory, argon2Threads, keySize)
	defer func() {
		if testKeyHook != nil {
			testKeyHook(key)
		}
		for i := range key {
			key[i] = 0
		}
	}()

	// Set up AES-256-GCM authenticated encryption (D-04).
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: create GCM: %w", err)
	}

	// Build the ciphertext blob: salt || nonce || ciphertext+tag (D-07).
	blob := make([]byte, 0, saltSize+nonceSize+len(plaintext)+gcm.Overhead())
	blob = append(blob, salt...)
	blob = append(blob, nonce...)
	blob = gcm.Seal(blob, nonce, plaintext, nil)

	// Compose the vault file: header line + base64 blob line (D-01, D-02).
	header := fmt.Sprintf("%s;t=%d,m=%d,p=%d", magicPrefix, argon2Time, argon2Memory, argon2Threads)
	encoded := base64.StdEncoding.EncodeToString(blob)
	return []byte(header + "\n" + encoded + "\n"), nil
}

// Decrypt decrypts vault file content produced by Encrypt.
// Returns the plaintext bytes. The caller should zero the returned slice after use (D-10).
// Returns an error if the password is wrong, the file is corrupted, or the format is unsupported.
func Decrypt(data, password []byte) ([]byte, error) {
	// Split header line from base64 blob line.
	content := strings.TrimRight(string(data), "\n")
	idx := strings.IndexByte(content, '\n')
	if idx < 0 {
		return nil, fmt.Errorf("vault: malformed vault file: expected 2 lines, got 1")
	}
	headerLine := content[:idx]
	blobLine := content[idx+1:]

	if blobLine == "" {
		return nil, fmt.Errorf("vault: malformed vault file: missing ciphertext line")
	}

	// Parse header to extract and validate KDF params (CRYPT-02, CRYPT-04).
	params, err := parseHeader(headerLine)
	if err != nil {
		return nil, err
	}

	// Base64-decode the ciphertext blob.
	blob, err := base64.StdEncoding.DecodeString(blobLine)
	if err != nil {
		return nil, fmt.Errorf("vault: decode ciphertext: %w", err)
	}

	// Validate blob has at least salt + nonce + 1 byte of ciphertext.
	if len(blob) < saltSize+nonceSize+1 {
		return nil, fmt.Errorf("vault: ciphertext blob too short (got %d bytes, need at least %d)", len(blob), saltSize+nonceSize+1)
	}

	// Extract salt, nonce, and ciphertext from blob (D-07).
	salt := blob[:saltSize]
	nonce := blob[saltSize : saltSize+nonceSize]
	ct := blob[saltSize+nonceSize:]

	// Derive the decryption key using parameters from the header (CRYPT-02).
	key := argon2.IDKey(password, salt, params.time, params.memory, params.threads, keySize)
	defer func() {
		if testKeyHook != nil {
			testKeyHook(key)
		}
		for i := range key {
			key[i] = 0
		}
	}()

	// Set up AES-256-GCM for decryption.
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: create GCM: %w", err)
	}

	// Decrypt and authenticate. GCM Open failure means wrong password or tampered data.
	// Return a generic error to avoid leaking algorithm details (Pitfall 3).
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("vault: decryption failed: wrong password or corrupted vault")
	}
	return plaintext, nil
}

// parseHeader validates the vault file header line and extracts KDF parameters.
// Returns an error if the header uses an unsupported format version or is malformed.
func parseHeader(line string) (vaultParams, error) {
	// Validate the full magic prefix exactly (Pitfall 5).
	if !strings.HasPrefix(line, magicPrefix) {
		return vaultParams{}, fmt.Errorf("vault: unsupported format version: %q (expected prefix %q)", line, magicPrefix)
	}

	// Extract the KDF params suffix after the magic prefix.
	// Expected format: $TACK_VAULT;1.0;AES256-GCM;t=N,m=N,p=N
	suffix := line[len(magicPrefix):]
	if suffix == "" || suffix[0] != ';' {
		return vaultParams{}, fmt.Errorf("vault: malformed header: missing KDF params after magic prefix")
	}
	paramStr := suffix[1:] // strip leading ';'

	var t, m uint32
	var p uint8
	n, err := fmt.Sscanf(paramStr, "t=%d,m=%d,p=%d", &t, &m, &p)
	if err != nil || n != 3 {
		return vaultParams{}, fmt.Errorf("vault: malformed header: cannot parse KDF params from %q", paramStr)
	}

	// Bound-check before the KDF runs so a hostile header cannot trigger a
	// resource-exhaustion (OOM) decryption. argon2 also requires m >= 8*p.
	if t < 1 || t > argon2MaxTime {
		return vaultParams{}, fmt.Errorf("vault: KDF time parameter t=%d out of range [1,%d]", t, argon2MaxTime)
	}
	if p < 1 || p > argon2MaxThreads {
		return vaultParams{}, fmt.Errorf("vault: KDF parallelism p=%d out of range [1,%d]", p, argon2MaxThreads)
	}
	if minMem := 8 * uint32(p); m < minMem || m > argon2MaxMemory {
		return vaultParams{}, fmt.Errorf("vault: KDF memory m=%d KiB out of range [%d,%d]", m, minMem, argon2MaxMemory)
	}

	return vaultParams{
		time:    t,
		memory:  m,
		threads: p,
	}, nil
}
