package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"testing"

	"filippo.io/age"
)

func TestZIPPreflightRejectsEmbeddedEndRecordWithTrailingData(t *testing.T) {
	for name, trailingSize := range map[string]int{"last kilobyte": 1, "extended search": 1024} {
		t.Run(name, func(t *testing.T) {
			raw := fixtureZIP(t, []archiveEntry{
				{name: "manifest.json", body: fixtureManifest(t, []byte(configJSON))},
				{name: "config.json", body: []byte(configJSON)},
			})
			end := len(raw) - 22
			central := bytes.LastIndex(raw[:end], []byte("PK\x01\x02"))
			if central < 0 || central+46 > end {
				t.Fatal("invalid independent fixture")
			}

			// The outer comment hides a larger directory and a later EOCD that does not reach EOF.
			directory := bytes.Repeat(raw[central:end], MaxMembers+1)
			nestedEnd := make([]byte, 22)
			binary.LittleEndian.PutUint32(nestedEnd, 0x06054b50)
			binary.LittleEndian.PutUint16(nestedEnd[8:], MaxMembers+1)
			binary.LittleEndian.PutUint16(nestedEnd[10:], MaxMembers+1)
			binary.LittleEndian.PutUint32(nestedEnd[12:], uint32(len(directory)))
			binary.LittleEndian.PutUint32(nestedEnd[16:], uint32(len(raw)))
			comment := append(directory, nestedEnd...)
			comment = append(comment, make([]byte, trailingSize)...)
			binary.LittleEndian.PutUint16(raw[end+20:], uint16(len(comment)))
			raw = append(raw, comment...)

			reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				t.Fatal("stdlib rejected the selection-differential fixture", err)
			}
			if len(reader.File) != MaxMembers+1 {
				t.Fatalf("stdlib selected %d members, want %d", len(reader.File), MaxMembers+1)
			}
			var invalid *Error
			if _, err := preflightZIP(raw); !errors.As(err, &invalid) || invalid.Code != "INVALID_ARCHIVE" {
				t.Fatalf("ambiguous EOCD reached ZIP reader allocation: %v", err)
			}
		})
	}
}

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

func fixtureZIPWithoutDescriptors(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		size := uint64(len(entry.body))
		header := &zip.FileHeader{
			Name:               entry.name,
			Method:             zip.Store,
			CRC32:              crc32.ChecksumIEEE(entry.body),
			CompressedSize64:   size,
			UncompressedSize64: size,
		}
		header.SetMode(0600)
		file, err := writer.CreateRaw(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestLocalZIPHeaderIntegrityMustAgreeWithoutDescriptor(t *testing.T) {
	base := func() []byte {
		return fixtureZIPWithoutDescriptors(t, []archiveEntry{
			{name: "manifest.json", body: fixtureManifest(t, []byte(configJSON))},
			{name: "config.json", body: []byte(configJSON)},
		})
	}
	raw := base()
	central := bytes.LastIndex(raw, []byte("PK\x01\x02"))
	if central < 0 || central+46 > len(raw) {
		t.Fatal("invalid independent fixture")
	}
	local := int(binary.LittleEndian.Uint32(raw[central+42:]))
	if local < 0 || local+30 > central || binary.LittleEndian.Uint16(raw[central+8:])&8 != 0 || binary.LittleEndian.Uint16(raw[local+6:])&8 != 0 {
		t.Fatal("fixture unexpectedly uses a data descriptor")
	}
	decoded, err := Decode(context.Background(), raw, "")
	if err != nil || string(decoded.Documents.Configuration) != configJSON {
		t.Fatal("valid archive without data descriptors rejected", err)
	}

	tests := map[string]int{
		"CRC32":             14,
		"compressed size":   18,
		"uncompressed size": 22,
	}
	for name, field := range tests {
		t.Run(name, func(t *testing.T) {
			raw := base()
			central := bytes.LastIndex(raw, []byte("PK\x01\x02"))
			local := int(binary.LittleEndian.Uint32(raw[central+42:]))
			binary.LittleEndian.PutUint32(raw[local+field:], binary.LittleEndian.Uint32(raw[local+field:])+1)
			if _, err := Decode(context.Background(), raw, ""); err == nil {
				t.Fatal("conflicting local ZIP integrity field accepted")
			}
		})
	}
}
