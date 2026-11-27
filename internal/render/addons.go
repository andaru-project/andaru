package render

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/andaru-project/andaru/internal/addon"
)

const (
	addonQuadletDir       = "/run/containers/systemd/"
	addonRuntimeDir       = "/run/andaru-addons/"
	addonStorageDir       = "/var/lib/andaru/addons/"
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

func addonHasBusGrants(g addonBusGrants) bool {
	for _, state := range g.Publish {
		if state == "granted" {
			return true
		}
	}
	for _, state := range g.Subscribe {
		if state == "granted" {
			return true
		}
	}
	return false
}

// addons renders Quadlet .container, .env, and .secret files for enabled add-ons, and emits
// blocking findings if Podman is absent, an enabled add-on's pinned digest is unstaged, or a secret is unresolved.
func (r *renderer) addons() {
	if len(r.d.Addons) == 0 {
		return
	}
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
		if !slices.Contains(r.in.Facts.StagedImages, m.Runtime.Image) {
			r.find("error", "ADDON_IMAGE_NOT_STAGED", basePath, fmt.Sprintf("Add-on %q image %s is not staged locally; stage the image before applying.", m.Name, m.Runtime.Image))
		}
		r.renderAddon(i, a, m)
	}
}

type renderedAddonSecret struct {
	hostPath string
	real     string
}

func (r *renderer) renderAddon(idx int, a addonEntry, m *addon.Manifest) {
	basePath := fmt.Sprintf("/addons/%d", idx)
	sources := []string{basePath}

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
		val, present := a.Settings[prop.Name]
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
		r.bindAddonProperty(prop, val, propPath, envMap, &execArgs)
	}

	hasBus := addonHasBusGrants(a.BusGrants)
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

	// Render non-secret environment file (0600) when any env vars exist.
	envPath := ""
	if len(envMap) > 0 {
		envPath = addonEnvPath(m.Slug)
		r.file(envPath, "0600", formatEnvFile(envMap), slices.Clone(sources))
	}

	// Build Quadlet .container unit.
	var header strings.Builder
	if m.Health != nil && m.Health.Endpoint != "" {
		if ep, ok := findAddonEndpoint(m, m.Health.Endpoint); ok {
			port, _ := m.EndpointPort(ep.Name, a.Settings)
			expected := 200
			if m.Health.ExpectedStatus != nil {
				expected = *m.Health.ExpectedStatus
			}
			fmt.Fprintf(&header, "# andaru-health-url: %s://127.0.0.1:%d%s %d\n", ep.Protocol, port, m.Health.Path, expected)
			if m.Health.TimeoutSeconds > 0 {
				fmt.Fprintf(&header, "# andaru-health-timeout: %d\n", m.Health.TimeoutSeconds)
			}
			if m.Health.StartPeriodSeconds > 0 {
				fmt.Fprintf(&header, "# andaru-health-start-period: %d\n", m.Health.StartPeriodSeconds)
			}
		}
	}

	unitLines := []string{
		"Description=Andaru Add-on: " + sanitizeUnitLine(m.Name),
		"After=andaru-network-boot.service systemd-networkd.service andaru-engine.service",
		"ConditionPathExists=!/run/andaru-boot/safe-mode",
		"ConditionPathExists=!/var/lib/andaru/safe-mode-next",
		"ConditionPathExists=!/run/andaru-boot/addons-held",
		"ConditionKernelCommandLine=!andaru.safe=1",
	}

	containerLines := []string{
		"Image=" + m.Runtime.Image,
		"Pull=never",
		"Network=host",
		"NoNewPrivileges=true",
		"DropCapability=ALL",
	}
	for _, capName := range m.Runtime.Capabilities {
		containerLines = append(containerLines, "AddCapability="+capName)
	}
	for _, dev := range m.Runtime.Devices {
		containerLines = append(containerLines, "AddDevice="+dev)
	}
	for _, st := range m.Storage {
		opts := "Z"
		if st.ReadOnly {
			opts = "ro,Z"
		}
		hostDir := addonStorageDir + m.Slug + "/" + st.Name
		containerLines = append(containerLines, fmt.Sprintf("Volume=%s:%s:%s", hostDir, st.MountPath, opts))
	}
	if len(secrets) > 0 {
		containerLines = append(containerLines, fmt.Sprintf("Volume=%s%s/secrets:%s:ro,Z", addonRuntimeDir, m.Slug, addonSecretsContDir))
	}
	if hasBus {
		containerLines = append(containerLines, fmt.Sprintf("Volume=%s%s/bus:%s:ro,Z", addonRuntimeDir, m.Slug, addonBusTokenContDir))
		containerLines = append(containerLines, fmt.Sprintf("Volume=%s:%s", addonBusSocketHostDir, addonBusSocketContDir))
	}
	if envPath != "" {
		containerLines = append(containerLines, "EnvironmentFile="+envPath)
	}

	var combinedExec []string
	if len(m.Runtime.Command) > 0 {
		containerLines = append(containerLines, "Entrypoint="+quoteSystemdWord(m.Runtime.Command[0]))
		combinedExec = append(combinedExec, m.Runtime.Command[1:]...)
	}
	combinedExec = append(combinedExec, m.Runtime.Args...)
	combinedExec = append(combinedExec, execArgs...)
	if len(combinedExec) > 0 {
		quoted := make([]string, len(combinedExec))
		for i, arg := range combinedExec {
			quoted[i] = quoteSystemdWord(arg)
		}
		containerLines = append(containerLines, "Exec="+strings.Join(quoted, " "))
	}

	if m.Health != nil && len(m.Health.HealthCmd) > 0 {
		quotedCmd := make([]string, len(m.Health.HealthCmd))
		for i, arg := range m.Health.HealthCmd {
			quotedCmd[i] = quoteSystemdWord(arg)
		}
		containerLines = append(containerLines, "HealthCmd="+strings.Join(quotedCmd, " "))
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

	stopSec := 5
	if m.Runtime.StopTimeoutSeconds != nil {
		stopSec = *m.Runtime.StopTimeoutSeconds
	}
	serviceLines := []string{
		"Restart=on-failure",
		"RestartSec=5",
		fmt.Sprintf("TimeoutStopSec=%d", stopSec),
	}

	content := header.String() + ini(
		section{"Unit", unitLines},
		section{"Container", containerLines},
		section{"Service", serviceLines},
	)
	r.file(addonContainerPath(m.Slug), "0600", content, sources)
}

func (r *renderer) bindAddonProperty(prop addon.Property, val any, propPath string, envMap map[string]string, execArgs *[]string) {
	b := prop.Binding
	switch prop.Type {
	case "string", "zone-reference":
		s, ok := val.(string)
		if !ok {
			return
		}
		emitScalarBinding(b, s, envMap, execArgs)
	case "boolean":
		bv, ok := val.(bool)
		if !ok {
			return
		}
		if b.Type == "argument" && b.Style == "flag" {
			if bv {
				*execArgs = append(*execArgs, b.Name)
			}
			return
		}
		emitScalarBinding(b, strconv.FormatBool(bv), envMap, execArgs)
	case "integer":
		iv, ok := toAddonInt64(val)
		if !ok {
			return
		}
		emitScalarBinding(b, strconv.FormatInt(iv, 10), envMap, execArgs)
	case "enum":
		// Single-select enum (string or 1-element array) unwraps automatically.
		if s, ok := val.(string); ok {
			emitScalarBinding(b, s, envMap, execArgs)
			return
		}
		items, ok := toStringSlice(val)
		if !ok || len(items) == 0 {
			return
		}
		if prop.MaxItems != nil && *prop.MaxItems == 1 && len(items) == 1 {
			emitScalarBinding(b, items[0], envMap, execArgs)
			return
		}
		emitListBinding(b, items, envMap, execArgs)
	case "string-list":
		items, ok := toStringSlice(val)
		if !ok || len(items) == 0 {
			return
		}
		emitListBinding(b, items, envMap, execArgs)
	default:
		_ = propPath
	}
}

func emitScalarBinding(b *addon.Binding, v string, envMap map[string]string, execArgs *[]string) {
	switch b.Type {
	case "environment":
		envMap[b.Name] = v
	case "argument":
		*execArgs = append(*execArgs, b.Name, v)
	}
}

func emitListBinding(b *addon.Binding, items []string, envMap map[string]string, execArgs *[]string) {
	if b.Encoding == nil {
		return
	}
	switch b.Encoding.Mode {
	case "join":
		emitScalarBinding(b, strings.Join(items, b.Encoding.Separator), envMap, execArgs)
	case "repeat":
		if b.Type != "argument" {
			return
		}
		for _, item := range items {
			emitScalarBinding(b, item, envMap, execArgs)
		}
	}
}

func findAddonEndpoint(m *addon.Manifest, name string) (addon.EndpointSpec, bool) {
	for _, ep := range m.Endpoints {
		if ep.Name == name {
			return ep, true
		}
	}
	return addon.EndpointSpec{}, false
}

func toAddonInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n {
			return 0, false
		}
		return int64(n), true
	default:
		return 0, false
	}
}

func toStringSlice(v any) ([]string, bool) {
	switch s := v.(type) {
	case []string:
		return s, true
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			str, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, str)
		}
		return out, true
	default:
		return nil, false
	}
}

// formatEnvFile renders sorted KEY=VALUE lines suitable for systemd EnvironmentFile=.
func formatEnvFile(envMap map[string]string) string {
	keys := make([]string, 0, len(envMap))
	for k := range envMap {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(quoteEnvValue(envMap[k]))
		b.WriteByte('\n')
	}
	return b.String()
}

// quoteEnvValue quotes a systemd EnvironmentFile value when it contains whitespace or syntax-significant characters.
func quoteEnvValue(v string) string {
	if v == "" {
		return `""`
	}
	needsQuote := strings.ContainsAny(v, " \t\"'\\#;$")
	if !needsQuote {
		return v
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

// quoteSystemdWord quotes an argument word for Quadlet Exec=/Entrypoint=/HealthCmd= without shell interpolation,
// escaping backslashes, double quotes, systemd specifiers (%), and environment expansion ($).
func quoteSystemdWord(s string) string {
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`%`, `%%`,
		`$`, `$$`,
	).Replace(s)
	return `"` + escaped + `"`
}

func sanitizeUnitLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}
