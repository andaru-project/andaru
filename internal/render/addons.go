package render

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
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
// blocking findings if Podman is absent, an enabled add-on's pinned digest is unstaged,
// a secret is unresolved, or any bound value contains unsafe control characters.
func (r *renderer) addons() {
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

func hasUnsafeDirectiveChar(s string) bool {
	return strings.ContainsRune(s, 0) || strings.ContainsAny(s, "\r\n")
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
	unsafe := false

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

	// Render Podman --env-file (0600, literal KEY=VALUE lines; Podman does not strip quotes).
	envPath := addonEnvPath(m.Slug)
	r.file(envPath, "0600", formatEnvFile(envMap), slices.Clone(sources))

	// Build Quadlet .container unit.
	var header strings.Builder
	if m.Health != nil && m.Health.Endpoint != "" {
		if ei := slices.IndexFunc(m.Endpoints, func(ep addon.EndpointSpec) bool { return ep.Name == m.Health.Endpoint }); ei >= 0 {
			ep := m.Endpoints[ei]
			port, _ := m.EndpointPort(ep.Name, a.Settings)
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
		opts := "Z,U"
		if st.ReadOnly {
			opts = "ro,Z,U"
		}
		hostDir := addonStorageDir + m.Slug + "/" + st.Name
		containerLines = append(containerLines, fmt.Sprintf("Volume=%s:%s:%s", hostDir, st.MountPath, opts))
	}
	if len(secrets) > 0 {
		containerLines = append(containerLines, fmt.Sprintf("Volume=%s%s/secrets:%s:ro,Z,U", addonRuntimeDir, m.Slug, addonSecretsContDir))
	}
	if hasBus {
		containerLines = append(containerLines, fmt.Sprintf("Volume=%s%s/bus:%s:ro,Z,U", addonRuntimeDir, m.Slug, addonBusTokenContDir))
		containerLines = append(containerLines, fmt.Sprintf("Volume=%s:%s:z", addonBusSocketHostDir, addonBusSocketContDir))
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
		fmt.Sprintf("TimeoutStopSec=%d", deref(m.Runtime.StopTimeoutSeconds, 5)),
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

// joinQuadletWords quotes each argument word for Quadlet Exec= (Quadlet parses double-quoted
// words and performs systemd specifier/variable escaping when generating ExecStart=).
func joinQuadletWords(words []string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = quoteOption(w)
	}
	return strings.Join(quoted, " ")
}

func sanitizeUnitLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(s)
}
