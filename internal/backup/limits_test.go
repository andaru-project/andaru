package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"

	"filippo.io/age"
)

func TestUnknownUnixMemberTypeRejected(t *testing.T) {
	raw := fixtureZIP(t, []archiveEntry{{name: "manifest.json", body: fixtureManifest(t, []byte(configJSON))}, {name: "config.json", body: []byte(configJSON)}})
	directory := bytes.LastIndex(raw, []byte("PK\x01\x02"))
	if directory < 0 || directory+46 > len(raw) {
		t.Fatal("invalid independent fixture")
	}
	binary.LittleEndian.PutUint32(raw[directory+38:], uint32(0170000|0600)<<16)
	if _, err := Decode(context.Background(), raw, ""); err == nil {
		t.Fatal("unrecognized Unix special-file type treated as regular")
	}
}

func TestArchiveSizeMustMatchActualRead(t *testing.T) {
	var manifest map[string]any
	if err := json.Unmarshal(fixtureManifest(t, []byte(configJSON)), &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["payloads"].(map[string]any)["config.json"].(map[string]any)["size"] = len(configJSON) + 1
	metadata, _ := json.Marshal(manifest)
	raw := fixtureZIP(t, []archiveEntry{{name: "manifest.json", body: metadata}, {name: "config.json", body: []byte(configJSON)}})
	directory := bytes.LastIndex(raw, []byte("PK\x01\x02"))
	if directory < 0 || directory+46 > len(raw) {
		t.Fatal("invalid independent fixture")
	}
	binary.LittleEndian.PutUint32(raw[directory+24:], uint32(len(configJSON)+1))
	if decoded, err := Decode(context.Background(), raw, ""); err == nil || decoded.Documents.Configuration != nil {
		t.Fatal("matching false manifest/header sizes bypassed actual-read validation")
	}
}

func TestDecryptedZIPSizeBound(t *testing.T) {
	recipient, err := age.NewScryptRecipient("TEST_ONLY_SIZE_PASSPHRASE")
	if err != nil {
		t.Fatal(err)
	}
	recipient.SetWorkFactor(1) // Independent input fixture; production writers must still use 18.
	var buffer bytes.Buffer
	writer, err := age.Encrypt(&buffer, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write(bytes.Repeat([]byte("x"), 12*1024*1024+1)); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(context.Background(), buffer.Bytes(), "TEST_ONLY_SIZE_PASSPHRASE")
	var archiveError *Error
	if !errors.As(err, &archiveError) || archiveError.Code != "ARCHIVE_LIMIT" || decoded.Documents.Configuration != nil {
		t.Fatal("decrypted bytes were not bounded before interpretation", err)
	}
}

func TestProductionEncryptionWorkFactor(t *testing.T) {
	raw, err := Encode(context.Background(), sourceFixture(), Documents{Configuration: json.RawMessage(configJSON), ConfigurationSecrets: json.RawMessage(secretsJSON)}, "TEST_ONLY_STRENGTH")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := age.NewScryptIdentity("TEST_ONLY_STRENGTH")
	if err != nil {
		t.Fatal(err)
	}
	identity.SetMaxWorkFactor(17)
	if _, err = age.Decrypt(bytes.NewReader(raw), identity); err == nil {
		t.Fatal("writer silently weakened passphrase encryption")
	}
	if _, err = Decode(context.Background(), raw, "TEST_ONLY_STRENGTH"); err != nil {
		t.Fatal("writer exceeds the supported read ceiling", err)
	}
}

func TestLocalZIPHeaderMustAgreeWithDirectory(t *testing.T) {
	base := func() []byte {
		return fixtureZIP(t, []archiveEntry{
			{name: "manifest.json", body: fixtureManifest(t, []byte(configJSON))},
			{name: "config.json", body: []byte(configJSON)},
		})
	}
	tests := map[string]func([]byte, int, int){
		"name": func(raw []byte, _, local int) {
			copy(raw[local+30:local+41], "../bad.jsonx")
		},
		"method": func(raw []byte, _, local int) {
			binary.LittleEndian.PutUint16(raw[local+8:], zip.Deflate)
		},
		"encryption flag": func(raw []byte, _, local int) {
			binary.LittleEndian.PutUint16(raw[local+6:], binary.LittleEndian.Uint16(raw[local+6:])^1)
		},
		"malformed": func(raw []byte, _, local int) {
			binary.LittleEndian.PutUint32(raw[local:], 0)
		},
		"truncated": func(raw []byte, central, _ int) {
			end := bytes.LastIndex(raw, []byte("PK\x05\x06"))
			directory := int(binary.LittleEndian.Uint32(raw[end+16:]))
			local := directory - 10
			binary.LittleEndian.PutUint32(raw[local:], 0x04034b50)
			binary.LittleEndian.PutUint32(raw[central+42:], uint32(local))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			raw := base()
			central := bytes.LastIndex(raw, []byte("PK\x01\x02"))
			if central < 0 || central+46 > len(raw) {
				t.Fatal("invalid independent fixture")
			}
			local := int(binary.LittleEndian.Uint32(raw[central+42:]))
			if local < 0 || local+41 > central {
				t.Fatal("invalid independent fixture local header")
			}
			mutate(raw, central, local)
			if _, err := Decode(context.Background(), raw, ""); err == nil {
				t.Fatal("conflicting or malformed local ZIP header accepted")
			}
		})
	}
}
