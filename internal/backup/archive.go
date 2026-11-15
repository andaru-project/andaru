// Package backup reads and writes bounded logical backups; it never extracts files.
package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/andaru-project/andaru/internal/jsonprofile"
)

const (
	MaxArchiveSize    = 16 << 20
	MaxZIPSize        = 12 << 20
	MaxManifestSize   = 64 << 10
	MaxDocumentSize   = 4 << 20
	MaxExpandedSize   = 12 << 20
	MaxMembers        = 3
	MaxExpansionRatio = 100
)

type Error struct{ Code string }

func (e *Error) Error() string { return e.Code }

type Documents struct {
	Configuration, ConfigurationSecrets json.RawMessage
}

type NetworkObservation struct {
	Live             *int   `json:"live"`
	Pending          *int   `json:"pending"`
	LiveSavedVersion string `json:"liveSavedVersion"`
}

type Source struct {
	AndaruVersion, SavedVersion string
	CapturedAt                  time.Time
	Network                     NetworkObservation
}

type Payload struct {
	SchemaVersion int    `json:"schemaVersion"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
}

type Manifest struct {
	FormatVersion          int                `json:"formatVersion"`
	BackupID               string             `json:"backupId"`
	CreatedAt              time.Time          `json:"createdAt"`
	AndaruVersion          string             `json:"andaruVersion"`
	SavedVersion           string             `json:"savedVersion"`
	Scopes                 []string           `json:"scopes"`
	Payloads               map[string]Payload `json:"payloads"`
	Network                NetworkObservation `json:"network"`
	SavedButUnappliedAreas []string           `json:"savedButUnappliedAreas"`
}

type Archive struct {
	Manifest  Manifest
	Documents Documents
}

func validVersion(version string) bool {
	n, err := strconv.ParseInt(version, 10, 64)
	return err == nil && n >= 0 && strconv.FormatInt(n, 10) == version
}

func validSource(source Source) bool {
	if source.CapturedAt.IsZero() || len(source.AndaruVersion) == 0 || len(source.AndaruVersion) > 128 || !validVersion(source.SavedVersion) {
		return false
	}
	for _, c := range source.AndaruVersion {
		if c < 33 || c > 126 {
			return false
		}
	}
	if source.Network.Pending != nil && *source.Network.Pending <= 0 {
		return false
	}
	if source.Network.Live == nil {
		return source.Network.LiveSavedVersion == ""
	}
	return *source.Network.Live > 0 && validVersion(source.Network.LiveSavedVersion)
}

func unapplied(source Source) []string {
	if source.Network.Live == nil || source.Network.LiveSavedVersion != source.SavedVersion {
		return []string{"network"}
	}
	return []string{}
}

func documentSchema(raw []byte, secret bool) (int, error) {
	if len(raw) > MaxDocumentSize {
		return 0, &Error{Code: "ARCHIVE_LIMIT"}
	}
	if !jsonprofile.Valid(raw, false) {
		return 0, &Error{Code: "INVALID_ARCHIVE"}
	}
	var version struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if json.Unmarshal(raw, &version) != nil {
		return 0, &Error{Code: "INVALID_ARCHIVE"}
	}
	if version.SchemaVersion != 1 && (secret || version.SchemaVersion != 2) {
		return 0, &Error{Code: "UNSUPPORTED_SCHEMA"}
	}
	return version.SchemaVersion, nil
}

func Encode(ctx context.Context, source Source, docs Documents, passphrase string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validSource(source) || docs.Configuration == nil {
		return nil, &Error{Code: "INVALID_ARCHIVE"}
	}
	if docs.ConfigurationSecrets != nil {
		if err := ValidatePassphrase(passphrase); err != nil {
			return nil, err
		}
	} else if passphrase != "" {
		return nil, &Error{Code: "PASSPHRASE_INVALID"}
	}
	manifest := Manifest{FormatVersion: 1, CreatedAt: source.CapturedAt.UTC(), AndaruVersion: source.AndaruVersion, SavedVersion: source.SavedVersion, Scopes: []string{}, Payloads: map[string]Payload{}, Network: source.Network, SavedButUnappliedAreas: unapplied(source)}
	var identity [16]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return nil, &Error{Code: "ENCRYPTION_FAILED"}
	}
	manifest.BackupID = hex.EncodeToString(identity[:])
	names := []string{"config.json"}
	if docs.ConfigurationSecrets != nil {
		names = append(names, "configuration-secrets.json")
	}
	for _, name := range names {
		data, scope, secret := docs.Configuration, "configuration", false
		if name == "configuration-secrets.json" {
			data, scope, secret = docs.ConfigurationSecrets, "configurationSecrets", true
		}
		schema, err := documentSchema(data, secret)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(data)
		manifest.Payloads[name] = Payload{SchemaVersion: schema, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
		manifest.Scopes = append(manifest.Scopes, scope)
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		return nil, &Error{Code: "INVALID_ARCHIVE"}
	}
	if len(rawManifest) > MaxManifestSize {
		return nil, &Error{Code: "ARCHIVE_LIMIT"}
	}
	var buffer bytes.Buffer
	buffer.Grow(len(rawManifest) + len(docs.Configuration) + len(docs.ConfigurationSecrets) + 1024)
	writer := zip.NewWriter(&buffer)
	for _, name := range append([]string{"manifest.json"}, names...) {
		data := rawManifest
		if name == "config.json" {
			data = docs.Configuration
		}
		if name == "configuration-secrets.json" {
			data = docs.ConfigurationSecrets
		}
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(0600)
		file, err := writer.CreateHeader(header)
		if err != nil {
			return nil, &Error{Code: "INVALID_ARCHIVE"}
		}
		if _, err = file.Write(data); err != nil {
			return nil, &Error{Code: "INVALID_ARCHIVE"}
		}
	}
	if err = writer.Close(); err != nil {
		return nil, &Error{Code: "INVALID_ARCHIVE"}
	}
	if buffer.Len() > MaxZIPSize {
		return nil, &Error{Code: "ARCHIVE_LIMIT"}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if docs.ConfigurationSecrets == nil {
		return buffer.Bytes(), nil
	}
	return encrypt(ctx, buffer.Bytes(), passphrase)
}

func exactObject(raw []byte, names ...string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(names) {
		return nil, false
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return nil, false
		}
	}
	return fields, true
}

func decodeManifest(raw []byte) (Manifest, error) {
	var manifest Manifest
	invalid := &Error{Code: "INVALID_ARCHIVE"}
	if !jsonprofile.Valid(raw, true) {
		return manifest, invalid
	}
	fields, ok := exactObject(raw, "formatVersion", "backupId", "createdAt", "andaruVersion", "savedVersion", "scopes", "payloads", "network", "savedButUnappliedAreas")
	if !ok {
		return manifest, invalid
	}
	network, ok := exactObject(fields["network"], "live", "pending", "liveSavedVersion")
	if !ok || bytes.Equal(bytes.TrimSpace(network["liveSavedVersion"]), []byte("null")) {
		return manifest, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil {
		return Manifest{}, invalid
	}
	if manifest.FormatVersion != 1 {
		return Manifest{}, &Error{Code: "UNSUPPORTED_SCHEMA"}
	}
	id, err := hex.DecodeString(manifest.BackupID)
	source := Source{AndaruVersion: manifest.AndaruVersion, SavedVersion: manifest.SavedVersion, CapturedAt: manifest.CreatedAt, Network: manifest.Network}
	_, offset := manifest.CreatedAt.Zone()
	if err != nil || len(id) != 16 || !validSource(source) || offset != 0 || manifest.Scopes == nil || manifest.SavedButUnappliedAreas == nil || !slices.Equal(manifest.SavedButUnappliedAreas, unapplied(source)) {
		return Manifest{}, invalid
	}
	if len(manifest.Scopes) < 1 || len(manifest.Scopes) > 2 || len(manifest.Payloads) != len(manifest.Scopes) {
		return Manifest{}, invalid
	}
	var rawPayloads map[string]json.RawMessage
	if json.Unmarshal(fields["payloads"], &rawPayloads) != nil {
		return Manifest{}, invalid
	}
	seen := map[string]bool{}
	for _, scope := range manifest.Scopes {
		name := ""
		switch scope {
		case "configuration":
			name = "config.json"
		case "configurationSecrets":
			name = "configuration-secrets.json"
		default:
			return Manifest{}, &Error{Code: "UNSUPPORTED_SCHEMA"}
		}
		if seen[scope] {
			return Manifest{}, invalid
		}
		seen[scope] = true
		payload, exists := manifest.Payloads[name]
		if !exists {
			return Manifest{}, invalid
		}
		if _, ok := exactObject(rawPayloads[name], "schemaVersion", "size", "sha256"); !ok {
			return Manifest{}, invalid
		}
		digest, err := hex.DecodeString(payload.SHA256)
		if err != nil || len(digest) != sha256.Size || payload.Size <= 0 {
			return Manifest{}, invalid
		}
		if payload.Size > MaxDocumentSize {
			return Manifest{}, &Error{Code: "ARCHIVE_LIMIT"}
		}
		if payload.SchemaVersion != 1 && (scope == "configurationSecrets" || payload.SchemaVersion != 2) {
			return Manifest{}, &Error{Code: "UNSUPPORTED_SCHEMA"}
		}
	}
	return manifest, nil
}

type zipLayout struct {
	localOffsets     [MaxMembers]uint32
	count, directory int
}

// Check directory cardinality before zip.NewReader can materialize untrusted entries.
func preflightZIP(raw []byte) (zipLayout, error) {
	var layout zipLayout
	invalid := &Error{Code: "INVALID_ARCHIVE"}
	if len(raw) > MaxZIPSize {
		return layout, &Error{Code: "ARCHIVE_LIMIT"}
	}
	if len(raw) < 22 || !bytes.HasPrefix(raw, []byte("PK\x03\x04")) {
		return layout, invalid
	}
	for end := len(raw) - 22; end >= max(0, len(raw)-22-65535); end-- {
		if binary.LittleEndian.Uint32(raw[end:]) != 0x06054b50 || end+22+int(binary.LittleEndian.Uint16(raw[end+20:])) != len(raw) {
			continue
		}
		if binary.LittleEndian.Uint16(raw[end+4:]) != 0 || binary.LittleEndian.Uint16(raw[end+6:]) != 0 {
			return layout, invalid
		}
		count := binary.LittleEndian.Uint16(raw[end+10:])
		if binary.LittleEndian.Uint16(raw[end+8:]) != count {
			return layout, invalid
		}
		if count == 0 {
			return layout, invalid
		}
		if count > MaxMembers {
			return layout, &Error{Code: "ARCHIVE_LIMIT"}
		}
		size, offset := binary.LittleEndian.Uint32(raw[end+12:]), binary.LittleEndian.Uint32(raw[end+16:])
		// shortcut: ZIP64 directories are unsupported within the 16 MiB/three-member format; extend only for a qualified producer.
		if size == ^uint32(0) || offset == ^uint32(0) {
			return layout, &Error{Code: "UNSUPPORTED_ARCHIVE"}
		}
		if uint64(size)+uint64(offset) != uint64(end) {
			return layout, invalid
		}
		layout.count, layout.directory = int(count), int(offset)
		at := layout.directory
		for i := range layout.count {
			if at+46 > end || binary.LittleEndian.Uint32(raw[at:]) != 0x02014b50 || binary.LittleEndian.Uint16(raw[at+34:]) != 0 {
				return layout, invalid
			}
			layout.localOffsets[i] = binary.LittleEndian.Uint32(raw[at+42:])
			at += 46 + int(binary.LittleEndian.Uint16(raw[at+28:])) + int(binary.LittleEndian.Uint16(raw[at+30:])) + int(binary.LittleEndian.Uint16(raw[at+32:]))
			if at > end {
				return layout, invalid
			}
		}
		if at != end {
			return layout, invalid
		}
		return layout, nil
	}
	return layout, invalid
}

func localHeaderAgrees(raw []byte, directory int, file *zip.File, offset uint32) bool {
	const fixedSize = 30
	start := uint64(offset)
	if start+fixedSize > uint64(directory) {
		return false
	}
	at := int(offset)
	if binary.LittleEndian.Uint32(raw[at:]) != 0x04034b50 {
		return false
	}
	nameSize := uint64(binary.LittleEndian.Uint16(raw[at+26:]))
	extraSize := uint64(binary.LittleEndian.Uint16(raw[at+28:]))
	body := start + fixedSize + nameSize + extraSize
	if body > uint64(directory) {
		return false
	}
	dataOffset, err := file.DataOffset()
	if err != nil || dataOffset < 0 || uint64(dataOffset) != body {
		return false
	}
	name := raw[at+fixedSize : at+fixedSize+int(nameSize)]
	if len(name) != len(file.Name) {
		return false
	}
	for i := range name {
		if name[i] != file.Name[i] {
			return false
		}
	}
	if binary.LittleEndian.Uint16(raw[at+8:]) != file.Method {
		return false
	}
	const relevantFlags = 1 | 2 | 4 | 8 | 0x40 | 0x800 | 0x2000
	if (binary.LittleEndian.Uint16(raw[at+6:])^file.Flags)&relevantFlags != 0 {
		return false
	}
	if file.Flags&8 != 0 {
		return true
	}
	const maxUint32 = uint64(^uint32(0))
	if file.CompressedSize64 >= maxUint32 || file.UncompressedSize64 >= maxUint32 {
		return false
	}
	return binary.LittleEndian.Uint32(raw[at+14:]) == file.CRC32 &&
		uint64(binary.LittleEndian.Uint32(raw[at+18:])) == file.CompressedSize64 &&
		uint64(binary.LittleEndian.Uint32(raw[at+22:])) == file.UncompressedSize64
}

func memberBytes(ctx context.Context, file *zip.File, limit int) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, &Error{Code: "INVALID_ARCHIVE"}
	}
	raw, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: reader}, int64(limit)+1))
	closeErr := reader.Close()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(raw) > limit {
		return nil, &Error{Code: "ARCHIVE_LIMIT"}
	}
	if err != nil || closeErr != nil || uint64(len(raw)) != file.UncompressedSize64 {
		return nil, &Error{Code: "INVALID_ARCHIVE"}
	}
	return raw, nil
}

func Decode(ctx context.Context, raw []byte, passphrase string) (Archive, error) {
	if err := ctx.Err(); err != nil {
		return Archive{}, err
	}
	if len(raw) > MaxArchiveSize {
		return Archive{}, &Error{Code: "ARCHIVE_LIMIT"}
	}
	encrypted := bytes.HasPrefix(raw, []byte("age-encryption.org/"))
	zipBytes := raw
	if encrypted {
		var err error
		zipBytes, err = decrypt(ctx, raw, passphrase)
		if err != nil {
			return Archive{}, err
		}
	} else if passphrase != "" {
		return Archive{}, &Error{Code: "PASSPHRASE_INVALID"}
	}
	layout, err := preflightZIP(zipBytes)
	if err != nil {
		return Archive{}, err
	}
	reader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return Archive{}, &Error{Code: "INVALID_ARCHIVE"}
	}
	if len(reader.File) != layout.count {
		return Archive{}, &Error{Code: "INVALID_ARCHIVE"}
	}
	files := make(map[string]*zip.File, len(reader.File))
	var expanded uint64
	for i, file := range reader.File {
		if !localHeaderAgrees(zipBytes, layout.directory, file, layout.localOffsets[i]) {
			return Archive{}, &Error{Code: "INVALID_ARCHIVE"}
		}
		if file.Name != "manifest.json" && file.Name != "config.json" && file.Name != "configuration-secrets.json" {
			return Archive{}, &Error{Code: "INVALID_ARCHIVE"}
		}
		if files[file.Name] != nil || !file.Mode().IsRegular() || file.Flags&1 != 0 {
			return Archive{}, &Error{Code: "INVALID_ARCHIVE"}
		}
		// FileMode does not represent every possible Unix type encoded in ZIP attributes.
		if file.CreatorVersion>>8 == 3 {
			kind := (file.ExternalAttrs >> 16) & 0170000
			if kind != 0 && kind != 0100000 {
				return Archive{}, &Error{Code: "INVALID_ARCHIVE"}
			}
		}
		if file.Method != zip.Store && file.Method != zip.Deflate {
			return Archive{}, &Error{Code: "UNSUPPORTED_ARCHIVE"}
		}
		limit := uint64(MaxDocumentSize)
		if file.Name == "manifest.json" {
			limit = MaxManifestSize
		}
		if file.UncompressedSize64 > limit || file.CompressedSize64 > uint64(len(zipBytes)) {
			return Archive{}, &Error{Code: "ARCHIVE_LIMIT"}
		}
		if file.UncompressedSize64 > MaxExpansionRatio*file.CompressedSize64 {
			return Archive{}, &Error{Code: "ARCHIVE_LIMIT"}
		}
		expanded += file.UncompressedSize64
		if expanded > MaxExpandedSize {
			return Archive{}, &Error{Code: "ARCHIVE_LIMIT"}
		}
		files[file.Name] = file
	}
	manifestFile := files["manifest.json"]
	if manifestFile == nil {
		return Archive{}, &Error{Code: "INVALID_ARCHIVE"}
	}
	rawManifest, err := memberBytes(ctx, manifestFile, MaxManifestSize)
	if err != nil {
		return Archive{}, err
	}
	manifest, err := decodeManifest(rawManifest)
	if err != nil {
		return Archive{}, err
	}
	if len(files) != len(manifest.Payloads)+1 {
		return Archive{}, &Error{Code: "INVALID_ARCHIVE"}
	}
	if _, secret := manifest.Payloads["configuration-secrets.json"]; secret && !encrypted {
		return Archive{}, &Error{Code: "SECRET_ENCRYPTION_REQUIRED"}
	}
	var docs Documents
	for _, name := range []string{"config.json", "configuration-secrets.json"} {
		payload, declared := manifest.Payloads[name]
		file := files[name]
		if !declared {
			if file != nil {
				return Archive{}, &Error{Code: "INVALID_ARCHIVE"}
			}
			continue
		}
		if file == nil || uint64(payload.Size) != file.UncompressedSize64 {
			return Archive{}, &Error{Code: "INVALID_ARCHIVE"}
		}
		data, err := memberBytes(ctx, file, MaxDocumentSize)
		if err != nil {
			return Archive{}, err
		}
		digest := sha256.Sum256(data)
		expected, _ := hex.DecodeString(payload.SHA256)
		if !bytes.Equal(digest[:], expected) {
			return Archive{}, &Error{Code: "INVALID_ARCHIVE"}
		}
		version, err := documentSchema(data, name == "configuration-secrets.json")
		if err != nil {
			return Archive{}, err
		}
		if version != payload.SchemaVersion {
			return Archive{}, &Error{Code: "INVALID_ARCHIVE"}
		}
		if name == "config.json" {
			docs.Configuration = data
		} else {
			docs.ConfigurationSecrets = data
		}
	}
	return Archive{Manifest: manifest, Documents: docs}, nil
}
