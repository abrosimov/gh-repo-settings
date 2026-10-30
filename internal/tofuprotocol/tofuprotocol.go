// Package tofuprotocol implements version 1 of the OpenTofu external key provider protocol, as defined in
// internal/encryption/keyprovider/external of the OpenTofu source (v1.13).
package tofuprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	headerMagic     = "OpenTofu-External-Key-Provider"
	protocolVersion = 1
	maxInputBytes   = 1 << 20
)

var (
	ErrInputTooLarge       = errors.New("key provider input exceeds the size limit")
	ErrMalformedInput      = errors.New("malformed key provider input")
	ErrUnsupportedMetadata = errors.New("key provider metadata was not written by this program")
)

var currentScheme = externalData{Scheme: "tofu-sshagent-key/envelope", Version: 1}

type header struct {
	Magic   string `json:"magic"`
	Version int    `json:"version"`
}

type externalData struct {
	Scheme  string `json:"scheme"`
	Version int    `json:"version"`
}

type metadata struct {
	ExternalData *externalData `json:"external_data"`
}

type keys struct {
	EncryptionKey []byte `json:"encryption_key"`
	DecryptionKey []byte `json:"decryption_key,omitempty"`
}

type output struct {
	Meta metadata `json:"meta"`
	Keys keys     `json:"keys"`
}

func Serve(stdin io.Reader, stdout io.Writer, key []byte) error {
	encoder := json.NewEncoder(stdout)
	if err := encoder.Encode(header{Magic: headerMagic, Version: protocolVersion}); err != nil {
		return fmt.Errorf("writing protocol header: %w", err)
	}

	decryptionRequested, err := readInput(stdin)
	if err != nil {
		return err
	}

	scheme := currentScheme
	out := output{
		Keys: keys{EncryptionKey: key},
		Meta: metadata{ExternalData: &scheme},
	}
	if decryptionRequested {
		out.Keys.DecryptionKey = key
	}
	if err := encoder.Encode(out); err != nil {
		return fmt.Errorf("writing protocol output: %w", err)
	}
	return nil
}

func readInput(stdin io.Reader) (bool, error) {
	data, err := io.ReadAll(io.LimitReader(stdin, maxInputBytes+1))
	if err != nil {
		return false, fmt.Errorf("reading key provider input: %w", err)
	}
	if len(data) > maxInputBytes {
		return false, fmt.Errorf("%w of %d bytes", ErrInputTooLarge, maxInputBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var in *metadata
	if err := decoder.Decode(&in); err != nil {
		return false, fmt.Errorf("%w: %w", ErrMalformedInput, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("%w: trailing data after the JSON value", ErrMalformedInput)
	}

	if in == nil {
		return false, nil
	}
	if in.ExternalData == nil || *in.ExternalData != currentScheme {
		return false, ErrUnsupportedMetadata
	}
	return true, nil
}
