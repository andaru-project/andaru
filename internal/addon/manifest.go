// Package addon defines Andaru's declarative add-on manifest contract, strict validation,
// image-shipped catalog loading, and manifest-driven settings validation.
package addon

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// SupportedManifestVersion is the only manifest format version accepted in Slice 6.
	SupportedManifestVersion = 1
	// ExtensionAPIName is the compatibility identifier checked in manifest requires.
	ExtensionAPIName = "andaru.extension-api"
	// MaxManifestBytes bounds a single manifest.json file.
	MaxManifestBytes = 256 << 10
)

var (
	idPattern          = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}(\.[a-z][a-z0-9-]{0,31})+$`)
	slugPattern        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	propNamePattern    = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)
	envNamePattern     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	argNamePattern     = regexp.MustCompile(`^--?[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	ifNamePattern      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,14}$`)
	secretNamePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	imageDigestPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*(\.[a-z0-9._-]+)?(/[a-z0-9._-]+)+@sha256:[0-9a-f]{64}$`)
	hostnamePattern    = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)
	i18nKeyPattern     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	permNamePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// ErrUnsupportedManifestVersion reports a manifest whose manifestVersion or requires cannot run on this Andaru.
var ErrUnsupportedManifestVersion = errors.New("unsupported add-on manifest version or API requirement")

// FieldError locates one validation failure with a JSON Pointer path.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError collects one or more structured field validation errors.
type ValidationError []FieldError

func (e ValidationError) Error() string {
	if len(e) == 0 {
		return "validation failed"
	}
	return fmt.Sprintf("validation failed at %s: %s", e[0].Path, e[0].Message)
}

// Text holds either literal English copy or an explicit localization reference {"i18n": "key"}.
type Text struct {
	Literal string
	I18n    string
}

func (t Text) Empty() bool { return t.Literal == "" && t.I18n == "" }

func (t Text) MarshalJSON() ([]byte, error) {
	if t.I18n != "" {
		return json.Marshal(struct {
			I18n string `json:"i18n"`
		}{I18n: t.I18n})
	}
	return json.Marshal(t.Literal)
}

func (t *Text) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return errors.New("empty text")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		if strings.TrimSpace(s) == "" {
			return errors.New("text must not be empty")
		}
		*t = Text{Literal: s}
		return nil
	}
	if raw[0] == '{' {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		if len(m) != 1 {
			return errors.New(`i18n text object must contain only the "i18n" key`)
		}
		v, ok := m["i18n"].(string)
		if !ok || !i18nKeyPattern.MatchString(v) {
			return errors.New(`invalid "i18n" key`)
		}
		*t = Text{I18n: v}
		return nil
	}
	return errors.New(`expected string or {"i18n": "key"}`)
}

// Manifest is one parsed and validated v1 add-on catalog manifest.
type Manifest struct {
	ManifestVersion int             `json:"manifestVersion"`
	ID              string          `json:"id"`
	Slug            string          `json:"slug"`
	Name            string          `json:"name"`
	Description     *Text           `json:"description,omitempty"`
	Version         string          `json:"version"`
	Application     Application     `json:"application"`
	Requires        []Requirement   `json:"requires"`
	Permissions     []Permission    `json:"permissions"`
	Properties      []Property      `json:"properties,omitempty"`
	Runtime         Runtime         `json:"runtime"`
	Interfaces      []InterfaceSpec `json:"interfaces,omitempty"`
	Storage         []StorageSpec   `json:"storage,omitempty"`
	Endpoints       []EndpointSpec  `json:"endpoints,omitempty"`
	Bus             *BusSpec        `json:"bus,omitempty"`
	Logs            []LogSpec       `json:"logs,omitempty"`
	Metrics         []MetricSpec    `json:"metrics,omitempty"`
	Health          *HealthSpec     `json:"health,omitempty"`
	WebUI           *WebUISpec      `json:"webui,omitempty"`
}

type Application struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Requirement struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Permission struct {
	Name  string `json:"name"`
	Title Text   `json:"title"`
}

type Runtime struct {
	Image              string            `json:"image"`
	Network            string            `json:"network"`
	Profile            string            `json:"profile,omitempty"`
	Capabilities       []string          `json:"capabilities,omitempty"`
	Devices            []string          `json:"devices,omitempty"`
	Command            []string          `json:"command,omitempty"`
	Args               []string          `json:"args,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	SettingsActivation string            `json:"settingsActivation,omitempty"`
	StopTimeoutSeconds int               `json:"stopTimeoutSeconds,omitempty"`
}

type InterfaceSpec struct {
	Name         string `json:"name"`
	Title        Text   `json:"title"`
	ZoneRequired bool   `json:"zoneRequired,omitempty"`
}

type StorageSpec struct {
	Name      string `json:"name"`
	Purpose   string `json:"purpose"`
	MountPath string `json:"mountPath"`
	ReadOnly  bool   `json:"readOnly,omitempty"`
	Backup    bool   `json:"backup,omitempty"`
}

type EndpointSpec struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Exposure string `json:"exposure"`
}

type BusSpec struct {
	Publish   []BusTopicSpec `json:"publish,omitempty"`
	Subscribe []BusTopicSpec `json:"subscribe,omitempty"`
}

type BusTopicSpec struct {
	Topic       string `json:"topic"`
	Title       Text   `json:"title"`
	Description *Text  `json:"description,omitempty"`
}

type LogSpec struct {
	Name       string    `json:"name"`
	Title      Text      `json:"title"`
	Permission string    `json:"permission"`
	Source     LogSource `json:"source"`
	Format     string    `json:"format,omitempty"`
}

type LogSource struct {
	Type    string `json:"type"`
	Storage string `json:"storage,omitempty"`
	Path    string `json:"path,omitempty"`
}

type MetricSpec struct {
	Name       string `json:"name"`
	Title      Text   `json:"title"`
	Permission string `json:"permission"`
	Endpoint   string `json:"endpoint"`
	Path       string `json:"path"`
	Format     string `json:"format"`
}

type HealthSpec struct {
	Endpoint           string   `json:"endpoint,omitempty"`
	Path               string   `json:"path,omitempty"`
	ExpectedStatus     int      `json:"expectedStatus,omitempty"`
	HealthCmd          []string `json:"healthCmd,omitempty"`
	IntervalSeconds    int      `json:"intervalSeconds,omitempty"`
	TimeoutSeconds     int      `json:"timeoutSeconds,omitempty"`
	StartPeriodSeconds int      `json:"startPeriodSeconds,omitempty"`
}

type WebUISpec struct {
	Title           Text   `json:"title"`
	Permission      string `json:"permission"`
	Endpoint        string `json:"endpoint"`
	HostnameSetting string `json:"hostnameSetting,omitempty"`
	Path            string `json:"path"`
}

type Property struct {
	Name        string       `json:"name"`
	Type        string       `json:"type"`
	Format      string       `json:"format,omitempty"`
	ItemFormat  string       `json:"itemFormat,omitempty"`
	Pattern     string       `json:"pattern,omitempty"`
	MinLength   *int         `json:"minLength,omitempty"`
	MaxLength   *int         `json:"maxLength,omitempty"`
	Minimum     *float64     `json:"minimum,omitempty"`
	Maximum     *float64     `json:"maximum,omitempty"`
	MinItems    *int         `json:"minItems,omitempty"`
	MaxItems    *int         `json:"maxItems,omitempty"`
	Options     []EnumOption `json:"options,omitempty"`
	Required    bool         `json:"required,omitempty"`
	Default     any          `json:"default,omitempty"`
	Title       Text         `json:"title"`
	Description *Text        `json:"description,omitempty"`
	Binding     *Binding     `json:"binding,omitempty"`
}

type EnumOption struct {
	Value       string `json:"value"`
	Title       Text   `json:"title"`
	Description *Text  `json:"description,omitempty"`
}

type Binding struct {
	Type     string    `json:"type"`
	Name     string    `json:"name,omitempty"`
	Style    string    `json:"style,omitempty"`
	Format   string    `json:"format,omitempty"`
	Env      string    `json:"env,omitempty"`
	Prefix   string    `json:"prefix,omitempty"`
	Encoding *Encoding `json:"encoding,omitempty"`
}

type Encoding struct {
	Mode      string `json:"mode"`
	Separator string `json:"separator,omitempty"`
}

// ParseManifest strictly parses and validates a manifest.json payload.
func ParseManifest(raw []byte) (*Manifest, error) {
	if len(raw) == 0 || len(raw) > MaxManifestBytes {
		return nil, ValidationError{{Path: "", Message: "manifest size out of bounds"}}
	}
	if err := checkStrictJSON(raw); err != nil {
		return nil, ValidationError{{Path: "", Message: err.Error()}}
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return nil, ValidationError{{Path: "", Message: "manifest must be a JSON object"}}
	}
	// Validate manifestVersion and requires BEFORE detailed unknown-field checks so a manifest
	// targeting a newer Andaru produces a compatibility error instead of reporting new fields as typos.
	if err := checkCompatibility(top); err != nil {
		return nil, err
	}
	if errs := checkUnknownFields(raw); len(errs) > 0 {
		return nil, ValidationError(errs)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, ValidationError{{Path: "", Message: err.Error()}}
	}
	// Apply documented defaults.
	if m.Runtime.Profile == "" {
		m.Runtime.Profile = "unprivileged"
	}
	if m.Runtime.SettingsActivation == "" {
		m.Runtime.SettingsActivation = "recreate"
	}
	if m.Runtime.StopTimeoutSeconds == 0 {
		m.Runtime.StopTimeoutSeconds = 5
	}
	if errs := validateManifest(&m); len(errs) > 0 {
		return nil, ValidationError(errs)
	}
	return &m, nil
}

// checkStrictJSON rejects duplicate keys, trailing data, invalid UTF-8, excessive nesting, and null values.
func checkStrictJSON(raw []byte) error {
	dec := jsontext.NewDecoder(bytes.NewReader(raw), jsontext.AllowDuplicateNames(false), jsontext.AllowInvalidUTF8(false))
	complete := false
	for {
		tok, err := dec.ReadToken()
		if err == io.EOF {
			if !complete {
				return errors.New("empty JSON document")
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
		if complete {
			return errors.New("unexpected trailing JSON content")
		}
		if dec.StackDepth() > 32 {
			return errors.New("JSON nesting limit exceeded")
		}
		if tok.Kind() == 'n' {
			return errors.New("null values are not allowed in manifest")
		}
		if dec.StackDepth() == 0 {
			complete = true
		}
	}
}

func checkCompatibility(top map[string]json.RawMessage) error {
	rawVer, ok := top["manifestVersion"]
	if !ok {
		return ValidationError{{Path: "/manifestVersion", Message: "manifestVersion is required"}}
	}
	var ver int
	if err := json.Unmarshal(rawVer, &ver); err != nil || string(bytes.TrimSpace(rawVer)) != fmt.Sprintf("%d", ver) {
		return ValidationError{{Path: "/manifestVersion", Message: "manifestVersion must be an integer"}}
	}
	if ver != SupportedManifestVersion {
		return fmt.Errorf("%w: manifestVersion %d (supported: %d)", ErrUnsupportedManifestVersion, ver, SupportedManifestVersion)
	}
	rawReq, ok := top["requires"]
	if !ok {
		return ValidationError{{Path: "/requires", Message: "requires is required"}}
	}
	var reqs []Requirement
	if err := json.Unmarshal(rawReq, &reqs); err != nil || len(reqs) == 0 {
		return ValidationError{{Path: "/requires", Message: "requires must be a non-empty array"}}
	}
	seenReq := map[string]bool{}
	hasAPI := false
	for i, req := range reqs {
		p := fmt.Sprintf("/requires/%d", i)
		if req.Name == "" || req.Version == "" {
			return ValidationError{{Path: p, Message: "name and version are required"}}
		}
		if seenReq[req.Name] {
			return ValidationError{{Path: p + "/name", Message: "duplicate requirement"}}
		}
		seenReq[req.Name] = true
		if req.Name == ExtensionAPIName {
			hasAPI = true
			if !compatibleAPI1(req.Version) {
				return fmt.Errorf("%w: %s %q is not satisfied by 1.0.0", ErrUnsupportedManifestVersion, req.Name, req.Version)
			}
		} else {
			return fmt.Errorf("%w: unknown requirement %q", ErrUnsupportedManifestVersion, req.Name)
		}
	}
	if !hasAPI {
		return ValidationError{{Path: "/requires", Message: fmt.Sprintf("must require %s", ExtensionAPIName)}}
	}
	return nil
}

// compatibleAPI1 checks simple semver range clauses (e.g. ">=1.0.0 <2.0.0" or "1.0.0") against API version 1.0.0.
func compatibleAPI1(expr string) bool {
	fields := strings.Fields(expr)
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		op := "="
		rest := f
		for _, pfx := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(f, pfx) {
				op = pfx
				rest = strings.TrimPrefix(f, pfx)
				break
			}
		}
		v, ok := parseSemver3(rest)
		if !ok {
			return false
		}
		cmp := compareSemver([3]int{1, 0, 0}, v)
		switch op {
		case "=":
			if cmp != 0 {
				return false
			}
		case ">=":
			if cmp < 0 {
				return false
			}
		case "<=":
			if cmp > 0 {
				return false
			}
		case ">":
			if cmp <= 0 {
				return false
			}
		case "<":
			if cmp >= 0 {
				return false
			}
		}
	}
	return true
}

func parseSemver3(s string) ([3]int, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return [3]int{}, false
		}
		n := 0
		for _, c := range part {
			if c < '0' || c > '9' {
				return [3]int{}, false
			}
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out, true
}

func compareSemver(a, b [3]int) int {
	for i := range 3 {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// checkUnknownFields walks the raw JSON object tree and reports every unknown key with its JSON Pointer path.
func checkUnknownFields(raw []byte) []FieldError {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return []FieldError{{Path: "", Message: err.Error()}}
	}
	var errs []FieldError
	checkObj := func(obj any, p string, allowed ...string) map[string]any {
		m, ok := obj.(map[string]any)
		if !ok {
			return nil
		}
		for k := range m {
			if !slices.Contains(allowed, k) {
				errs = append(errs, FieldError{Path: p + "/" + escapePointer(k), Message: fmt.Sprintf("unknown field %q", k)})
			}
		}
		return m
	}
	checkArr := func(v any, p string, fn func(item any, itemPath string)) {
		arr, ok := v.([]any)
		if !ok {
			return
		}
		for i, item := range arr {
			fn(item, fmt.Sprintf("%s/%d", p, i))
		}
	}

	m := checkObj(root, "",
		"manifestVersion", "id", "slug", "name", "description", "version", "application",
		"requires", "permissions", "properties", "runtime", "interfaces", "storage",
		"endpoints", "bus", "logs", "metrics", "health", "webui",
	)
	checkObj(m["application"], "/application", "name", "version")
	checkArr(m["requires"], "/requires", func(item any, p string) {
		checkObj(item, p, "name", "version")
	})
	checkArr(m["permissions"], "/permissions", func(item any, p string) {
		checkObj(item, p, "name", "title")
	})
	checkObj(m["runtime"], "/runtime",
		"image", "network", "profile", "capabilities", "devices", "command", "args", "env",
		"settingsActivation", "stopTimeoutSeconds",
	)
	checkArr(m["interfaces"], "/interfaces", func(item any, p string) {
		checkObj(item, p, "name", "title", "zoneRequired")
	})
	checkArr(m["storage"], "/storage", func(item any, p string) {
		checkObj(item, p, "name", "purpose", "mountPath", "readOnly", "backup")
	})
	checkArr(m["endpoints"], "/endpoints", func(item any, p string) {
		checkObj(item, p, "name", "protocol", "port", "exposure")
	})
	if busObj := checkObj(m["bus"], "/bus", "publish", "subscribe"); busObj != nil {
		checkArr(busObj["publish"], "/bus/publish", func(item any, p string) {
			checkObj(item, p, "topic", "title", "description")
		})
		checkArr(busObj["subscribe"], "/bus/subscribe", func(item any, p string) {
			checkObj(item, p, "topic", "title", "description")
		})
	}
	checkArr(m["logs"], "/logs", func(item any, p string) {
		if l := checkObj(item, p, "name", "title", "permission", "source", "format"); l != nil {
			checkObj(l["source"], p+"/source", "type", "storage", "path")
		}
	})
	checkArr(m["metrics"], "/metrics", func(item any, p string) {
		checkObj(item, p, "name", "title", "permission", "endpoint", "path", "format")
	})
	checkObj(m["health"], "/health", "endpoint", "path", "expectedStatus", "healthCmd", "intervalSeconds", "timeoutSeconds", "startPeriodSeconds")
	checkObj(m["webui"], "/webui", "title", "permission", "endpoint", "hostnameSetting", "path")
	checkArr(m["properties"], "/properties", func(item any, p string) {
		prop := checkObj(item, p,
			"name", "type", "format", "itemFormat", "pattern", "minLength", "maxLength",
			"minimum", "maximum", "minItems", "maxItems", "options", "required", "default",
			"title", "description", "binding",
		)
		if prop == nil {
			return
		}
		checkArr(prop["options"], p+"/options", func(opt any, op string) {
			checkObj(opt, op, "value", "title", "description")
		})
		if b := checkObj(prop["binding"], p+"/binding", "type", "name", "style", "format", "env", "prefix", "encoding"); b != nil {
			checkObj(b["encoding"], p+"/binding/encoding", "mode", "separator")
		}
	})
	slices.SortFunc(errs, func(a, b FieldError) int { return strings.Compare(a.Path, b.Path) })
	return errs
}

func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func validateManifest(m *Manifest) []FieldError {
	var errs []FieldError
	bad := func(p, msg string) {
		errs = append(errs, FieldError{Path: p, Message: msg})
	}

	if !idPattern.MatchString(m.ID) {
		bad("/id", "invalid reverse-DNS add-on id")
	}
	if !slugPattern.MatchString(m.Slug) {
		bad("/slug", "invalid add-on slug")
	}
	if strings.TrimSpace(m.Name) == "" {
		bad("/name", "name is required")
	}
	if strings.TrimSpace(m.Version) == "" {
		bad("/version", "version is required")
	}
	if strings.TrimSpace(m.Application.Name) == "" {
		bad("/application/name", "application name is required")
	}
	if strings.TrimSpace(m.Application.Version) == "" {
		bad("/application/version", "application version is required")
	}

	perms := map[string]bool{}
	for i, perm := range m.Permissions {
		p := fmt.Sprintf("/permissions/%d", i)
		if !permNamePattern.MatchString(perm.Name) {
			bad(p+"/name", "invalid permission name")
		} else if perms[perm.Name] {
			bad(p+"/name", "duplicate permission name")
		} else {
			perms[perm.Name] = true
		}
		if perm.Title.Empty() {
			bad(p+"/title", "permission title is required")
		}
	}

	// Runtime validation and static env reservation.
	usedEnv := map[string]string{}
	validateRuntime(m, usedEnv, bad)

	// Interfaces.
	ifaces := map[string]bool{}
	for i, ifc := range m.Interfaces {
		p := fmt.Sprintf("/interfaces/%d", i)
		if !validAddonInterfaceName(ifc.Name) {
			bad(p+"/name", "invalid or reserved interface name")
		} else if ifaces[ifc.Name] {
			bad(p+"/name", "duplicate interface name")
		} else {
			ifaces[ifc.Name] = true
		}
		if ifc.Title.Empty() {
			bad(p+"/title", "interface title is required")
		}
	}

	// Storage.
	storages := map[string]bool{}
	mounts := map[string]bool{}
	for i, st := range m.Storage {
		p := fmt.Sprintf("/storage/%d", i)
		if !propNamePattern.MatchString(st.Name) {
			bad(p+"/name", "storage name must be a valid identifier")
		} else if storages[st.Name] {
			bad(p+"/name", "duplicate storage name")
		} else {
			storages[st.Name] = true
		}
		if !slices.Contains([]string{"state", "data", "cache", "logs"}, st.Purpose) {
			bad(p+"/purpose", "unsupported storage purpose")
		}
		if !validContainerPath(st.MountPath) {
			bad(p+"/mountPath", "mountPath must be a clean absolute container path")
		} else if mounts[st.MountPath] {
			bad(p+"/mountPath", "duplicate storage mountPath")
		} else {
			mounts[st.MountPath] = true
		}
	}

	// Endpoints.
	endpoints := map[string]EndpointSpec{}
	for i, ep := range m.Endpoints {
		p := fmt.Sprintf("/endpoints/%d", i)
		if !propNamePattern.MatchString(ep.Name) {
			bad(p+"/name", "invalid endpoint name")
		} else if _, dup := endpoints[ep.Name]; dup {
			bad(p+"/name", "duplicate endpoint name")
		} else {
			endpoints[ep.Name] = ep
		}
		if !slices.Contains([]string{"http", "https", "tcp", "udp"}, ep.Protocol) {
			bad(p+"/protocol", "unsupported endpoint protocol")
		}
		if ep.Port < 1 || ep.Port > 65535 {
			bad(p+"/port", "port must be between 1 and 65535")
		}
		if ep.Exposure != "internal" && ep.Exposure != "managed" {
			bad(p+"/exposure", `exposure must be "internal" or "managed"`)
		}
	}

	// Bus requests.
	if m.Bus != nil {
		validateBus(m.Slug, m.Bus, bad)
	}

	// Logs.
	logNames := map[string]bool{}
	for i, l := range m.Logs {
		p := fmt.Sprintf("/logs/%d", i)
		if !propNamePattern.MatchString(l.Name) {
			bad(p+"/name", "invalid log name")
		} else if logNames[l.Name] {
			bad(p+"/name", "duplicate log name")
		} else {
			logNames[l.Name] = true
		}
		if l.Title.Empty() {
			bad(p+"/title", "log title is required")
		}
		if !perms[l.Permission] {
			bad(p+"/permission", "undeclared permission")
		}
		switch l.Source.Type {
		case "runtime":
			if l.Source.Storage != "" || l.Source.Path != "" {
				bad(p+"/source", "runtime log source must not specify storage or path")
			}
		case "file":
			if !storages[l.Source.Storage] {
				bad(p+"/source/storage", "undeclared storage")
			}
			if !validRelativePath(l.Source.Path) {
				bad(p+"/source/path", "log file path must be a safe relative path")
			}
		default:
			bad(p+"/source/type", `log source type must be "runtime" or "file"`)
		}
		if l.Format != "" && l.Format != "text" && l.Format != "json-lines" {
			bad(p+"/format", `log format must be "text" or "json-lines"`)
		}
	}

	// Metrics.
	metricNames := map[string]bool{}
	for i, mt := range m.Metrics {
		p := fmt.Sprintf("/metrics/%d", i)
		if !propNamePattern.MatchString(mt.Name) {
			bad(p+"/name", "invalid metric name")
		} else if metricNames[mt.Name] {
			bad(p+"/name", "duplicate metric name")
		} else {
			metricNames[mt.Name] = true
		}
		if mt.Title.Empty() {
			bad(p+"/title", "metric title is required")
		}
		if !perms[mt.Permission] {
			bad(p+"/permission", "undeclared permission")
		}
		ep, ok := endpoints[mt.Endpoint]
		if !ok {
			bad(p+"/endpoint", "undeclared endpoint")
		} else if ep.Exposure != "internal" {
			bad(p+"/endpoint", "metrics endpoint must have internal exposure")
		}
		if !strings.HasPrefix(mt.Path, "/") {
			bad(p+"/path", "metrics path must start with /")
		}
		if mt.Format != "prometheus" {
			bad(p+"/format", `metrics format must be "prometheus"`)
		}
	}

	// Health.
	if m.Health != nil {
		hasHTTP := m.Health.Endpoint != "" || m.Health.Path != ""
		hasCmd := len(m.Health.HealthCmd) > 0
		if hasHTTP == hasCmd {
			bad("/health", "health must declare either an internal HTTP endpoint or healthCmd, not both")
		}
		if hasHTTP {
			ep, ok := endpoints[m.Health.Endpoint]
			if !ok {
				bad("/health/endpoint", "undeclared endpoint")
			} else if ep.Exposure != "internal" {
				bad("/health/endpoint", "health endpoint must have internal exposure")
			}
			if !strings.HasPrefix(m.Health.Path, "/") {
				bad("/health/path", "health path must start with /")
			}
			if m.Health.ExpectedStatus != 0 && (m.Health.ExpectedStatus < 100 || m.Health.ExpectedStatus > 599) {
				bad("/health/expectedStatus", "expectedStatus must be a valid HTTP status code")
			}
		}
	}

	// Properties and bindings.
	propMap := map[string]Property{}
	usedArg := map[string]string{}
	usedSecretFile := map[string]string{}
	for i := range m.Properties {
		prop := &m.Properties[i]
		p := fmt.Sprintf("/properties/%d", i)
		if !propNamePattern.MatchString(prop.Name) {
			bad(p+"/name", "invalid property name")
		} else if _, dup := propMap[prop.Name]; dup {
			bad(p+"/name", "duplicate property name")
		} else {
			propMap[prop.Name] = *prop
		}
		validateProperty(prop, p, usedEnv, usedArg, usedSecretFile, bad)
	}

	// WebUI link.
	if m.WebUI != nil {
		if m.WebUI.Title.Empty() {
			bad("/webui/title", "webui title is required")
		}
		if !perms[m.WebUI.Permission] {
			bad("/webui/permission", "undeclared permission")
		}
		if _, ok := endpoints[m.WebUI.Endpoint]; !ok {
			bad("/webui/endpoint", "undeclared endpoint")
		}
		if !strings.HasPrefix(m.WebUI.Path, "/") {
			bad("/webui/path", "webui path must start with /")
		}
		if m.WebUI.HostnameSetting != "" {
			if !strings.HasPrefix(m.WebUI.HostnameSetting, "/") {
				bad("/webui/hostnameSetting", "hostnameSetting must be a property pointer like /hostname")
			} else if ref, ok := propMap[strings.TrimPrefix(m.WebUI.HostnameSetting, "/")]; !ok || ref.Type != "string" || ref.Format != "hostname" {
				bad("/webui/hostnameSetting", "hostnameSetting must reference a string property with format hostname")
			}
		}
	}

	return errs
}

func validateRuntime(m *Manifest, usedEnv map[string]string, bad func(string, string)) {
	rt := m.Runtime
	if !imageDigestPattern.MatchString(rt.Image) {
		bad("/runtime/image", "runtime image must be pinned by immutable @sha256 digest")
	}
	if rt.Network != "host" {
		bad("/runtime/network", `only "host" networking is supported in Slice 6`)
	}
	if rt.Profile != "unprivileged" && rt.Profile != "network-interface" {
		bad("/runtime/profile", `profile must be "unprivileged" or "network-interface"`)
	}
	if rt.Profile == "unprivileged" {
		if len(rt.Capabilities) > 0 {
			bad("/runtime/capabilities", "unprivileged profile may not request capabilities")
		}
		if len(rt.Devices) > 0 {
			bad("/runtime/devices", "unprivileged profile may not request devices")
		}
	} else {
		seenCap := map[string]bool{}
		for i, c := range rt.Capabilities {
			p := fmt.Sprintf("/runtime/capabilities/%d", i)
			if c != "NET_ADMIN" && c != "NET_RAW" {
				bad(p, fmt.Sprintf("capability %q is not in the Slice 6 allowlist (NET_ADMIN, NET_RAW)", c))
			} else if seenCap[c] {
				bad(p, "duplicate capability")
			}
			seenCap[c] = true
		}
		seenDev := map[string]bool{}
		for i, d := range rt.Devices {
			p := fmt.Sprintf("/runtime/devices/%d", i)
			if d != "/dev/net/tun" {
				bad(p, fmt.Sprintf("device %q is not in the Slice 6 allowlist (/dev/net/tun)", d))
			} else if seenDev[d] {
				bad(p, "duplicate device")
			}
			seenDev[d] = true
		}
	}
	if rt.SettingsActivation != "recreate" {
		bad("/runtime/settingsActivation", `only "recreate" settingsActivation is supported in Slice 6`)
	}
	if rt.StopTimeoutSeconds < 1 || rt.StopTimeoutSeconds > 10 {
		bad("/runtime/stopTimeoutSeconds", "stopTimeoutSeconds must be between 1 and 10")
	}
	for k := range rt.Env {
		p := "/runtime/env/" + escapePointer(k)
		if !envNamePattern.MatchString(k) {
			bad(p, "invalid environment variable name")
		} else if strings.HasPrefix(k, "ANDARU_") {
			bad(p, "ANDARU_ prefix is reserved for platform-injected variables")
		} else {
			usedEnv[k] = p
		}
	}
}

func validAddonInterfaceName(name string) bool {
	if !ifNamePattern.MatchString(name) {
		return false
	}
	for _, forbidden := range []string{"en", "eth", "andaru", "podman"} {
		if strings.HasPrefix(name, forbidden) {
			return false
		}
	}
	return name != "lo"
}

func validContainerPath(p string) bool {
	if !strings.HasPrefix(p, "/") || p == "/" || strings.ContainsRune(p, 0) {
		return false
	}
	if path.Clean(p) != p {
		return false
	}
	return !slices.Contains(strings.Split(p, "/"), "..")
}

func validRelativePath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return false
	}
	clean := path.Clean(p)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && clean == p
}

var (
	forbiddenBusPublish = []string{
		"notification/#", "delivery/#", "alias-fetch/#", "alias-result/#",
		"config/#", "apply/#", "notifications/#",
	}
	forbiddenBusSubscribe = []string{
		"notification/#", "delivery/#", "alias-fetch/#",
	}
)

func validateBus(slug string, b *BusSpec, bad func(string, string)) {
	allowedNotify := "notification/addon." + slug
	checkList := func(list []BusTopicSpec, basePath string, isPublish bool) {
		seen := map[string]bool{}
		for i, item := range list {
			p := fmt.Sprintf("%s/%d", basePath, i)
			if item.Title.Empty() {
				bad(p+"/title", "bus topic title is required")
			}
			if !validBusFilter(item.Topic) {
				bad(p+"/topic", "invalid bus topic filter")
				continue
			}
			if seen[item.Topic] {
				bad(p+"/topic", "duplicate bus topic filter")
				continue
			}
			seen[item.Topic] = true
			levels := strings.Split(item.Topic, "/")
			if levels[0] == "#" || levels[0] == "+" {
				bad(p+"/topic", "first topic level may not be a wildcard")
				continue
			}
			if isPublish {
				if item.Topic == allowedNotify {
					continue
				}
				for _, fb := range forbiddenBusPublish {
					if filtersOverlap(item.Topic, fb) {
						bad(p+"/topic", fmt.Sprintf("publish topic %q overlaps forbidden namespace %q", item.Topic, fb))
						break
					}
				}
			} else {
				for _, fb := range forbiddenBusSubscribe {
					if filtersOverlap(item.Topic, fb) {
						bad(p+"/topic", fmt.Sprintf("subscribe topic %q overlaps forbidden namespace %q", item.Topic, fb))
						break
					}
				}
			}
		}
	}
	checkList(b.Publish, "/bus/publish", true)
	checkList(b.Subscribe, "/bus/subscribe", false)
}

func validBusFilter(filter string) bool {
	if filter == "" || strings.ContainsRune(filter, 0) {
		return false
	}
	levels := strings.Split(filter, "/")
	for i, level := range levels {
		switch {
		case level == "":
			return false
		case level == "#":
			if i != len(levels)-1 {
				return false
			}
		case level != "+" && strings.ContainsAny(level, "+#"):
			return false
		}
	}
	return true
}

// filtersOverlap reports whether any concrete topic could match both a and b.
func filtersOverlap(a, b string) bool {
	fa, fb := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; ; i++ {
		if i < len(fa) && fa[i] == "#" {
			return true
		}
		if i < len(fb) && fb[i] == "#" {
			return true
		}
		if i == len(fa) && i == len(fb) {
			return true
		}
		if i == len(fa) || i == len(fb) {
			return false
		}
		if fa[i] != "+" && fb[i] != "+" && fa[i] != fb[i] {
			return false
		}
	}
}

func validateProperty(prop *Property, p string, usedEnv, usedArg, usedSecretFile map[string]string, bad func(string, string)) {
	if prop.Title.Empty() {
		bad(p+"/title", "property title is required")
	}
	switch prop.Type {
	case "string":
		if prop.Format != "" && !slices.Contains([]string{"hostname", "uri", "ipv4", "ipv6", "cidr", "date", "date-time"}, prop.Format) {
			bad(p+"/format", "unsupported string format")
		}
		if prop.Pattern != "" {
			if _, err := regexp.Compile(prop.Pattern); err != nil {
				bad(p+"/pattern", "invalid regular expression")
			}
		}
		if prop.MinLength != nil && *prop.MinLength < 0 {
			bad(p+"/minLength", "minLength must be >= 0")
		}
		if prop.MaxLength != nil && *prop.MaxLength < 0 {
			bad(p+"/maxLength", "maxLength must be >= 0")
		}
		if prop.MinLength != nil && prop.MaxLength != nil && *prop.MinLength > *prop.MaxLength {
			bad(p+"/minLength", "minLength must be <= maxLength")
		}
	case "string-list":
		if prop.ItemFormat != "" && !slices.Contains([]string{"hostname", "ipv4", "ipv6", "cidr"}, prop.ItemFormat) {
			bad(p+"/itemFormat", "unsupported string-list itemFormat")
		}
		validateCardinality(prop, p, bad)
	case "boolean", "integer", "number":
		if prop.Minimum != nil && prop.Maximum != nil && *prop.Minimum > *prop.Maximum {
			bad(p+"/minimum", "minimum must be <= maximum")
		}
	case "enum":
		if len(prop.Options) == 0 {
			bad(p+"/options", "enum requires at least one option")
		}
		seenOpt := map[string]bool{}
		for i, opt := range prop.Options {
			op := fmt.Sprintf("%s/options/%d", p, i)
			if opt.Value == "" {
				bad(op+"/value", "enum option value must not be empty")
			} else if seenOpt[opt.Value] {
				bad(op+"/value", "duplicate enum option value")
			}
			seenOpt[opt.Value] = true
			if opt.Title.Empty() {
				bad(op+"/title", "enum option title is required")
			}
		}
		validateCardinality(prop, p, bad)
	case "secret-reference":
		if prop.Default != nil {
			bad(p+"/default", "secret-reference properties may not declare a default value")
		}
	case "zone-reference":
		if prop.Default != nil {
			bad(p+"/default", "zone-reference properties may not declare a default value")
		}
	case "certificate-reference", "alias-reference":
		bad(p+"/type", fmt.Sprintf("property type %q is reserved and not yet supported", prop.Type))
	default:
		bad(p+"/type", fmt.Sprintf("unsupported property type %q", prop.Type))
	}

	if prop.Default != nil {
		if defErrs := validateValue(prop, prop.Default, p+"/default", nil); len(defErrs) > 0 {
			for _, fe := range defErrs {
				bad(fe.Path, fe.Message)
			}
		}
	}
	if prop.Binding != nil {
		validateBinding(prop, p+"/binding", usedEnv, usedArg, usedSecretFile, bad)
	}
}

func validateCardinality(prop *Property, p string, bad func(string, string)) {
	if prop.MinItems != nil && *prop.MinItems < 0 {
		bad(p+"/minItems", "minItems must be >= 0")
	}
	if prop.MaxItems != nil && *prop.MaxItems < 1 {
		bad(p+"/maxItems", "maxItems must be >= 1")
	}
	if prop.MinItems != nil && prop.MaxItems != nil && *prop.MinItems > *prop.MaxItems {
		bad(p+"/minItems", "minItems must be <= maxItems")
	}
}

func validateBinding(prop *Property, p string, usedEnv, usedArg, usedSecretFile map[string]string, bad func(string, string)) {
	b := prop.Binding
	isMulti := prop.Type == "string-list" || (prop.Type == "enum" && (prop.MaxItems == nil || *prop.MaxItems > 1))
	switch b.Type {
	case "environment":
		if prop.Type == "secret-reference" {
			bad(p+"/type", `secret-reference must use "file" binding, not plaintext "environment"`)
			return
		}
		target := b.Name
		if target == "" {
			target = prop.Name
		}
		if !envNamePattern.MatchString(target) {
			bad(p+"/name", "invalid environment variable name")
		} else if strings.HasPrefix(target, "ANDARU_") {
			bad(p+"/name", "ANDARU_ prefix is reserved for platform-injected variables")
		} else if prev, dup := usedEnv[target]; dup {
			bad(p+"/name", fmt.Sprintf("environment variable %q already bound at %s", target, prev))
		} else {
			usedEnv[target] = p
		}
		if isMulti {
			if b.Encoding == nil || b.Encoding.Mode != "join" || b.Encoding.Separator == "" {
				bad(p+"/encoding", `multi-value environment binding requires encoding { "mode": "join", "separator": "..." }`)
			} else if prop.Type == "enum" {
				for _, opt := range prop.Options {
					if strings.Contains(opt.Value, b.Encoding.Separator) {
						bad(p+"/encoding/separator", fmt.Sprintf("enum option %q contains join separator %q", opt.Value, b.Encoding.Separator))
						break
					}
				}
			}
		} else if b.Encoding != nil {
			bad(p+"/encoding", "encoding is only valid for multi-value properties")
		}
	case "argument":
		if prop.Type == "secret-reference" {
			bad(p+"/type", `secret-reference must use "file" binding, not CLI "argument"`)
			return
		}
		if !argNamePattern.MatchString(b.Name) {
			bad(p+"/name", "invalid CLI argument name")
		} else if prev, dup := usedArg[b.Name]; dup {
			bad(p+"/name", fmt.Sprintf("CLI argument %q already bound at %s", b.Name, prev))
		} else {
			usedArg[b.Name] = p
		}
		if b.Style != "" {
			if b.Style != "flag" || prop.Type != "boolean" {
				bad(p+"/style", `style "flag" is only valid on boolean argument bindings`)
			}
		}
		if isMulti {
			if b.Encoding == nil || (b.Encoding.Mode != "join" && b.Encoding.Mode != "repeat") {
				bad(p+"/encoding", `multi-value argument binding requires encoding mode "join" or "repeat"`)
			} else if b.Encoding.Mode == "join" {
				if b.Encoding.Separator == "" {
					bad(p+"/encoding/separator", "join encoding requires a non-empty separator")
				} else if prop.Type == "enum" {
					for _, opt := range prop.Options {
						if strings.Contains(opt.Value, b.Encoding.Separator) {
							bad(p+"/encoding/separator", fmt.Sprintf("enum option %q contains join separator %q", opt.Value, b.Encoding.Separator))
							break
						}
					}
				}
			}
		} else if b.Encoding != nil {
			bad(p+"/encoding", "encoding is only valid for multi-value properties")
		}
	case "file":
		if prop.Type != "secret-reference" {
			bad(p+"/type", `file binding is only supported for secret-reference properties`)
			return
		}
		if !propNamePattern.MatchString(b.Name) {
			bad(p+"/name", "secret file binding name must match [a-zA-Z][a-zA-Z0-9_]{0,63}")
		} else if prev, dup := usedSecretFile[b.Name]; dup {
			bad(p+"/name", fmt.Sprintf("secret file %q already bound at %s", b.Name, prev))
		} else {
			usedSecretFile[b.Name] = p
		}
		if b.Format != "" && b.Format != "raw" && b.Format != "htpasswd" {
			bad(p+"/format", `file binding format must be "raw" or "htpasswd"`)
		}
		if b.Env != "" {
			if !envNamePattern.MatchString(b.Env) {
				bad(p+"/env", "invalid environment variable name")
			} else if strings.HasPrefix(b.Env, "ANDARU_") {
				bad(p+"/env", "ANDARU_ prefix is reserved for platform-injected variables")
			} else if prev, dup := usedEnv[b.Env]; dup {
				bad(p+"/env", fmt.Sprintf("environment variable %q already bound at %s", b.Env, prev))
			} else {
				usedEnv[b.Env] = p + "/env"
			}
		} else if b.Prefix != "" {
			bad(p+"/prefix", "prefix requires env to be set")
		}
	default:
		bad(p+"/type", `binding type must be "environment", "argument", or "file"`)
	}
}

func validateFormat(format, val string) string {
	switch format {
	case "":
		return ""
	case "hostname":
		if len(val) == 0 || len(val) > 253 || !hostnamePattern.MatchString(val) {
			return "invalid hostname"
		}
	case "uri":
		u, err := url.Parse(val)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return "invalid URI"
		}
		if u.User != nil {
			return "URI userinfo is not allowed; use a secret-reference property"
		}
	case "ipv4":
		a, err := netip.ParseAddr(val)
		if err != nil || !a.Is4() {
			return "invalid IPv4 address"
		}
	case "ipv6":
		a, err := netip.ParseAddr(val)
		if err != nil || !a.Is6() {
			return "invalid IPv6 address"
		}
	case "cidr":
		if _, err := netip.ParsePrefix(val); err != nil {
			return "invalid CIDR prefix"
		}
	case "date":
		if _, err := time.Parse("2006-01-02", val); err != nil {
			return "invalid ISO 8601 date (YYYY-MM-DD)"
		}
	case "date-time":
		if _, err := time.Parse(time.RFC3339, val); err != nil {
			return "invalid ISO 8601 date-time (RFC 3339)"
		}
	}
	return ""
}
