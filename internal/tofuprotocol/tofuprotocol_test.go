//go:build unit

package tofuprotocol_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/abrosimov/gh-repo-settings/internal/tofuprotocol"
)

// Mirrors OutputV1 and MetadataV1 in opentofu/internal/encryption/keyprovider/external (v1.13.5),
// which OpenTofu decodes with DisallowUnknownFields.
type tofuOutput struct {
	Keys struct {
		EncryptionKey []byte `json:"encryption_key,omitempty"`
		DecryptionKey []byte `json:"decryption_key,omitempty"`
	} `json:"keys"`
	Meta struct {
		ExternalData map[string]any `json:"external_data"`
	} `json:"meta"`
}

type tofuHeader struct {
	Magic   string `json:"magic"`
	Version int    `json:"version"`
}

var testKey = bytes.Repeat([]byte{0x42}, 32)

func serve(t *testing.T, input string) (tofuHeader, tofuOutput, string) {
	t.Helper()
	var stdout bytes.Buffer
	if err := tofuprotocol.Serve(strings.NewReader(input), &stdout, testKey); err != nil {
		t.Fatalf("Serve(%s): %v", input, err)
	}

	headerLine, body, found := strings.Cut(stdout.String(), "\n")
	if !found {
		t.Fatalf("stdout has no header line: %q", stdout.String())
	}
	var header tofuHeader
	if err := json.Unmarshal([]byte(headerLine), &header); err != nil {
		t.Fatalf("parsing header %q: %v", headerLine, err)
	}

	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	var out tofuOutput
	if err := decoder.Decode(&out); err != nil {
		t.Fatalf("parsing output %q as OpenTofu would: %v", body, err)
	}
	return header, out, body
}

func TestServeWritesHeaderLine(t *testing.T) {
	header, _, _ := serve(t, "null")

	if header.Magic != "OpenTofu-External-Key-Provider" || header.Version != 1 {
		t.Errorf("header = %+v, want magic OpenTofu-External-Key-Provider and version 1", header)
	}
}

func TestServeWithNullInputReturnsEncryptionKeyOnly(t *testing.T) {
	_, out, body := serve(t, "null\n")

	if !bytes.Equal(out.Keys.EncryptionKey, testKey) {
		t.Errorf("encryption_key = %x, want %x", out.Keys.EncryptionKey, testKey)
	}
	if out.Keys.DecryptionKey != nil || strings.Contains(body, "decryption_key") {
		t.Errorf("output %s carries a decryption_key without input metadata", body)
	}
	if len(out.Meta.ExternalData) == 0 {
		t.Errorf("meta.external_data is empty; OpenTofu would send null instead of it on decryption")
	}
}

func TestServeWithStoredMetadataReturnsBothKeys(t *testing.T) {
	_, first, _ := serve(t, "null")
	stored, err := json.Marshal(map[string]any{"external_data": first.Meta.ExternalData})
	if err != nil {
		t.Fatalf("encoding stored metadata: %v", err)
	}

	_, out, _ := serve(t, string(stored))

	if !bytes.Equal(out.Keys.EncryptionKey, testKey) || !bytes.Equal(out.Keys.DecryptionKey, testKey) {
		t.Errorf("keys = (%x, %x), want both %x", out.Keys.EncryptionKey, out.Keys.DecryptionKey, testKey)
	}
}

func TestServeRejectsInvalidInput(t *testing.T) {
	cases := map[string]struct {
		input string
		want  error
	}{
		"foreign scheme":        {`{"external_data":{"scheme":"pbkdf2","version":1}}`, tofuprotocol.ErrUnsupportedMetadata},
		"future version":        {`{"external_data":{"scheme":"tofu-sshagent-key/envelope","version":2}}`, tofuprotocol.ErrUnsupportedMetadata},
		"empty external data":   {`{"external_data":{}}`, tofuprotocol.ErrUnsupportedMetadata},
		"missing external data": {`{}`, tofuprotocol.ErrUnsupportedMetadata},
		"unknown field":         {`{"other":1}`, tofuprotocol.ErrMalformedInput},
		"not JSON":              {`hello`, tofuprotocol.ErrMalformedInput},
		"empty input":           {``, tofuprotocol.ErrMalformedInput},
		"trailing data":         {`null null`, tofuprotocol.ErrMalformedInput},
		"oversized input":       {strings.Repeat(" ", 1<<20+1), tofuprotocol.ErrInputTooLarge},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var stdout bytes.Buffer

			err := tofuprotocol.Serve(strings.NewReader(tc.input), &stdout, testKey)

			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if strings.Contains(stdout.String(), "encryption_key") {
				t.Errorf("stdout carries key material after a rejected input: %q", stdout.String())
			}
		})
	}
}
