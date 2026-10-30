package envelope

import (
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const (
	Namespace = "tofu-state@gh-repo-settings"

	// Changing any of these values makes every existing recipient undecryptable.
	challenge           = "gh-repo-settings OpenTofu state key-encryption-key v1"
	kekInfo             = Namespace + " kek-v1"
	sshsigMagic         = "SSHSIG"
	sshsigHashAlgorithm = "sha512"

	keySize = 32
)

var (
	ErrUnsupportedKeyType  = errors.New("unsupported key type: only ssh-ed25519 keys are accepted")
	ErrUnexpectedSignature = errors.New("ssh-agent returned a signature that does not verify")
)

type Agent interface {
	List() ([]*agent.Key, error)
	Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error)
}

func DeriveKEK(a Agent, key ssh.PublicKey) ([]byte, error) {
	fingerprint := ssh.FingerprintSHA256(key)
	if key.Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("%w: %s is %s", ErrUnsupportedKeyType, fingerprint, key.Type())
	}

	data := sshsigSignedData()
	signature, err := a.Sign(key, data)
	if err != nil {
		return nil, fmt.Errorf("signing challenge with %s: %w", fingerprint, err)
	}
	if err = key.Verify(data, signature); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnexpectedSignature, fingerprint, err)
	}

	kek, err := hkdf.Key(sha256.New, signature.Blob, []byte(fingerprint), kekInfo, keySize)
	if err != nil {
		return nil, fmt.Errorf("deriving key-encryption key for %s: %w", fingerprint, err)
	}
	return kek, nil
}

// sshsigSignedData builds the blob that `ssh-keygen -Y sign -n <Namespace>` signs (PROTOCOL.sshsig).
func sshsigSignedData() []byte {
	digest := sha512.Sum512([]byte(challenge))
	body := ssh.Marshal(struct {
		Namespace     string
		Reserved      string
		HashAlgorithm string
		Hash          []byte
	}{
		Namespace:     Namespace,
		HashAlgorithm: sshsigHashAlgorithm,
		Hash:          digest[:],
	})
	return append([]byte(sshsigMagic), body...)
}
