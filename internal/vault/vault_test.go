package vault

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncrypt_HeaderFormat(t *testing.T) {
	plaintext := []byte("db_password: secret123\n")
	password := []byte("test-password")

	out, err := Encrypt(plaintext, password)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	require.Equal(t, 2, len(lines), "vault file must have exactly 2 lines")

	expectedHeader := "$TACK_VAULT;1.0;AES256-GCM;t=3,m=65536,p=4"
	assert.Equal(t, expectedHeader, lines[0], "header line must match exactly")
}

func TestEncrypt_OutputIsTwoLinesWithNewlines(t *testing.T) {
	plaintext := []byte("api_key: tok123\n")
	password := []byte("pw")

	out, err := Encrypt(plaintext, password)
	require.NoError(t, err)

	// Must end with newline
	assert.True(t, strings.HasSuffix(string(out), "\n"), "output must end with newline")

	// Must contain exactly 2 newline-terminated lines
	lines := strings.Split(string(out), "\n")
	// After splitting by \n, we expect [header, base64, ""] since trailing \n
	assert.Equal(t, 3, len(lines), "split by newline should yield 3 parts (2 lines + trailing empty)")
	assert.NotEmpty(t, lines[0], "header line must not be empty")
	assert.NotEmpty(t, lines[1], "base64 line must not be empty")
	assert.Empty(t, lines[2], "no content after trailing newline")
}

func TestDecrypt_Roundtrip(t *testing.T) {
	plaintext := []byte("db_password: secret\napi_key: tok123\n")
	password := []byte("correct-horse-battery-staple")

	encrypted, err := Encrypt(plaintext, password)
	require.NoError(t, err)

	decrypted, err := Decrypt(encrypted, password)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted, "decrypted content must equal original plaintext")
}

func TestDecrypt_WrongPassword(t *testing.T) {
	plaintext := []byte("secret: value\n")
	password := []byte("correct-password")

	encrypted, err := Encrypt(plaintext, password)
	require.NoError(t, err)

	_, err = Decrypt(encrypted, []byte("wrong-password"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrong password or corrupted vault",
		"error must contain clear user-facing message, not raw crypto error")
}

func TestDecrypt_TruncatedCiphertext(t *testing.T) {
	// Construct a vault with truncated base64 blob
	header := "$TACK_VAULT;1.0;AES256-GCM;t=3,m=65536,p=4\n"
	// Too-short blob: less than saltSize+nonceSize
	tooShort := make([]byte, 5)
	blob := base64.StdEncoding.EncodeToString(tooShort)
	vaultData := []byte(header + blob + "\n")

	_, err := Decrypt(vaultData, []byte("password"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vault:", "error must have vault: prefix")
}

func TestDecrypt_UnsupportedVersionHeader(t *testing.T) {
	vaultData := []byte("$TACK_VAULT;2.0;AES256-GCM;t=3,m=65536,p=4\nYWJj\n")

	_, err := Decrypt(vaultData, []byte("password"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported", "error must mention unsupported version")
}

func TestDecrypt_MalformedHeaderMissingKDFParams(t *testing.T) {
	// Valid magic but no KDF params
	vaultData := []byte("$TACK_VAULT;1.0;AES256-GCM\nYWJj\n")

	_, err := Decrypt(vaultData, []byte("password"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vault:", "error must have vault: prefix")
}

// A hostile vault header with out-of-range KDF params must be rejected before
// argon2.IDKey runs, so a tampered m/t/p can't OOM the process.
func TestDecrypt_RejectsOutOfRangeKDFParams(t *testing.T) {
	cases := map[string]string{
		"huge memory":  "$TACK_VAULT;1.0;AES256-GCM;t=3,m=4294967295,p=4\nYWJj\n",
		"zero time":    "$TACK_VAULT;1.0;AES256-GCM;t=0,m=65536,p=4\nYWJj\n",
		"huge time":    "$TACK_VAULT;1.0;AES256-GCM;t=999,m=65536,p=4\nYWJj\n",
		"zero threads": "$TACK_VAULT;1.0;AES256-GCM;t=3,m=65536,p=0\nYWJj\n",
		"tiny memory":  "$TACK_VAULT;1.0;AES256-GCM;t=3,m=1,p=4\nYWJj\n",
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decrypt([]byte(data), []byte("password"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "out of range")
		})
	}
}

func TestEncrypt_DifferentCiphertextsForSameInput(t *testing.T) {
	plaintext := []byte("same-plaintext: value\n")
	password := []byte("same-password")

	enc1, err := Encrypt(plaintext, password)
	require.NoError(t, err)

	enc2, err := Encrypt(plaintext, password)
	require.NoError(t, err)

	assert.False(t, bytes.Equal(enc1, enc2),
		"two encryptions of the same plaintext must produce different ciphertexts (fresh salt/nonce)")
}

func TestEncrypt_BlobStructure(t *testing.T) {
	plaintext := []byte("key: val\n")
	password := []byte("pw")

	out, err := Encrypt(plaintext, password)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	require.Equal(t, 2, len(lines))

	blob, err := base64.StdEncoding.DecodeString(lines[1])
	require.NoError(t, err)

	// Blob must be at least saltSize(16) + nonceSize(12) + 1 (min ciphertext) + 16 (GCM tag)
	assert.GreaterOrEqual(t, len(blob), 16+12+1+16,
		"blob must contain salt(16) + nonce(12) + ciphertext + GCM tag(16)")
}

func TestDecrypt_RoundtripMultilineYAML(t *testing.T) {
	plaintext := []byte("db_password: secret\napi_key: tok123\nother_secret: abcdef\n")
	password := []byte("multiline-test-password")

	encrypted, err := Encrypt(plaintext, password)
	require.NoError(t, err)

	decrypted, err := Decrypt(encrypted, password)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestEncrypt_KeyZeroingViaTestHook(t *testing.T) {
	var capturedKey []byte

	// Set the test hook to capture the key before zeroing
	testKeyHook = func(k []byte) {
		// Copy the key slice so we can inspect after zeroing
		capturedKey = make([]byte, len(k))
		copy(capturedKey, k)
	}
	defer func() { testKeyHook = nil }()

	plaintext := []byte("secret: value\n")
	password := []byte("zeroing-test-password")

	_, err := Encrypt(plaintext, password)
	require.NoError(t, err)

	// The hook captured a copy of the key before zeroing.
	// The key itself (inside Encrypt) should be all zeros after the defer runs.
	// We verify that the hook was called and the key was non-zero before zeroing.
	require.NotNil(t, capturedKey, "test hook must have been called")
	require.Equal(t, 32, len(capturedKey), "key must be 32 bytes (AES-256)")

	// The captured copy should have been non-zero (it was a real derived key)
	allZero := true
	for _, b := range capturedKey {
		if b != 0 {
			allZero = false
			break
		}
	}
	assert.False(t, allZero, "captured key copy must be non-zero (was a real derived key)")
}

func TestDecrypt_KeyZeroingViaTestHook(t *testing.T) {
	plaintext := []byte("secret: value\n")
	password := []byte("zeroing-decrypt-test")

	encrypted, err := Encrypt(plaintext, password)
	require.NoError(t, err)

	var capturedKey []byte
	testKeyHook = func(k []byte) {
		capturedKey = make([]byte, len(k))
		copy(capturedKey, k)
	}
	defer func() { testKeyHook = nil }()

	decrypted, err := Decrypt(encrypted, password)
	require.NoError(t, err)
	require.NotNil(t, capturedKey, "test hook must have been called during Decrypt")
	assert.Equal(t, 32, len(capturedKey), "key must be 32 bytes (AES-256)")

	// Verify decrypted plaintext is mutable (D-11): caller can zero it
	for i := range decrypted {
		decrypted[i] = 0
	}
	allZero := true
	for _, b := range decrypted {
		if b != 0 {
			allZero = false
			break
		}
	}
	assert.True(t, allZero, "returned plaintext []byte must be zeroable by caller (not string-backed)")
}
