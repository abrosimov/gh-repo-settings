package envelope

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	FormatVersion = 1

	gcmNonceSize    = 12
	gcmTagSize      = 16
	wrappedKeySize  = keySize + gcmTagSize
	jsonIndentation = "  "
)

var (
	ErrUnsupportedFormat  = errors.New("unsupported recipients file format version")
	ErrNoRecipients       = errors.New("recipients file lists no recipients")
	ErrDuplicateRecipient = errors.New("duplicate recipient")
	ErrInvalidRecipient   = errors.New("invalid recipient")
)

type Recipient struct {
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
	Comment     string `json:"comment"`
	Nonce       []byte `json:"nonce"`
	WrappedKey  []byte `json:"wrapped_key"`
}

type File struct {
	Recipients []Recipient `json:"recipients"`
	Version    int         `json:"version"`
}

func Load(path string) (*File, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("reading recipients file: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file File
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parsing recipients file %s: %w", path, err)
	}
	if err := file.validate(); err != nil {
		return nil, fmt.Errorf("recipients file %s: %w", path, err)
	}
	return &file, nil
}

func (f *File) Save(path string) (err error) {
	slices.SortFunc(f.Recipients, func(a, b Recipient) int {
		return strings.Compare(a.Fingerprint, b.Fingerprint)
	})
	data, err := json.MarshalIndent(f, "", jsonIndentation)
	if err != nil {
		return fmt.Errorf("encoding recipients file: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary recipients file: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, removeIfPresent(tmp.Name()))
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return errors.Join(fmt.Errorf("writing %s: %w", tmp.Name(), err), tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(fmt.Errorf("syncing %s: %w", tmp.Name(), err), tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replacing recipients file %s: %w", path, err)
	}
	return nil
}

func (f *File) recipient(fingerprint string) *Recipient {
	for i := range f.Recipients {
		if f.Recipients[i].Fingerprint == fingerprint {
			return &f.Recipients[i]
		}
	}
	return nil
}

func (f *File) validate() error {
	if f.Version != FormatVersion {
		return fmt.Errorf("%w: %d (expected %d)", ErrUnsupportedFormat, f.Version, FormatVersion)
	}
	if len(f.Recipients) == 0 {
		return ErrNoRecipients
	}
	seen := make(map[string]struct{}, len(f.Recipients))
	for i := range f.Recipients {
		r := &f.Recipients[i]
		if err := r.validate(); err != nil {
			return err
		}
		if _, ok := seen[r.Fingerprint]; ok {
			return fmt.Errorf("%w: %s", ErrDuplicateRecipient, r.Fingerprint)
		}
		seen[r.Fingerprint] = struct{}{}
	}
	return nil
}

func (r *Recipient) validate() error {
	fields := strings.Fields(r.PublicKey)
	if len(fields) != 2 || fields[0] != ssh.KeyAlgoED25519 {
		return fmt.Errorf("%w %s: public_key must be an ssh-ed25519 key without a comment", ErrInvalidRecipient, r.Fingerprint)
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return fmt.Errorf("%w %s: decoding public_key: %w", ErrInvalidRecipient, r.Fingerprint, err)
	}
	key, err := ssh.ParsePublicKey(blob)
	if err != nil {
		return fmt.Errorf("%w %s: parsing public_key: %w", ErrInvalidRecipient, r.Fingerprint, err)
	}
	if key.Type() != ssh.KeyAlgoED25519 {
		return fmt.Errorf("%w %s: public_key is %s", ErrInvalidRecipient, r.Fingerprint, key.Type())
	}
	if actual := ssh.FingerprintSHA256(key); actual != r.Fingerprint {
		return fmt.Errorf("%w %s: public_key has fingerprint %s", ErrInvalidRecipient, r.Fingerprint, actual)
	}
	if len(r.Nonce) != gcmNonceSize {
		return fmt.Errorf("%w %s: nonce must be %d bytes", ErrInvalidRecipient, r.Fingerprint, gcmNonceSize)
	}
	if len(r.WrappedKey) != wrappedKeySize {
		return fmt.Errorf("%w %s: wrapped_key must be %d bytes", ErrInvalidRecipient, r.Fingerprint, wrappedKeySize)
	}
	return nil
}

func authorizedKey(key ssh.PublicKey) string {
	return strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(key)), "\n")
}

func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}
