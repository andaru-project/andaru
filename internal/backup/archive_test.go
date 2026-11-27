package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"
)

const configJSON = `{"schemaVersion":1,"links":[],"segments":[],"zones":[]}`
const secretsJSON = `{"schemaVersion":1,"secrets":{"core":{"WAN_PASSWORD":{"kind":"password","value":"TEST_ONLY_SECRET_VALUE"}}}}`
const usersJSON = `{"schemaVersion":1,"identityRevision":7,"accounts":[],"tombstones":[]}`
const credentialsJSON = `{"schemaVersion":1,"identityRevision":7,"passwords":[],"tokens":[]}`

func sourceFixture() Source {
	live := 3
	return Source{AndaruVersion: "0.4.0~dev-1", SavedVersion: "7", CapturedAt: time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC), Network: NetworkObservation{Live: &live, LiveSavedVersion: "6"}}
}

func TestPlainArchiveContainsOnlyDeclaredData(t *testing.T) {
	raw, err := Encode(context.Background(), sourceFixture(), Documents{Configuration: json.RawMessage(configJSON)}, "")
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, file := range z.File {
		names = append(names, file.Name)
		if !file.Mode().IsRegular() || file.Mode().Perm() != 0600 || file.Method != zip.Store {
			t.Fatal("writer emitted executable, special or compressed content")
		}
	}
	if !slices.Equal(names, []string{"manifest.json", "config.json"}) {
		t.Fatalf("unexpected members: %v", names)
	}
	reader, err := z.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		FormatVersion               int
		BackupID                    string
		CreatedAt                   time.Time
		AndaruVersion, SavedVersion string
		Scopes                      []string
		Payloads                    map[string]struct {
			SchemaVersion int
			Size          int64
			SHA256        string
		}
		SavedButUnappliedAreas []string
	}
	if err = json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(configJSON))
	payload := manifest.Payloads["config.json"]
	if manifest.FormatVersion != 1 || len(manifest.BackupID) != 32 || manifest.SavedVersion != "7" || manifest.AndaruVersion != "0.4.0~dev-1" || !manifest.CreatedAt.Equal(sourceFixture().CapturedAt) || payload.SchemaVersion != 1 || payload.Size != int64(len(configJSON)) || payload.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("manifest lost source identity or payload integrity")
	}
	if !slices.Equal(manifest.Scopes, []string{"configuration"}) || !slices.Equal(manifest.SavedButUnappliedAreas, []string{"network"}) {
		t.Fatal("wrong scope or saved-vs-live provenance")
	}
	if bytes.Contains(raw, []byte("TEST_ONLY_SECRET_VALUE")) {
		t.Fatal("default archive contains secret material")
	}
}

type archiveEntry struct {
	name          string
	body          []byte
	mode          fs.FileMode
	method, flags uint16
}

type passthroughCloser struct{ io.Writer }

func (passthroughCloser) Close() error { return nil }

func fixtureZIP(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	writer.RegisterCompressor(99, func(w io.Writer) (io.WriteCloser, error) { return passthroughCloser{w}, nil })
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: entry.method, Flags: entry.flags}
		mode := entry.mode
		if mode == 0 {
			mode = 0600
		}
		header.SetMode(mode)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(entry.name, "/") {
			continue
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

func fixtureManifest(t *testing.T, payload []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(payload)
	manifest := map[string]any{
		"formatVersion": 1, "backupId": strings.Repeat("1", 32), "createdAt": "2026-11-15T09:00:00Z", "andaruVersion": "0.4.0~dev-1", "savedVersion": "7",
		"scopes":   []string{"configuration"},
		"payloads": map[string]any{"config.json": map[string]any{"schemaVersion": 1, "size": len(payload), "sha256": hex.EncodeToString(digest[:])}},
		"network":  map[string]any{"live": 3, "pending": nil, "liveSavedVersion": "7"}, "savedButUnappliedAreas": []string{},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestArchiveReaderAcceptsIndependentFixture(t *testing.T) {
	raw := fixtureZIP(t, []archiveEntry{{name: "manifest.json", body: fixtureManifest(t, []byte(configJSON))}, {name: "config.json", body: []byte(configJSON)}})
	decoded, err := Decode(context.Background(), raw, "")
	if err != nil || string(decoded.Documents.Configuration) != configJSON || decoded.Documents.ConfigurationSecrets != nil {
		t.Fatal("independent archive rejected or changed", err)
	}
}

func TestArchiveReaderAcceptsStoredAndDeflatedDataDescriptors(t *testing.T) {
	for name, method := range map[string]uint16{"stored": zip.Store, "deflated": zip.Deflate} {
		t.Run(name, func(t *testing.T) {
			raw := fixtureZIP(t, []archiveEntry{
				{name: "manifest.json", body: fixtureManifest(t, []byte(configJSON)), method: method},
				{name: "config.json", body: []byte(configJSON), method: method},
			})
			reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range reader.File {
				if file.Flags&8 == 0 {
					t.Fatal("fixture does not use data descriptors")
				}
			}
			decoded, err := Decode(context.Background(), raw, "")
			if err != nil || string(decoded.Documents.Configuration) != configJSON {
				t.Fatal("valid data-descriptor archive rejected", err)
			}
		})
	}
}

func TestHostileArchiveRejected(t *testing.T) {
	basic := func() []archiveEntry {
		return []archiveEntry{{name: "manifest.json", body: fixtureManifest(t, []byte(configJSON))}, {name: "config.json", body: []byte(configJSON)}}
	}
	mutations := map[string]func([]archiveEntry) []archiveEntry{}
	for _, name := range []string{"/config.json", "../config.json", "a/../config.json", "a\\config.json", "config.json\x00", "./config.json", "config.json/", "unknown.json"} {
		mutations[name] = func(entries []archiveEntry) []archiveEntry { entries[1].name = name; return entries }
	}
	mutations["duplicate"] = func(entries []archiveEntry) []archiveEntry { return append(entries, entries[1]) }
	mutations["four-members"] = func(entries []archiveEntry) []archiveEntry { return append(entries, entries[1], entries[1]) }
	mutations["symlink"] = func(entries []archiveEntry) []archiveEntry { entries[1].mode = fs.ModeSymlink | 0600; return entries }
	mutations["device"] = func(entries []archiveEntry) []archiveEntry { entries[1].mode = fs.ModeDevice | 0600; return entries }
	mutations["directory"] = func(entries []archiveEntry) []archiveEntry { entries[1].mode = fs.ModeDir | 0700; return entries }
	mutations["unsupported-method"] = func(entries []archiveEntry) []archiveEntry { entries[1].method = 99; return entries }
	mutations["native-encryption"] = func(entries []archiveEntry) []archiveEntry { entries[1].flags = 1; return entries }
	mutations["bad-hash"] = func(entries []archiveEntry) []archiveEntry {
		entries[1].body = []byte(`{"schemaVersion":1}`)
		return entries
	}
	mutations["missing-payload"] = func(entries []archiveEntry) []archiveEntry { return entries[:1] }
	mutations["oversize-manifest"] = func(entries []archiveEntry) []archiveEntry {
		entries[0].body = bytes.Repeat([]byte(" "), 64*1024+1)
		return entries
	}
	mutations["oversize-document"] = func(entries []archiveEntry) []archiveEntry {
		entries[1].body = bytes.Repeat([]byte(" "), 4*1024*1024+1)
		entries[0].body = fixtureManifest(t, entries[1].body)
		return entries
	}
	mutations["compression-ratio"] = func(entries []archiveEntry) []archiveEntry {
		entries[1].body = append([]byte(configJSON), bytes.Repeat([]byte(" "), 256*1024)...)
		entries[1].method = zip.Deflate
		entries[0].body = fixtureManifest(t, entries[1].body)
		return entries
	}
	mutations["duplicate-json"] = func(entries []archiveEntry) []archiveEntry {
		entries[1].body = []byte(`{"schemaVersion":1,"schemaVersion":1}`)
		entries[0].body = fixtureManifest(t, entries[1].body)
		return entries
	}
	mutations["trailing-json"] = func(entries []archiveEntry) []archiveEntry {
		entries[1].body = []byte(configJSON + ` {}`)
		entries[0].body = fixtureManifest(t, entries[1].body)
		return entries
	}
	mutations["future-schema"] = func(entries []archiveEntry) []archiveEntry {
		entries[1].body = []byte(`{"schemaVersion":4,"links":[],"segments":[],"zones":[]}`)
		entries[0].body = fixtureManifest(t, entries[1].body)
		return entries
	}
	mutations["undeclared-secret"] = func(entries []archiveEntry) []archiveEntry {
		return append(entries, archiveEntry{name: "configuration-secrets.json", body: []byte(secretsJSON)})
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			raw := fixtureZIP(t, mutate(basic()))
			decoded, err := Decode(context.Background(), raw, "")
			if err == nil || decoded.Documents.Configuration != nil || decoded.Documents.ConfigurationSecrets != nil {
				t.Fatal("unsafe archive returned documents")
			}
		})
	}
	for _, raw := range [][]byte{bytes.Repeat([]byte("x"), MaxArchiveSize+1), append(fixtureZIP(t, basic()), bytes.Repeat([]byte("x"), MaxZIPSize)...)} {
		if _, err := Decode(context.Background(), raw, ""); err == nil {
			t.Fatal("accepted oversized input or ZIP")
		}
	}
}

func TestManifestStrictness(t *testing.T) {
	for _, member := range []string{"formatVersion", "backupId", "createdAt", "andaruVersion", "savedVersion", "scopes", "payloads", "network", "savedButUnappliedAreas"} {
		var manifest map[string]json.RawMessage
		if err := json.Unmarshal(fixtureManifest(t, []byte(configJSON)), &manifest); err != nil {
			t.Fatal(err)
		}
		delete(manifest, member)
		raw, _ := json.Marshal(manifest)
		zipBytes := fixtureZIP(t, []archiveEntry{{name: "manifest.json", body: raw}, {name: "config.json", body: []byte(configJSON)}})
		if _, err := Decode(context.Background(), zipBytes, ""); err == nil {
			t.Fatalf("accepted missing manifest field %s", member)
		}
	}
}

func TestArchiveCRCChecksActualPayload(t *testing.T) {
	original := []byte(configJSON + " ")
	changed := []byte(configJSON + "\n")
	raw := fixtureZIP(t, []archiveEntry{{name: "manifest.json", body: fixtureManifest(t, changed)}, {name: "config.json", body: original}})
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	offset, err := reader.File[1].DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	raw[int(offset)+len(original)-1] = '\n'
	decoded, err := Decode(context.Background(), raw, "")
	if err == nil || decoded.Documents.Configuration != nil {
		t.Fatal("payload matching manifest bypassed the ZIP CRC")
	}
}

func TestIdentityDocumentsRequireV2AndCredentialEncryption(t *testing.T) {
	source := sourceFixture()
	plain, err := Encode(context.Background(), source, Documents{
		Configuration: []byte(`{"schemaVersion":3,"links":[],"roles":[],"segments":[],"zones":[]}`),
		Users:         []byte(usersJSON),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(context.Background(), plain, "")
	if err != nil || decoded.Manifest.FormatVersion != 3 || !bytes.Equal(decoded.Documents.Users, []byte(usersJSON)) {
		t.Fatalf("plain identity archive: %+v %v", decoded.Manifest, err)
	}
	if slices.Contains(decoded.Manifest.Scopes, "userCredentials") {
		t.Fatal("default archive declared credentials")
	}

	if _, err = Encode(context.Background(), source, Documents{
		Configuration:   []byte(configJSON),
		Users:           []byte(usersJSON),
		UserCredentials: []byte(credentialsJSON),
	}, ""); err == nil {
		t.Fatal("plaintext credentials were accepted")
	}
	encrypted, err := Encode(context.Background(), source, Documents{
		Configuration:   []byte(configJSON),
		Users:           []byte(usersJSON),
		UserCredentials: []byte(credentialsJSON),
	}, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = Decode(context.Background(), encrypted, "correct horse battery staple")
	if err != nil || !bytes.Equal(decoded.Documents.UserCredentials, []byte(credentialsJSON)) {
		t.Fatalf("encrypted credential archive: %+v %v", decoded.Manifest, err)
	}
}

func TestV1ArchiveRemainsReadableWithoutIdentityMembers(t *testing.T) {
	raw := fixtureZIP(t, []archiveEntry{
		{name: "manifest.json", body: fixtureManifest(t, []byte(configJSON))},
		{name: "config.json", body: []byte(configJSON)},
	})
	decoded, err := Decode(context.Background(), raw, "")
	if err != nil || decoded.Manifest.FormatVersion != 1 || decoded.Documents.Users != nil || decoded.Documents.UserCredentials != nil {
		t.Fatalf("v1 archive: %+v %v", decoded, err)
	}
}

// Format 3 trusts the engine's unapplied areas over a live generation; format 2 and the factory network keep them
// derived from the versions, and a later format is unsupported (#233).
func TestFormat3UnappliedAreas(t *testing.T) {
	archive := func(format int, live any, areas []string) []byte {
		config, users := []byte(configJSON), []byte(usersJSON)
		cd, ud := sha256.Sum256(config), sha256.Sum256(users)
		liveSaved := "6"
		if live == nil {
			liveSaved = ""
		}
		raw, err := json.Marshal(map[string]any{
			"formatVersion": format, "backupId": strings.Repeat("1", 32), "createdAt": "2026-11-15T09:00:00Z", "andaruVersion": "0.4.0~dev-1", "savedVersion": "7",
			"scopes": []string{"configuration", "users"},
			"payloads": map[string]any{
				"config.json": map[string]any{"schemaVersion": 1, "size": len(config), "sha256": hex.EncodeToString(cd[:])},
				"users.json":  map[string]any{"schemaVersion": 1, "size": len(users), "sha256": hex.EncodeToString(ud[:])},
			},
			"network": map[string]any{"live": live, "pending": nil, "liveSavedVersion": liveSaved}, "savedButUnappliedAreas": areas,
		})
		if err != nil {
			t.Fatal(err)
		}
		return fixtureZIP(t, []archiveEntry{{name: "manifest.json", body: raw}, {name: "config.json", body: config}, {name: "users.json", body: users}})
	}
	for _, c := range []struct {
		format int
		live   any
		areas  []string
		code   string // "" accepted
	}{
		{2, 3, []string{"network"}, ""},
		{2, 3, []string{}, "INVALID_ARCHIVE"},
		{3, 3, []string{}, ""},
		{3, 3, []string{"network"}, ""},
		{3, nil, []string{}, "INVALID_ARCHIVE"},
		{3, nil, []string{"network"}, ""},
		{4, 3, []string{"network"}, "UNSUPPORTED_SCHEMA"},
	} {
		_, err := Decode(context.Background(), archive(c.format, c.live, c.areas), "")
		var e *Error
		if c.code == "" && err != nil || c.code != "" && (!errors.As(err, &e) || e.Code != c.code) {
			t.Errorf("format %d, live %v, areas %q: %v", c.format, c.live, c.areas, err)
		}
	}

	// The engine's finding reaches a format 3 archive; a format 1 archive cannot carry it.
	source := sourceFixture()
	source.Realized = true
	for _, users := range [][]byte{[]byte(usersJSON), nil} {
		raw, err := Encode(context.Background(), source, Documents{Configuration: json.RawMessage(configJSON), Users: users}, "")
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(context.Background(), raw, "")
		want, format := []string{}, 3
		if users == nil {
			want, format = []string{"network"}, 1
		}
		if err != nil || decoded.Manifest.FormatVersion != format || !slices.Equal(decoded.Manifest.SavedButUnappliedAreas, want) {
			t.Fatalf("users %v: %+v %v", users != nil, decoded.Manifest, err)
		}
	}
}
