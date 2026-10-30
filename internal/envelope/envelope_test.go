//go:build unit

package envelope_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/abrosimov/gh-repo-settings/internal/envelope"
)

// Produced by `ssh-keygen -Y sign -n tofu-state@gh-repo-settings` over the challenge, using the key with this seed.
const (
	goldenSeed        = "3783b8eea8a582e6a963a87c6d339bfb6a2cf7c7d387534e8dc24ecbf0ebcf48"
	goldenFingerprint = "SHA256:dhUYqSCPkOLBkVdXhkSRYlwuSItbIdGexFppeDUM3CU"
	goldenSignature   = "c33eddd72345c8adbb2232efee839995cf09bd33afdc93e094db93cadef9fc0a" +
		"46bab14b61743c916909befdf1eeac4659cd8fa52c97afd1f78e7aa525589a0e"
)

type testKey struct {
	private any
	public  ssh.PublicKey
	comment string
}

func newEd25519Key(t *testing.T, comment string) testKey {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ed25519 key: %v", err)
	}
	return newTestKey(t, private, public, comment)
}

func newTestKey(t *testing.T, private, public any, comment string) testKey {
	t.Helper()
	sshPublic, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatalf("converting public key: %v", err)
	}
	return testKey{private: private, public: sshPublic, comment: comment}
}

func newKeyring(t *testing.T, keys ...testKey) agent.Agent {
	t.Helper()
	keyring := agent.NewKeyring()
	for _, key := range keys {
		if err := keyring.Add(agent.AddedKey{PrivateKey: key.private, Comment: key.comment}); err != nil {
			t.Fatalf("adding %s to keyring: %v", key.comment, err)
		}
	}
	return keyring
}

func recipientsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "recipients.json")
}

func mustInit(t *testing.T, a envelope.Agent, path string) []envelope.Recipient {
	t.Helper()
	added, err := envelope.Init(a, path)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return added
}

func mustLoad(t *testing.T, path string) *envelope.File {
	t.Helper()
	file, err := envelope.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return file
}

func mustUnwrap(t *testing.T, a envelope.Agent, path string) []byte {
	t.Helper()
	dataKey, err := envelope.UnwrapDataKey(a, mustLoad(t, path))
	if err != nil {
		t.Fatalf("UnwrapDataKey: %v", err)
	}
	return dataKey
}

func fingerprints(recipients []envelope.Recipient) []string {
	result := make([]string, 0, len(recipients))
	for i := range recipients {
		result = append(result, recipients[i].Fingerprint)
	}
	slices.Sort(result)
	return result
}

func fingerprintsOf(keys ...testKey) []string {
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, ssh.FingerprintSHA256(key.public))
	}
	slices.Sort(result)
	return result
}

func TestDeriveKEKIsDeterministicPerKey(t *testing.T) {
	alice := newEd25519Key(t, "alice")
	bob := newEd25519Key(t, "bob")
	keyring := newKeyring(t, alice, bob)

	first, err := envelope.DeriveKEK(keyring, alice.public)
	if err != nil {
		t.Fatalf("DeriveKEK(alice): %v", err)
	}
	again, err := envelope.DeriveKEK(newKeyring(t, alice), alice.public)
	if err != nil {
		t.Fatalf("DeriveKEK(alice) from a second agent: %v", err)
	}
	other, err := envelope.DeriveKEK(keyring, bob.public)
	if err != nil {
		t.Fatalf("DeriveKEK(bob): %v", err)
	}

	if len(first) != 32 {
		t.Fatalf("KEK length = %d, want 32", len(first))
	}
	if !bytes.Equal(first, again) {
		t.Error("KEK differs between derivations for the same key")
	}
	if bytes.Equal(first, other) {
		t.Error("KEK is identical for two different keys")
	}
}

func TestDeriveKEKMatchesSSHKeygenSignature(t *testing.T) {
	seed, err := hex.DecodeString(goldenSeed)
	if err != nil {
		t.Fatalf("decoding seed: %v", err)
	}
	signature, err := hex.DecodeString(goldenSignature)
	if err != nil {
		t.Fatalf("decoding signature: %v", err)
	}
	private := ed25519.NewKeyFromSeed(seed)
	key := newTestKey(t, private, private.Public(), "golden")
	if got := ssh.FingerprintSHA256(key.public); got != goldenFingerprint {
		t.Fatalf("fingerprint = %s, want %s", got, goldenFingerprint)
	}

	kek, err := envelope.DeriveKEK(newKeyring(t, key), key.public)
	if err != nil {
		t.Fatalf("DeriveKEK: %v", err)
	}

	want, err := hkdf.Key(sha256.New, signature, []byte(goldenFingerprint), "tofu-state@gh-repo-settings kek-v1", 32)
	if err != nil {
		t.Fatalf("hkdf: %v", err)
	}
	if !bytes.Equal(kek, want) {
		t.Errorf("KEK = %x, want %x (derived from the ssh-keygen signature)", kek, want)
	}
}

func TestDeriveKEKRejectsNonEd25519Key(t *testing.T) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating ecdsa key: %v", err)
	}
	key := newTestKey(t, private, &private.PublicKey, "ecdsa")

	_, err = envelope.DeriveKEK(newKeyring(t, key), key.public)

	if !errors.Is(err, envelope.ErrUnsupportedKeyType) {
		t.Fatalf("err = %v, want ErrUnsupportedKeyType", err)
	}
}

func TestInitWrapsForEveryEd25519KeyAndSkipsOthers(t *testing.T) {
	alice := newEd25519Key(t, "alice@laptop-1")
	bob := newEd25519Key(t, "bob@laptop-2")
	ecdsaPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating ecdsa key: %v", err)
	}
	rsaPrivate, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating rsa key: %v", err)
	}
	ecdsaKey := newTestKey(t, ecdsaPrivate, &ecdsaPrivate.PublicKey, "ecdsa")
	rsaKey := newTestKey(t, rsaPrivate, &rsaPrivate.PublicKey, "rsa")
	path := recipientsPath(t)

	added := mustInit(t, newKeyring(t, ecdsaKey, alice, rsaKey, bob), path)

	if got, want := fingerprints(added), fingerprintsOf(alice, bob); !slices.Equal(got, want) {
		t.Fatalf("added = %v, want %v", got, want)
	}
	file := mustLoad(t, path)
	if file.Version != envelope.FormatVersion {
		t.Errorf("version = %d, want %d", file.Version, envelope.FormatVersion)
	}
	if got, want := fingerprints(file.Recipients), fingerprintsOf(alice, bob); !slices.Equal(got, want) {
		t.Errorf("recipients = %v, want %v", got, want)
	}
	for i := range file.Recipients {
		r := &file.Recipients[i]
		if r.Fingerprint == ssh.FingerprintSHA256(alice.public) {
			if r.Comment != alice.comment || r.PublicKey != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(alice.public))) {
				t.Errorf("alice recipient = %+v, want comment %q and alice's authorised key", r, alice.comment)
			}
		}
	}
}

func TestInitWritesSortedRecipientsWithoutTheDataKey(t *testing.T) {
	keys := []testKey{newEd25519Key(t, "a"), newEd25519Key(t, "b"), newEd25519Key(t, "c")}
	keyring := newKeyring(t, keys...)
	path := recipientsPath(t)

	mustInit(t, keyring, path)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading recipients file: %v", err)
	}
	var file envelope.File
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parsing recipients file: %v", err)
	}
	if !slices.IsSortedFunc(file.Recipients, func(a, b envelope.Recipient) int {
		return strings.Compare(a.Fingerprint, b.Fingerprint)
	}) {
		t.Errorf("recipients are not sorted by fingerprint: %v", fingerprints(file.Recipients))
	}
	dataKey := mustUnwrap(t, keyring, path)
	if bytes.Contains(raw, dataKey) || strings.Contains(string(raw), hex.EncodeToString(dataKey)) {
		t.Error("recipients file contains the plaintext data key")
	}
}

func TestInitRefusesWhenFileExists(t *testing.T) {
	keyring := newKeyring(t, newEd25519Key(t, "alice"))
	path := recipientsPath(t)
	mustInit(t, keyring, path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading recipients file: %v", err)
	}

	_, err = envelope.Init(keyring, path)

	if !errors.Is(err, envelope.ErrRecipientsFileExists) {
		t.Fatalf("err = %v, want ErrRecipientsFileExists", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading recipients file: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("recipients file changed after a refused init")
	}
}

func TestInitRefusesWithoutEd25519Key(t *testing.T) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating ecdsa key: %v", err)
	}
	path := recipientsPath(t)

	_, err = envelope.Init(newKeyring(t, newTestKey(t, private, &private.PublicKey, "ecdsa")), path)

	if !errors.Is(err, envelope.ErrNoEd25519Key) {
		t.Fatalf("err = %v, want ErrNoEd25519Key", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("recipients file exists after a refused init (stat: %v)", statErr)
	}
}

func TestUnwrapDataKeyWithAnyEnrolledKeyYieldsTheSameKey(t *testing.T) {
	alice := newEd25519Key(t, "alice")
	bob := newEd25519Key(t, "bob")
	path := recipientsPath(t)
	mustInit(t, newKeyring(t, alice, bob), path)

	fromAlice := mustUnwrap(t, newKeyring(t, alice), path)
	fromBob := mustUnwrap(t, newKeyring(t, bob), path)

	if len(fromAlice) != 32 {
		t.Fatalf("data key length = %d, want 32", len(fromAlice))
	}
	if !bytes.Equal(fromAlice, fromBob) {
		t.Error("enrolled keys unwrap different data keys")
	}
}

func TestUnwrapDataKeyFailsClosedWithoutEnrolledKey(t *testing.T) {
	path := recipientsPath(t)
	mustInit(t, newKeyring(t, newEd25519Key(t, "alice")), path)
	file := mustLoad(t, path)

	for name, keyring := range map[string]agent.Agent{
		"empty agent":      newKeyring(t),
		"non-enrolled key": newKeyring(t, newEd25519Key(t, "mallory")),
		"two non-enrolled": newKeyring(t, newEd25519Key(t, "mallory"), newEd25519Key(t, "trudy")),
	} {
		t.Run(name, func(t *testing.T) {
			dataKey, err := envelope.UnwrapDataKey(keyring, file)

			if !errors.Is(err, envelope.ErrNoEnrolledKey) {
				t.Fatalf("err = %v, want ErrNoEnrolledKey", err)
			}
			if dataKey != nil {
				t.Errorf("data key = %x, want none", dataKey)
			}
		})
	}
}

func TestUnwrapDataKeyRejectsSwappedWrappedKey(t *testing.T) {
	alice := newEd25519Key(t, "alice")
	bob := newEd25519Key(t, "bob")
	path := recipientsPath(t)
	mustInit(t, newKeyring(t, alice, bob), path)
	file := mustLoad(t, path)
	file.Recipients[0].WrappedKey, file.Recipients[1].WrappedKey = file.Recipients[1].WrappedKey, file.Recipients[0].WrappedKey
	file.Recipients[0].Nonce, file.Recipients[1].Nonce = file.Recipients[1].Nonce, file.Recipients[0].Nonce

	_, err := envelope.UnwrapDataKey(newKeyring(t, alice), file)

	if err == nil || errors.Is(err, envelope.ErrNoEnrolledKey) {
		t.Fatalf("err = %v, want an unwrap failure", err)
	}
}

func TestEnrollAddsOnlyNewEd25519Keys(t *testing.T) {
	alice := newEd25519Key(t, "alice@laptop-1")
	bob := newEd25519Key(t, "bob@laptop-2")
	ecdsaPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating ecdsa key: %v", err)
	}
	ecdsaKey := newTestKey(t, ecdsaPrivate, &ecdsaPrivate.PublicKey, "ecdsa")
	path := recipientsPath(t)
	mustInit(t, newKeyring(t, alice), path)
	original := mustUnwrap(t, newKeyring(t, alice), path)

	added, err := envelope.Enroll(newKeyring(t, ecdsaKey, alice, bob), path)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	if got, want := fingerprints(added), fingerprintsOf(bob); !slices.Equal(got, want) {
		t.Fatalf("added = %v, want %v", got, want)
	}
	if got, want := fingerprints(mustLoad(t, path).Recipients), fingerprintsOf(alice, bob); !slices.Equal(got, want) {
		t.Errorf("recipients = %v, want %v", got, want)
	}
	if fromBob := mustUnwrap(t, newKeyring(t, bob), path); !bytes.Equal(fromBob, original) {
		t.Error("newly enrolled key unwraps a different data key")
	}
}

func TestEnrollIsNoOpWhenNothingIsNew(t *testing.T) {
	keyring := newKeyring(t, newEd25519Key(t, "alice"))
	path := recipientsPath(t)
	mustInit(t, keyring, path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading recipients file: %v", err)
	}

	added, err := envelope.Enroll(keyring, path)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	if len(added) != 0 {
		t.Errorf("added = %v, want none", fingerprints(added))
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading recipients file: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("recipients file changed after a no-op enrol")
	}
}

func TestEnrollRequiresAnEnrolledKeyInTheAgent(t *testing.T) {
	path := recipientsPath(t)
	mustInit(t, newKeyring(t, newEd25519Key(t, "alice")), path)

	_, err := envelope.Enroll(newKeyring(t, newEd25519Key(t, "bob")), path)

	if !errors.Is(err, envelope.ErrNoEnrolledKey) {
		t.Fatalf("err = %v, want ErrNoEnrolledKey", err)
	}
	if got := len(mustLoad(t, path).Recipients); got != 1 {
		t.Errorf("recipients = %d, want 1", got)
	}
}

func TestRevokeRemovesRecipient(t *testing.T) {
	alice := newEd25519Key(t, "alice")
	bob := newEd25519Key(t, "bob")
	path := recipientsPath(t)
	mustInit(t, newKeyring(t, alice, bob), path)
	bobFingerprint := ssh.FingerprintSHA256(bob.public)

	revoked, err := envelope.Revoke(path, bobFingerprint)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	if revoked.Fingerprint != bobFingerprint || revoked.Comment != "bob" {
		t.Errorf("revoked = %s (%s), want %s (bob)", revoked.Fingerprint, revoked.Comment, bobFingerprint)
	}
	if got, want := fingerprints(mustLoad(t, path).Recipients), fingerprintsOf(alice); !slices.Equal(got, want) {
		t.Errorf("recipients = %v, want %v", got, want)
	}
	_, err = envelope.UnwrapDataKey(newKeyring(t, bob), mustLoad(t, path))
	if !errors.Is(err, envelope.ErrNoEnrolledKey) {
		t.Errorf("unwrap with revoked key: err = %v, want ErrNoEnrolledKey", err)
	}
}

func TestRevokeRefusesLastRecipient(t *testing.T) {
	alice := newEd25519Key(t, "alice")
	path := recipientsPath(t)
	mustInit(t, newKeyring(t, alice), path)

	_, err := envelope.Revoke(path, ssh.FingerprintSHA256(alice.public))

	if !errors.Is(err, envelope.ErrLastRecipient) {
		t.Fatalf("err = %v, want ErrLastRecipient", err)
	}
	if got := len(mustLoad(t, path).Recipients); got != 1 {
		t.Errorf("recipients = %d, want 1", got)
	}
}

func TestRevokeRejectsUnknownFingerprint(t *testing.T) {
	path := recipientsPath(t)
	mustInit(t, newKeyring(t, newEd25519Key(t, "alice"), newEd25519Key(t, "bob")), path)

	_, err := envelope.Revoke(path, ssh.FingerprintSHA256(newEd25519Key(t, "carol").public))

	if !errors.Is(err, envelope.ErrUnknownRecipient) {
		t.Fatalf("err = %v, want ErrUnknownRecipient", err)
	}
}

func TestLoadRejectsTamperedRecipient(t *testing.T) {
	path := recipientsPath(t)
	mustInit(t, newKeyring(t, newEd25519Key(t, "alice")), path)
	file := mustLoad(t, path)
	file.Recipients[0].PublicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(newEd25519Key(t, "mallory").public)))
	if err := file.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err := envelope.Load(path)

	if !errors.Is(err, envelope.ErrInvalidRecipient) {
		t.Fatalf("err = %v, want ErrInvalidRecipient", err)
	}
}
