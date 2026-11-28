package render

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/andaru-project/andaru/internal/addon"
)

const (
	addonQuadletDir       = "/run/containers/systemd/"
	addonRuntimeDir       = "/run/andaru-addons/"
	addonBusSocketHostDir = "/run/andaru-addons/bus-socket"
	addonBusSocketContDir = "/run/andaru/bus-socket"
	addonBusSocketCont    = "/run/andaru/bus-socket/bus.sock"
	addonBusTokenContDir  = "/run/andaru/bus"
	addonBusTokenCont     = "/run/andaru/bus/token"
	addonSecretsContDir   = "/run/andaru/secrets"
)

func addonContainerPath(slug string) string {
	return addonQuadletDir + "andaru-addon-" + slug + ".container"
}

func addonEnvPath(slug string) string {
	return addonRuntimeDir + slug + "/andaru-addon-" + slug + ".env"
}

func addonSecretFileName(slug, name string) string {
	return "andaru-addon-" + slug + "-" + name + ".secret"
}

func addonSecretHostPath(slug, name string) string {
	return addonRuntimeDir + slug + "/secrets/" + addonSecretFileName(slug, name)
}

func addonGrantedBusHash(g addonBusGrants) (string, bool) {
	var pub, sub []string
	for topic, state := range g.Publish {
		if state == "granted" {
			pub = append(pub, topic)
		}
	}
	for topic, state := range g.Subscribe {
		if state == "granted" {
			sub = append(sub, topic)
		}
	}
	if len(pub) == 0 && len(sub) == 0 {
		return "", false
	}
	slices.Sort(pub)
	slices.Sort(sub)
	h := sha256.New()
	for _, topic := range pub {
		h.Write([]byte("p:"))
		h.Write([]byte(topic))
		h.Write([]byte{0})
	}
	for _, topic := range sub {
		h.Write([]byte("s:"))
		h.Write([]byte(topic))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// addons renders Quadlet .container, .env, and .secret files for enabled add-ons, and emits
// blocking findings if Podman is absent, an enabled add-on's pinned digest is unstaged,
// a secret is unresolved, or any bound value contains unsafe control characters.
func (r *renderer) addons() {
	emittedMgmtRequired := false
	for i, a := range r.d.Addons {
		if !a.Enabled {
			continue
		}
		basePath := fmt.Sprintf("/addons/%d", i)
		if r.in.Catalog == nil {
			r.find("error", "ADDON_NOT_IN_CATALOG", basePath+"/id", fmt.Sprintf("Add-on %q cannot be realized because the catalog is unavailable.", a.ID))
			continue
		}
		m, ok := r.in.Catalog.ByID(a.ID)
		if !ok {
			r.find("error", "ADDON_NOT_IN_CATALOG", basePath+"/id", fmt.Sprintf("Add-on %q is not present in the shipped catalog.", a.ID))
			continue
		}
		if !r.in.Facts.PodmanTool {
			r.find("error", "TOOL_MISSING", basePath, "podman is not installed; add-ons require Podman.")
		}
		if r.in.Facts.StagedImagesErr != "" {
			r.find("error", "ADDON_STAGED_STATE_UNREADABLE", basePath, fmt.Sprintf("Staged add-on image index is unreadable: %s", r.in.Facts.StagedImagesErr))
		} else if !slices.Contains(r.in.Facts.StagedImages, m.Runtime.Image) {
			r.find("error", "ADDON_IMAGE_NOT_STAGED", basePath, fmt.Sprintf("Add-on %q image %s is not staged locally; stage the image before applying.", m.Name, m.Runtime.Image))
		}
		r.renderAddon(i, a, m, &emittedMgmtRequired)
	}
}

type renderedAddonSecret struct {
	hostPath string
	real     string
}

func hasUnsafeDirectiveChar(s string) bool {
	return strings.ContainsRune(s, 0) || strings.ContainsAny(s, "\r\n")
}

func (r *renderer) renderAddon(idx int, a addonEntry, m *addon.Manifest, emittedMgmtRequired *bool) {
	basePath := fmt.Sprintf("/addons/%d", idx)
	sources := []string{basePath}

	// Validate settings and required interface/filter invariants fail-closed at render/apply time so
	// restored or manually modified declarations can never bypass manifest constraints, while still
	// reporting all blocking findings together before refusing file emission.
	unsafe := false
	zones := make(map[string]bool, len(r.d.Zones))
	for _, z := range r.d.Zones {
		zones[z.ID] = true
	}
	settings, valErrs := addon.ValidateSettings(m, a.Settings, zones, nil)
	if len(valErrs) > 0 {
		for _, fe := range valErrs {
			r.find("error", "ADDON_SETTING_INVALID", basePath+"/settings"+fe.Path, fe.Message)
		}
		unsafe = true
	}
	boundZones := make(map[string]string, len(a.Interfaces))
	for _, ifc := range a.Interfaces {
		boundZones[ifc.Name] = ifc.Zone
	}
	needsFilter := false
	for _, spec := range m.Interfaces {
		if !spec.ZoneRequired {
			continue
		}
		needsFilter = true
		if z := boundZones[spec.Name]; z == "" || !zones[z] {
			r.find("error", "ADDON_INTERFACE_UNBOUND", basePath+"/interfaces", fmt.Sprintf("Interface %q requires a firewall zone when enabled.", spec.Name))
			unsafe = true
		}
	}
	if needsFilter && !r.d.filtered() {
		if !*emittedMgmtRequired {
			r.find("error", "MANAGEMENT_REQUIRED", "/management", "Add-ons with zone-bound interfaces require a management segment so Andaru's packet filter is active. Set management.segment.")
			*emittedMgmtRequired = true
		}
		unsafe = true
	}

	envMap := map[string]string{
		"ANDARU_ADDON_ID":   m.ID,
		"ANDARU_ADDON_SLUG": m.Slug,
	}
	for _, st := range m.Storage {
		envMap["ANDARU_STORAGE_"+strings.ToUpper(st.Name)] = st.MountPath
	}
	for k, v := range m.Runtime.Env {
		envMap[k] = v
	}

	var execArgs []string
	var secrets []renderedAddonSecret

	for _, prop := range m.Properties {
		val, present := settings[prop.Name]
		if !present {
			if prop.Default == nil {
				continue
			}
			val = prop.Default
		}
		propPath := basePath + "/settings/" + prop.Name

		if prop.Type == "secret-reference" {
			secMap, ok := val.(map[string]any)
			if !ok {
				r.find("error", "SECRET_UNRESOLVED", propPath, "Invalid add-on secret reference.")
				continue
			}
			secName, _ := secMap["secret"].(string)
			unresolved, _ := secMap["unresolved"].(bool)
			secVal, hasVal := r.in.AddonSecrets[a.ID][secName]
			if secName == "" || unresolved || !hasVal {
				r.find("error", "SECRET_UNRESOLVED", propPath, "The add-on secret reference is unresolved; set it before applying.")
				continue
			}
			if prop.Binding != nil && prop.Binding.Type == "file" {
				fileName := addonSecretFileName(m.Slug, prop.Binding.Name)
				contPath := addonSecretsContDir + "/" + fileName
				secrets = append(secrets, renderedAddonSecret{
					hostPath: addonSecretHostPath(m.Slug, prop.Binding.Name),
					real:     secVal,
				})
				envMap["ANDARU_SECRET_"+strings.ToUpper(prop.Binding.Name)+"_FILE"] = contPath
				if prop.Binding.Env != "" {
					envMap[prop.Binding.Env] = prop.Binding.Prefix + contPath
				}
			}
			continue
		}

		if prop.Binding == nil {
			continue
		}
		if !bindAddonProperty(prop, val, envMap, &execArgs) {
			r.find("error", "ADDON_SETTING_INVALID", propPath, "Add-on setting contains NUL bytes or newlines that cannot be rendered safely.")
			unsafe = true
		}
	}

	// Defense-in-depth: never emit Quadlet or env files if any env value or CLI word contains NUL or newlines.
	for _, k := range slices.Sorted(maps.Keys(envMap)) {
		if hasUnsafeDirectiveChar(k) || hasUnsafeDirectiveChar(envMap[k]) {
			r.find("error", "ADDON_SETTING_INVALID", basePath, fmt.Sprintf("Environment binding %q contains unsafe control characters.", k))
			unsafe = true
		}
	}
	var combinedExec []string
	if len(m.Runtime.Command) > 0 {
		if hasUnsafeDirectiveChar(m.Runtime.Command[0]) {
			r.find("error", "ADDON_SETTING_INVALID", basePath, "Add-on entrypoint contains unsafe control characters.")
			unsafe = true
		}
		combinedExec = append(combinedExec, m.Runtime.Command[1:]...)
	}
	combinedExec = append(combinedExec, m.Runtime.Args...)
	combinedExec = append(combinedExec, execArgs...)
	for _, arg := range combinedExec {
		if hasUnsafeDirectiveChar(arg) {
			r.find("error", "ADDON_SETTING_INVALID", basePath, "Add-on argument contains unsafe control characters.")
			unsafe = true
		}
	}
	if unsafe {
		return
	}

	busGrantsHash, hasBus := addonGrantedBusHash(a.BusGrants)
	if hasBus {
		envMap["ANDARU_BUS_SOCKET"] = addonBusSocketCont
		envMap["ANDARU_BUS_TOKEN_FILE"] = addonBusTokenCont
	}

	// Render secret files (0400, redacted in preview Content, raw value in Real).
	for _, sf := range secrets {
		r.result.Files = append(r.result.Files, File{
			Path:    sf.hostPath,
			Mode:    "0400",
			Secret:  true,
			Content: redacted + "\n",
			real:    sf.real,
			Sources: slices.Clone(sources),
		})
	}

	// Render Podman --env-file (0600, literal KEY=VALUE lines; Podman does not strip quotes).
	envPath := addonEnvPath(m.Slug)
	r.file(envPath, "0600", formatEnvFile(envMap), slices.Clone(sources))

	// Build Quadlet .container unit.
	var header strings.Builder
	if hasBus {
		// Embed a deterministic digest of granted bus topics so changing any grant changes the rendered unit hash
		// and restarts the add-on container cleanly on apply.
		fmt.Fprintf(&header, "# andaru-bus-grants: %s\n", busGrantsHash)
	}
	if m.Health != nil && m.Health.Endpoint != "" {
		if ei := slices.IndexFunc(m.Endpoints, func(ep addon.EndpointSpec) bool { return ep.Name == m.Health.Endpoint }); ei >= 0 {
			ep := m.Endpoints[ei]
			port, _ := m.EndpointPort(ep.Name, settings)
			expected := deref(m.Health.ExpectedStatus, 200)
			fmt.Fprintf(&header, "# andaru-health-url: %s://127.0.0.1:%d%s %d\n", ep.Protocol, port, m.Health.Path, expected)
			if m.Health.TimeoutSeconds > 0 {
				fmt.Fprintf(&header, "# andaru-health-timeout: %d\n", m.Health.TimeoutSeconds)
			}
			if m.Health.StartPeriodSeconds > 0 {
				fmt.Fprintf(&header, "# andaru-health-start-period: %d\n", m.Health.StartPeriodSeconds)
			}
		}
	}

	stateDir := cmp.Or(r.in.StateDir, "/var/lib/andaru")
	storageRoot := filepath.Join(stateDir, "addons") + "/"
	safeModeNext := filepath.Join(stateDir, "safe-mode-next")

	unitLines := []string{
		"Description=Andaru Add-on: " + sanitizeUnitLine(m.Name),
		"After=andaru-network-boot.service systemd-networkd.service andaru-engine.service",
		"ConditionPathExists=!/run/andaru-boot/safe-mode",
		"ConditionPathExists=!" + safeModeNext,
		"ConditionPathExists=!/run/andaru-boot/addons-held",
		"ConditionKernelCommandLine=!andaru.safe=1",
	}

	stopSec := deref(m.Runtime.StopTimeoutSeconds, 5)
	containerLines := []string{
		"Image=" + m.Runtime.Image,
		"Pull=never",
		"Network=host",
		"NoNewPrivileges=true",
		"DropCapability=ALL",
		fmt.Sprintf("StopTimeout=%d", stopSec),
	}
	for _, capName := range m.Runtime.Capabilities {
		containerLines = append(containerLines, "AddCapability="+capName)
	}
	for _, dev := range m.Runtime.Devices {
		containerLines = append(containerLines, "AddDevice="+dev)
	}
	for _, st := range m.Storage {
		opts := "Z,U"
		if st.ReadOnly {
			opts = "ro,Z,U"
		}
		hostDir := storageRoot + m.Slug + "/" + st.Name
		containerLines = append(containerLines, fmt.Sprintf("Volume=%s:%s:%s", hostDir, st.MountPath, opts))
	}
	if len(secrets) > 0 {
		containerLines = append(containerLines, fmt.Sprintf("Volume=%s%s/secrets:%s:ro,Z,U", addonRuntimeDir, m.Slug, addonSecretsContDir))
	}
	if hasBus {
		containerLines = append(containerLines, fmt.Sprintf("Volume=%s%s/bus:%s:ro,Z,U", addonRuntimeDir, m.Slug, addonBusTokenContDir))
		containerLines = append(containerLines, fmt.Sprintf("Volume=%s:%s:ro,z", addonBusSocketHostDir, addonBusSocketContDir))
	}
	containerLines = append(containerLines, "EnvironmentFile="+envPath)

	if len(m.Runtime.Command) > 0 {
		containerLines = append(containerLines, "Entrypoint="+m.Runtime.Command[0])
	}
	if len(combinedExec) > 0 {
		containerLines = append(containerLines, "Exec="+joinQuadletWords(combinedExec))
	}

	if m.Health != nil && len(m.Health.HealthCmd) > 0 {
		if rawCmd, err := json.Marshal(m.Health.HealthCmd); err == nil {
			containerLines = append(containerLines, "HealthCmd="+string(rawCmd))
		}
		if m.Health.IntervalSeconds > 0 {
			containerLines = append(containerLines, fmt.Sprintf("HealthInterval=%ds", m.Health.IntervalSeconds))
		}
		if m.Health.TimeoutSeconds > 0 {
			containerLines = append(containerLines, fmt.Sprintf("HealthTimeout=%ds", m.Health.TimeoutSeconds))
		}
		if m.Health.StartPeriodSeconds > 0 {
			containerLines = append(containerLines, fmt.Sprintf("HealthStartPeriod=%ds", m.Health.StartPeriodSeconds))
		}
	}

	serviceLines := []string{
		"Restart=on-failure",
		"RestartSec=5",
		// Allow 10s beyond Podman's StopTimeout so podman stop finishes container cleanup before systemd SIGKILLs it.
		fmt.Sprintf("TimeoutStopSec=%d", stopSec+10),
	}

	content := header.String() + ini(
		section{"Unit", unitLines},
		section{"Container", containerLines},
		section{"Service", serviceLines},
	)
	r.file(addonContainerPath(m.Slug), "0600", content, sources)
}

func bindAddonProperty(prop addon.Property, val any, envMap map[string]string, execArgs *[]string) bool {
	b := prop.Binding
	switch prop.Type {
	case "string", "zone-reference":
		s, ok := val.(string)
		if !ok {
			return true
		}
		if hasUnsafeDirectiveChar(s) {
			return false
		}
		emitScalarBinding(prop.Name, b, s, envMap, execArgs)
	case "boolean":
		bv, ok := val.(bool)
		if !ok {
			return true
		}
		if b.Type == "argument" && b.Style == "flag" {
			if bv {
				*execArgs = append(*execArgs, b.Name)
			}
			return true
		}
		emitScalarBinding(prop.Name, b, strconv.FormatBool(bv), envMap, execArgs)
	case "integer":
		iv, ok := addon.ToExactInt64(val)
		if !ok {
			return true
		}
		emitScalarBinding(prop.Name, b, strconv.FormatInt(iv, 10), envMap, execArgs)
	case "enum":
		if s, ok := val.(string); ok {
			if hasUnsafeDirectiveChar(s) {
				return false
			}
			emitScalarBinding(prop.Name, b, s, envMap, execArgs)
			return true
		}
		items, ok := addon.ToStringSlice(val)
		if !ok || len(items) == 0 {
			return true
		}
		for _, item := range items {
			if hasUnsafeDirectiveChar(item) {
				return false
			}
		}
		if prop.MaxItems != nil && *prop.MaxItems == 1 && len(items) == 1 {
			emitScalarBinding(prop.Name, b, items[0], envMap, execArgs)
			return true
		}
		emitListBinding(prop.Name, b, items, envMap, execArgs)
	case "string-list":
		items, ok := addon.ToStringSlice(val)
		if !ok || len(items) == 0 {
			return true
		}
		for _, item := range items {
			if hasUnsafeDirectiveChar(item) {
				return false
			}
		}
		emitListBinding(prop.Name, b, items, envMap, execArgs)
	}
	return true
}

func emitScalarBinding(propName string, b *addon.Binding, v string, envMap map[string]string, execArgs *[]string) {
	switch b.Type {
	case "environment":
		envMap[cmp.Or(b.Name, propName)] = v
	case "argument":
		*execArgs = append(*execArgs, b.Name, v)
	}
}

func emitListBinding(propName string, b *addon.Binding, items []string, envMap map[string]string, execArgs *[]string) {
	if b.Encoding == nil {
		return
	}
	switch b.Encoding.Mode {
	case "join":
		emitScalarBinding(propName, b, strings.Join(items, b.Encoding.Separator), envMap, execArgs)
	case "repeat":
		if b.Type != "argument" {
			return
		}
		for _, item := range items {
			emitScalarBinding(propName, b, item, envMap, execArgs)
		}
	}
}

// formatEnvFile renders sorted literal KEY=VALUE lines for Podman --env-file.
// Callers guarantee keys and values contain no NUL bytes or newlines.
func formatEnvFile(envMap map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(envMap)) {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(envMap[k])
		b.WriteByte('\n')
	}
	return b.String()
}

// joinQuadletWords quotes each argument word for Quadlet Exec= and escapes systemd % specifiers
// and $ variables so ExecStart= passes the literal values to the container.
func joinQuadletWords(words []string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = quoteOption(strings.NewReplacer("%", "%%", "$", "$$").Replace(w))
	}
	return strings.Join(quoted, " ")
}

func sanitizeUnitLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(s)
}
