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
	MaxArchiveSize    = 18 << 20
	MaxZIPSize        = 17 << 20
	MaxManifestSize   = 64 << 10
	MaxDocumentSize   = 4 << 20
	MaxExpandedSize   = 17 << 20
	MaxMembers        = 5
	MaxExpansionRatio = 100
)

type Error struct{ Code string }

func (e *Error) Error() string { return e.Code }

type Documents struct {
	Configuration, ConfigurationSecrets, Users, UserCredentials json.RawMessage
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
	// Realized says the live generation realizes the saved version although it was made from an older one: what
	// changed since takes effect when saved, as notifications do. Only format 3 can say so (#233).
	Realized bool
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
	if source.Network.Live == nil || source.Network.LiveSavedVersion != source.SavedVersion && !source.Realized {
		return []string{"network"}
	}
	return []string{}
}

// MaximumConfigSchema is the newest form of the saved configuration a backup may hold. An engine that knows
// only an older form refuses the archive as unsupported.
const MaximumConfigSchema = 4

func documentSchema(raw []byte, maximum int) (int, error) {
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
	if version.SchemaVersion < 1 || version.SchemaVersion > maximum {
		return 0, &Error{Code: "UNSUPPORTED_SCHEMA"}
	}
	return version.SchemaVersion, nil
}

func Encode(ctx context.Context, source Source, docs Documents, passphrase string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validSource(source) || docs.Configuration == nil || docs.UserCredentials != nil && docs.Users == nil {
		return nil, &Error{Code: "INVALID_ARCHIVE"}
	}
	secret := docs.ConfigurationSecrets != nil || docs.UserCredentials != nil
	if secret {
		if err := ValidatePassphrase(passphrase); err != nil {
			return nil, err
		}
	} else if passphrase != "" {
		return nil, &Error{Code: "PASSPHRASE_INVALID"}
	}
	// Format 1 holds the configuration alone, format 2 the users too. Format 3 holds what format 2 does, and is
	// written only when its areas say what format 2 cannot: a live generation that realizes a newer saved version.
	// Older releases refuse it as unsupported rather than read those areas as a lie; format 1 cannot carry it.
	format := 1
	if docs.Users != nil {
		format = 2
	}
	source.Realized = source.Realized && format == 2 && source.Network.Live != nil && source.Network.LiveSavedVersion != source.SavedVersion
	if source.Realized {
		format = 3
	}
	manifest := Manifest{FormatVersion: format, CreatedAt: source.CapturedAt.UTC(), AndaruVersion: source.AndaruVersion, SavedVersion: source.SavedVersion, Scopes: []string{}, Payloads: map[string]Payload{}, Network: source.Network, SavedButUnappliedAreas: unapplied(source)}
	var identity [16]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return nil, &Error{Code: "ENCRYPTION_FAILED"}
	}
	manifest.BackupID = hex.EncodeToString(identity[:])
	type document struct {
		name, scope string
		data        json.RawMessage
		maximum     int
	}
	documents := []document{{"config.json", "configuration", docs.Configuration, MaximumConfigSchema}}
	if docs.Users != nil {
		documents = append(documents, document{"users.json", "users", docs.Users, 1})
	}
	if docs.ConfigurationSecrets != nil {
		documents = append(documents, document{"configuration-secrets.json", "configurationSecrets", docs.ConfigurationSecrets, 1})
	}
	if docs.UserCredentials != nil {
		documents = append(documents, document{"user-credentials.json", "userCredentials", docs.UserCredentials, 1})
	}
	for _, document := range documents {
		schema, err := documentSchema(document.data, document.maximum)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(document.data)
		manifest.Payloads[document.name] = Payload{SchemaVersion: schema, Size: int64(len(document.data)), SHA256: hex.EncodeToString(digest[:])}
		manifest.Scopes = append(manifest.Scopes, document.scope)
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		return nil, &Error{Code: "INVALID_ARCHIVE"}
	}
	if len(rawManifest) > MaxManifestSize {
		return nil, &Error{Code: "ARCHIVE_LIMIT"}
	}
	var buffer bytes.Buffer
	buffer.Grow(len(rawManifest) + len(docs.Configuration) + len(docs.ConfigurationSecrets) + len(docs.Users) + len(docs.UserCredentials) + 1024)
	writer := zip.NewWriter(&buffer)
	for i := -1; i < len(documents); i++ {
		name, data := "manifest.json", rawManifest
		if i >= 0 {
			name, data = documents[i].name, documents[i].data
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
	if !secret {
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
	if manifest.FormatVersion < 1 || manifest.FormatVersion > 3 {
		return Manifest{}, &Error{Code: "UNSUPPORTED_SCHEMA"}
	}
	shape := min(manifest.FormatVersion, 2) // format 3 holds what format 2 does
	id, err := hex.DecodeString(manifest.BackupID)
	source := Source{AndaruVersion: manifest.AndaruVersion, SavedVersion: manifest.SavedVersion, CapturedAt: manifest.CreatedAt, Network: manifest.Network}
	_, offset := manifest.CreatedAt.Zone()
	if err != nil || len(id) != 16 || !validSource(source) || offset != 0 || manifest.Scopes == nil || manifest.SavedButUnappliedAreas == nil ||
		!slices.Equal(manifest.SavedButUnappliedAreas, unapplied(source)) && manifest.FormatVersion != 3 ||
		// Format 3 says only that a live generation realizes a newer saved version: the engine's finding, which only it
		// could check, and the one archive written in it.
		manifest.FormatVersion == 3 && (source.Network.Live == nil || source.Network.LiveSavedVersion == source.SavedVersion || len(manifest.SavedButUnappliedAreas) != 0) {
		return Manifest{}, invalid
	}
	maxScopes := 2
	if shape == 2 {
		maxScopes = 4
	}
	if len(manifest.Scopes) < shape || len(manifest.Scopes) > maxScopes || len(manifest.Payloads) != len(manifest.Scopes) ||
		manifest.Scopes[0] != "configuration" || shape == 2 && manifest.Scopes[1] != "users" {
		return Manifest{}, invalid
	}
	var rawPayloads map[string]json.RawMessage
	if json.Unmarshal(fields["payloads"], &rawPayloads) != nil {
		return Manifest{}, invalid
	}
	order := map[string]int{"configuration": 0, "users": 1, "configurationSecrets": 2, "userCredentials": 3}
	seen := map[string]bool{}
	last := -1
	for _, scope := range manifest.Scopes {
		position, known := order[scope]
		if !known || position <= last || seen[scope] || shape == 1 && position != 0 && position != 2 {
			return Manifest{}, &Error{Code: "UNSUPPORTED_SCHEMA"}
		}
		last, seen[scope] = position, true
		name, maximum := "config.json", MaximumConfigSchema
		switch scope {
		case "users":
			name, maximum = "users.json", 1
		case "configurationSecrets":
			name, maximum = "configuration-secrets.json", 1
		case "userCredentials":
			name, maximum = "user-credentials.json", 1
		}
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
		if payload.SchemaVersion < 1 || payload.SchemaVersion > maximum {
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
		if binary.LittleEndian.Uint32(raw[end:]) != 0x06054b50 {
			continue
		}
		// Match archive/zip's last-signature selection; never skip trailing or truncated data.
		if end+22+int(binary.LittleEndian.Uint16(raw[end+20:])) != len(raw) {
			return layout, invalid
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
		// shortcut: ZIP64 directories are unsupported within the bounded portable format; extend only for a qualified producer.
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
		if !slices.Contains([]string{"manifest.json", "config.json", "configuration-secrets.json", "users.json", "user-credentials.json"}, file.Name) {
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
	if _, secret := manifest.Payloads["user-credentials.json"]; secret && !encrypted {
		return Archive{}, &Error{Code: "SECRET_ENCRYPTION_REQUIRED"}
	}
	var docs Documents
	for _, name := range []string{"config.json", "users.json", "configuration-secrets.json", "user-credentials.json"} {
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
		maximum := 1
		if name == "config.json" {
			maximum = MaximumConfigSchema
		}
		version, err := documentSchema(data, maximum)
		if err != nil {
			return Archive{}, err
		}
		if version != payload.SchemaVersion {
			return Archive{}, &Error{Code: "INVALID_ARCHIVE"}
		}
		switch name {
		case "config.json":
			docs.Configuration = data
		case "users.json":
			docs.Users = data
		case "configuration-secrets.json":
			docs.ConfigurationSecrets = data
		case "user-credentials.json":
			docs.UserCredentials = data
		}
	}
	return Archive{Manifest: manifest, Documents: docs}, nil
}
