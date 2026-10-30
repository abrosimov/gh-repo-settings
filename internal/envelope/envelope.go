package envelope

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const wrapAADPrefix = Namespace + " dek-v1 "

var (
	ErrRecipientsFileExists = errors.New("recipients file already exists")
	ErrNoEd25519Key         = errors.New("ssh-agent holds no ed25519 key")
	ErrNoEnrolledKey        = errors.New("ssh-agent holds no enrolled ed25519 key")
	ErrUnknownRecipient     = errors.New("no enrolled recipient has this fingerprint")
	ErrLastRecipient        = errors.New("refusing to revoke the last recipient")
)

func Init(a Agent, path string) ([]Recipient, error) {
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrRecipientsFileExists, path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("checking recipients file %s: %w", path, err)
	}

	keys, err := ed25519Keys(a)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, ErrNoEd25519Key
	}

	dataKey := make([]byte, keySize)
	if _, err = rand.Read(dataKey); err != nil {
		return nil, fmt.Errorf("generating data key: %w", err)
	}

	file := &File{Version: FormatVersion}
	added, err := file.addRecipients(a, keys, dataKey)
	if err != nil {
		return nil, err
	}
	if err := file.Save(path); err != nil {
		return nil, err
	}
	return added, nil
}

func Enroll(a Agent, path string) ([]Recipient, error) {
	file, err := Load(path)
	if err != nil {
		return nil, err
	}
	dataKey, err := UnwrapDataKey(a, file)
	if err != nil {
		return nil, err
	}
	keys, err := ed25519Keys(a)
	if err != nil {
		return nil, err
	}

	added, err := file.addRecipients(a, keys, dataKey)
	if err != nil {
		return nil, err
	}
	if len(added) == 0 {
		return added, nil
	}
	if err := file.Save(path); err != nil {
		return nil, err
	}
	return added, nil
}

func Revoke(path, fingerprint string) (Recipient, error) {
	file, err := Load(path)
	if err != nil {
		return Recipient{}, err
	}
	target := file.recipient(fingerprint)
	if target == nil {
		return Recipient{}, fmt.Errorf("%w: %s", ErrUnknownRecipient, fingerprint)
	}
	if len(file.Recipients) == 1 {
		return Recipient{}, fmt.Errorf("%w: %s", ErrLastRecipient, fingerprint)
	}

	revoked := *target
	file.Recipients = slices.DeleteFunc(file.Recipients, func(r Recipient) bool {
		return r.Fingerprint == fingerprint
	})
	if err := file.Save(path); err != nil {
		return Recipient{}, err
	}
	return revoked, nil
}

// UnwrapDataKey uses the enrolled keys in the agent's order and returns on the first success.
func UnwrapDataKey(a Agent, file *File) ([]byte, error) {
	keys, err := ed25519Keys(a)
	if err != nil {
		return nil, err
	}

	var failures []error
	for _, key := range keys {
		r := file.recipient(ssh.FingerprintSHA256(key))
		if r == nil {
			continue
		}
		dataKey, err := unwrap(a, key, r)
		if err == nil {
			return dataKey, nil
		}
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		return nil, ErrNoEnrolledKey
	}
	return nil, errors.Join(failures...)
}

func AgentFingerprints(a Agent) (map[string]struct{}, error) {
	keys, err := ed25519Keys(a)
	if err != nil {
		return nil, err
	}
	fingerprints := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		fingerprints[ssh.FingerprintSHA256(key)] = struct{}{}
	}
	return fingerprints, nil
}

func (f *File) addRecipients(a Agent, keys []*agent.Key, dataKey []byte) ([]Recipient, error) {
	added := make([]Recipient, 0, len(keys))
	for _, key := range keys {
		if f.recipient(ssh.FingerprintSHA256(key)) != nil {
			continue
		}
		r, err := wrap(a, key, dataKey)
		if err != nil {
			return nil, err
		}
		f.Recipients = append(f.Recipients, r)
		added = append(added, r)
	}
	return added, nil
}

func ed25519Keys(a Agent) ([]*agent.Key, error) {
	all, err := a.List()
	if err != nil {
		return nil, fmt.Errorf("listing ssh-agent keys: %w", err)
	}
	keys := make([]*agent.Key, 0, len(all))
	for _, key := range all {
		if key.Type() == ssh.KeyAlgoED25519 {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func wrap(a Agent, key *agent.Key, dataKey []byte) (Recipient, error) {
	fingerprint := ssh.FingerprintSHA256(key)
	aead, err := recipientAEAD(a, key)
	if err != nil {
		return Recipient{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Recipient{}, fmt.Errorf("generating nonce for %s: %w", fingerprint, err)
	}
	return Recipient{
		Fingerprint: fingerprint,
		PublicKey:   authorizedKey(key),
		Comment:     key.Comment,
		Nonce:       nonce,
		WrappedKey:  aead.Seal(nil, nonce, dataKey, []byte(wrapAADPrefix+fingerprint)),
	}, nil
}

func unwrap(a Agent, key *agent.Key, r *Recipient) ([]byte, error) {
	aead, err := recipientAEAD(a, key)
	if err != nil {
		return nil, err
	}
	dataKey, err := aead.Open(nil, r.Nonce, r.WrappedKey, []byte(wrapAADPrefix+r.Fingerprint))
	if err != nil {
		return nil, fmt.Errorf("unwrapping data key for %s: %w", r.Fingerprint, err)
	}
	return dataKey, nil
}

func recipientAEAD(a Agent, key *agent.Key) (cipher.AEAD, error) {
	kek, err := DeriveKEK(a, key)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating AES-GCM: %w", err)
	}
	return aead, nil
}
