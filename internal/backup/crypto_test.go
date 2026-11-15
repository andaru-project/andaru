package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSecretArchiveRequiresEncryption(t *testing.T) {
	docs := Documents{Configuration: json.RawMessage(configJSON), ConfigurationSecrets: json.RawMessage(secretsJSON)}
	for _, password := range []string{"", "bad\x00password", string([]byte{0xff}), strings.Repeat("x", 1025)} {
		if raw, err := Encode(context.Background(), sourceFixture(), docs, password); err == nil || raw != nil {
			t.Fatal("invalid passphrase produced a secret-bearing artifact")
		}
	}
	var manifest map[string]any
	if err := json.Unmarshal(fixtureManifest(t, []byte(configJSON)), &manifest); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(secretsJSON))
	manifest["scopes"] = []string{"configuration", "configurationSecrets"}
	manifest["payloads"].(map[string]any)["configuration-secrets.json"] = map[string]any{"schemaVersion": 1, "size": len(secretsJSON), "sha256": hex.EncodeToString(digest[:])}
	rawManifest, _ := json.Marshal(manifest)
	plain := fixtureZIP(t, []archiveEntry{{name: "manifest.json", body: rawManifest}, {name: "config.json", body: []byte(configJSON)}, {name: "configuration-secrets.json", body: []byte(secretsJSON)}})
	if decoded, err := Decode(context.Background(), plain, ""); err == nil || decoded.Documents.ConfigurationSecrets != nil {
		t.Fatal("unencrypted secret scope was accepted")
	}
}

func TestEncryptedArchiveAuthenticatesToEOF(t *testing.T) {
	zones := make([]map[string]any, 100)
	for i := range zones {
		zones[i] = map[string]any{"id": fmt.Sprintf("zon_00000000-0000-4000-8000-%012x", i+1), "name": fmt.Sprintf("zone-%d", i), "description": strings.Repeat("x", 1000)}
	}
	config, _ := json.Marshal(map[string]any{"schemaVersion": 1, "links": []any{}, "segments": []any{}, "zones": zones})
	docs := Documents{Configuration: config, ConfigurationSecrets: json.RawMessage(secretsJSON)}
	raw, err := Encode(context.Background(), sourceFixture(), docs, "TEST_ONLY_PASSPHRASE")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 64*1024 || bytes.Contains(raw, []byte("TEST_ONLY_SECRET_VALUE")) {
		t.Fatal("fixture did not exercise multiple authenticated chunks or leaked plaintext")
	}
	decoded, err := Decode(context.Background(), raw, "TEST_ONLY_PASSPHRASE")
	if err != nil || !bytes.Equal(decoded.Documents.Configuration, config) || string(decoded.Documents.ConfigurationSecrets) != secretsJSON {
		t.Fatal("encrypted archive did not round-trip", err)
	}
	tampered := bytes.Clone(raw)
	tampered[len(tampered)-1] ^= 1
	for _, input := range [][]byte{raw[:len(raw)-1], tampered} {
		decoded, err := Decode(context.Background(), input, "TEST_ONLY_PASSPHRASE")
		if err == nil || decoded.Documents.Configuration != nil || decoded.Documents.ConfigurationSecrets != nil {
			t.Fatal("partial authentication returned archive contents")
		}
	}
	if decoded, err := Decode(context.Background(), raw, "WRONG_TEST_ONLY_PASSWORD"); err == nil || decoded.Documents.Configuration != nil {
		t.Fatal("wrong password returned archive contents")
	}
}

func TestAgeWorkFactorHelper(t *testing.T) {
	if os.Getenv("ANDARU_KDF_LIMIT_PROBE") == "" {
		t.Skip("subprocess resource probe only")
	}
	raw := []byte("age-encryption.org/v1\n-> scrypt AAAAAAAAAAAAAAAAAAAAAA 19\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n--- AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n")
	raw = append(raw, make([]byte, 64)...)
	_, err := Decode(context.Background(), raw, "TEST_ONLY_PASSPHRASE")
	var archiveError *Error
	if !errors.As(err, &archiveError) || archiveError.Code != "UNSUPPORTED_ENCRYPTION" {
		t.Fatal("excessive work factor reached password authentication", err)
	}
}

func TestAgeWorkFactorCeiling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgeWorkFactorHelper$", "-test.count=1")
	child.Env = append(os.Environ(), "ANDARU_KDF_LIMIT_PROBE=1", "GOMEMLIMIT=128MiB")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("KDF limit probe failed: %v %s", err, output)
	}
}
