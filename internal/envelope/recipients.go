package envelope

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	FormatVersion = 1

	fingerprintPrefix = "SHA256:"

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
	digest, ok := strings.CutPrefix(r.Fingerprint, fingerprintPrefix)
	if !ok {
		return fmt.Errorf("%w %q: fingerprint must start with %s", ErrInvalidRecipient, r.Fingerprint, fingerprintPrefix)
	}
	if raw, err := base64.RawStdEncoding.Strict().DecodeString(digest); err != nil || len(raw) != sha256.Size {
		return fmt.Errorf("%w %q: fingerprint must be an unpadded base64 SHA-256 digest", ErrInvalidRecipient, r.Fingerprint)
	}
	if len(r.Nonce) != gcmNonceSize {
		return fmt.Errorf("%w %s: nonce must be %d bytes", ErrInvalidRecipient, r.Fingerprint, gcmNonceSize)
	}
	if len(r.WrappedKey) != wrappedKeySize {
		return fmt.Errorf("%w %s: wrapped_key must be %d bytes", ErrInvalidRecipient, r.Fingerprint, wrappedKeySize)
	}
	return nil
}

func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}
