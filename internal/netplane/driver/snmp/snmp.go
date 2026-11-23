// SPDX-License-Identifier: AGPL-3.0-or-later
package snmp

import (
	"context"
	"fmt"
	"maps"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// Standard SNMP MIB OID roots.
const (
	oidSysDescr   = ".1.3.6.1.2.1.1.1.0"
	oidSysUpTime  = ".1.3.6.1.2.1.1.3.0"
	oidSysName    = ".1.3.6.1.2.1.1.5.0"
	oidIfTable    = ".1.3.6.1.2.1.2.2.1"
	oidIfXTable   = ".1.3.6.1.2.1.31.1.1.1"
	oidBasePortIf = ".1.3.6.1.2.1.17.1.4.1.2"
	oidDot1qFdb   = ".1.3.6.1.2.1.17.7.1.2.2.1.2"
	oidVlanFdbID  = ".1.3.6.1.2.1.17.7.1.4.2.1.3" // dot1qVlanFdbId.<timeMark>.<vlan> = fdbId
	oidLldpRem    = ".1.0.8802.1.1.2.1.4.1.1"
	oidLldpLoc    = ".1.0.8802.1.1.2.1.3.7.1"
	oidPethPort   = ".1.3.6.1.2.1.105.1.1.1"
)

// Walker abstracts SNMP subtree walking so tests can inject deterministic OID fixtures.
type Walker interface {
	Walk(ctx context.Context, rootOID string) ([]gosnmp.SnmpPDU, error)
	Close() error
}

// WalkerFactory constructs a Walker for a given target config; ctx bounds connection setup.
type WalkerFactory func(ctx context.Context, cfg driver.TargetConfig) (Walker, error)

// Driver polls managed switches and routers over SNMP v2c or SNMP v3.
type Driver struct {
	newWalker WalkerFactory
}

// New returns a new SNMP v2c/v3 driver using gosnmp.
func New() *Driver {
	return &Driver{newWalker: defaultWalkerFactory}
}

// NewWithFactory returns an SNMP driver using a custom WalkerFactory (for testing).
func NewWithFactory(f WalkerFactory) *Driver {
	return &Driver{newWalker: f}
}

// Kind returns "snmp".
func (d *Driver) Kind() string { return "snmp" }

// ValidateConfig validates SNMP v2c or v3 target settings and required secrets.
func (d *Driver) ValidateConfig(cfg driver.TargetConfig) error {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return fmt.Errorf("snmp target %q: endpoint (host[:port]) is required", cfg.ID)
	}
	if _, _, err := splitEndpoint(cfg.Endpoint); err != nil {
		return fmt.Errorf("snmp target %q: %w", cfg.ID, err)
	}
	ver := snmpVersion(cfg)
	switch ver {
	case "2c":
		if cfg.ResolvedSecrets["community"] == "" {
			return fmt.Errorf("snmp v2c target %q: community secret is required", cfg.ID)
		}
	case "3":
		user := cfg.StringSetting("username", cfg.ResolvedSecrets["username"])
		if user == "" {
			return fmt.Errorf("snmp v3 target %q: username setting or secret is required", cfg.ID)
		}
		ap := cfg.StringSetting("auth_protocol", "")
		if ap != "" && parseAuthProto(ap) == gosnmp.NoAuth {
			return fmt.Errorf("snmp v3 target %q: unsupported auth_protocol %q", cfg.ID, ap)
		}
		if ap != "" && cfg.ResolvedSecrets["auth_passphrase"] == "" {
			return fmt.Errorf("snmp v3 target %q: auth_passphrase secret is required for auth_protocol %q", cfg.ID, ap)
		}
		pp := cfg.StringSetting("priv_protocol", "")
		if pp != "" && parsePrivProto(pp) == gosnmp.NoPriv {
			return fmt.Errorf("snmp v3 target %q: unsupported priv_protocol %q", cfg.ID, pp)
		}
		if pp != "" && ap == "" {
			return fmt.Errorf("snmp v3 target %q: priv_protocol requires auth_protocol (USM has no privacy without authentication)", cfg.ID)
		}
		if pp != "" && cfg.ResolvedSecrets["priv_passphrase"] == "" {
			return fmt.Errorf("snmp v3 target %q: priv_passphrase secret is required for priv_protocol %q", cfg.ID, pp)
		}
	default:
		return fmt.Errorf("snmp target %q: unsupported version %q (supported: 2c, 3)", cfg.ID, ver)
	}
	return nil
}

// Poll walks IF-MIB, IF-X-MIB, Q-BRIDGE-MIB, LLDP-MIB, and POWER-ETHERNET-MIB.
func (d *Driver) Poll(ctx context.Context, cfg driver.TargetConfig) (*driver.Snapshot, error) {
	if err := d.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	w, err := d.newWalker(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("snmp connect %s: %w", cfg.Endpoint, err)
	}
	defer func() { _ = w.Close() }()

	sysPDUs, err := w.Walk(ctx, ".1.3.6.1.2.1.1")
	if err != nil {
		return nil, fmt.Errorf("snmp walk system: %w", err)
	}
	var sysDescr, sysName string
	var uptimeSec uint64
	for _, p := range sysPDUs {
		switch p.Name {
		case oidSysDescr:
			sysDescr = pduString(p)
		case oidSysName:
			sysName = pduString(p)
		case oidSysUpTime:
			uptimeSec = pduUint64(p) / 100
		}
	}

	ifPDUs, err := w.Walk(ctx, oidIfTable)
	if err != nil {
		return nil, fmt.Errorf("snmp walk ifTable: %w", err)
	}
	ifXPDUs, ifXErr := w.Walk(ctx, oidIfXTable)
	if ifXErr != nil {
		// ifName is the link identity (link names, counter history, FDB and LLDP ports): a failed
		// walk would rename every port, so fail the poll. A device without ifXTable walks empty.
		return nil, fmt.Errorf("snmp walk ifXTable: %w", ifXErr)
	}
	basePortPDUs, basePortErr := w.Walk(ctx, oidBasePortIf)
	fdbPDUs, fdbErr := w.Walk(ctx, oidDot1qFdb)
	vlanFdbPDUs, vlanFdbErr := w.Walk(ctx, oidVlanFdbID)
	lldpPDUs, lldpErr := w.Walk(ctx, oidLldpRem)
	lldpLocPDUs, lldpLocErr := w.Walk(ctx, oidLldpLoc)
	poePDUs, _ := w.Walk(ctx, oidPethPort)

	type ifAcc struct {
		idx       int
		descr     string
		ifType    uint64
		name      string
		alias     string
		mtu       int
		speed     uint64
		highSpeed uint64
		mac       string
		adminUp   bool
		operUp    bool
		inHC      uint64 // ifHCInOctets
		outHC     uint64 // ifHCOutOctets
		hasInHC   bool
		hasOutHC  bool
		// c holds the ifTable counters; the HC octets above replace its bytes when both exist.
		c model.PortCounters
	}
	ifaces := map[int]*ifAcc{}
	getIf := func(idx int) *ifAcc {
		if a, ok := ifaces[idx]; ok {
			return a
		}
		a := &ifAcc{idx: idx}
		ifaces[idx] = a
		return a
	}

	for _, p := range ifPDUs {
		col, idx, ok := parseTableOID(p.Name, oidIfTable)
		if !ok {
			continue
		}
		a := getIf(idx)
		switch col {
		case 2:
			a.descr = pduString(p)
		case 3:
			a.ifType = pduUint64(p)
		case 4:
			a.mtu = int(pduUint64(p))
		case 5:
			a.speed = pduUint64(p)
		case 6:
			a.mac = pduMAC(p)
		case 7:
			a.adminUp = pduUint64(p) == 1
		case 8:
			a.operUp = pduUint64(p) == 1
		case 10:
			a.c.RxBytes = pduUint64(p)
		case 13:
			a.c.RxDropped = pduUint64(p)
		case 14:
			a.c.RxErrors = pduUint64(p)
		case 16:
			a.c.TxBytes = pduUint64(p)
		case 19:
			a.c.TxDropped = pduUint64(p)
		case 20:
			a.c.TxErrors = pduUint64(p)
		}
	}

	for _, p := range ifXPDUs {
		col, idx, ok := parseTableOID(p.Name, oidIfXTable)
		if !ok {
			continue
		}
		a := getIf(idx)
		switch col {
		case 1:
			a.name = pduString(p)
		case 6:
			a.inHC, a.hasInHC = pduUint64(p), true
		case 7:
			a.c.RxPackets = pduUint64(p)
		case 10:
			a.outHC, a.hasOutHC = pduUint64(p), true
		case 11:
			a.c.TxPackets = pduUint64(p)
		case 15:
			a.highSpeed = pduUint64(p)
		case 18:
			a.alias = pduString(p)
		}
	}

	// Map bridge base port -> ifIndex.
	bridgePortToIf := map[int]int{}
	for _, p := range basePortPDUs {
		parts := strings.Split(strings.TrimPrefix(p.Name, oidBasePortIf+"."), ".")
		if len(parts) == 1 {
			if bp, err := strconv.Atoi(parts[0]); err == nil {
				bridgePortToIf[bp] = int(pduUint64(p))
			}
		}
	}

	// PoE status per pethPsePortTable index (group.port -> enabled/state).
	poeByKey := map[[2]int]*model.PortPoE{}
	for _, p := range poePDUs {
		rest := strings.TrimPrefix(p.Name, oidPethPort+".")
		parts := strings.Split(rest, ".")
		if len(parts) != 3 {
			continue
		}
		col, err1 := strconv.Atoi(parts[0])
		group, err2 := strconv.Atoi(parts[1])
		portIdx, err3 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		key := [2]int{group, portIdx}
		poe := poeByKey[key]
		if poe == nil {
			poe = &model.PortPoE{Supported: true}
			poeByKey[key] = poe
		}
		switch col {
		case 3: // pethPsePortAdminEnable (1=true, 2=false)
			poe.Enabled = pduUint64(p) == 1
		case 6: // pethPsePortDetectionStatus (3=deliveringPower)
			switch pduUint64(p) {
			case 1:
				poe.State = "disabled"
			case 2:
				poe.State = "searching"
			case 3:
				poe.State = "delivering"
			case 4:
				poe.State = "fault"
			}
		}
	}

	// POWER-ETHERNET-MIB defines no group.port -> ifIndex mapping. With a single PSE group,
	// treat pethPsePortIndex as the ifIndex (common on single-unit switches); with several
	// groups (stacks) the same port number is ambiguous, so attach nothing.
	poeByIf := map[int]*model.PortPoE{}
	groups := map[int]bool{}
	for k := range poeByKey {
		groups[k[0]] = true
	}
	if len(groups) == 1 {
		for k, poe := range poeByKey {
			poeByIf[k[1]] = poe
		}
	}

	idxs := slices.Sorted(maps.Keys(ifaces))

	var rawMACs []string
	ifIdxToName := map[int]string{}

	for _, idx := range idxs {
		a := ifaces[idx]
		ifName := a.name
		if ifName == "" {
			ifName = a.descr
		}
		if ifName == "" {
			ifName = ifIndexName(idx)
		}
		ifIdxToName[idx] = ifName
		rawMACs = append(rawMACs, a.mac)
	}
	macs, primaryMAC := driver.CollectMACs(rawMACs)

	now := time.Now().UTC()
	devID := "snmp:" + cfg.ID
	name := cfg.Name
	if name == "" {
		name = sysName
	}
	if name == "" {
		name = cfg.ID
	}
	host, _, err := net.SplitHostPort(cfg.Endpoint)
	if err != nil {
		host = cfg.Endpoint
	}

	caps := []model.Capability{model.CapLinks}
	if len(fdbPDUs) > 0 {
		caps = append(caps, model.CapFDB, model.CapVLANs)
	}
	if len(lldpPDUs) > 0 {
		caps = append(caps, model.CapLLDP)
	}
	if len(poeByKey) > 0 {
		caps = append(caps, model.CapPoE)
	}

	snap := &driver.Snapshot{
		TargetID:    cfg.ID,
		CollectedAt: now,
		Devices: []model.Device{{
			ID:           devID,
			Driver:       "snmp",
			TargetID:     cfg.ID,
			Name:         name,
			Hostname:     sysName,
			Model:        sysDescr,
			Role:         model.RoleSwitch,
			PrimaryMAC:   primaryMAC,
			MACs:         macs,
			MgmtIP:       host,
			UptimeSec:    uptimeSec,
			Online:       true,
			LastSeen:     now,
			Capabilities: caps,
		}},
	}
	// A failed walk (as opposed to a table the device lacks) keeps the last FDB data, including
	// the walks its port and VLAN mapping depends on. LLDP is marked after its ports resolve.
	if fdbErr != nil || basePortErr != nil || vlanFdbErr != nil {
		snap.MarkUnavailable(driver.SectionFDB)
	}

	for _, idx := range idxs {
		a := ifaces[idx]
		ifName := ifIdxToName[idx]
		if strings.EqualFold(ifName, "lo") || strings.EqualFold(a.descr, "loopback") {
			continue
		}
		speedMbps := int(a.highSpeed)
		if speedMbps == 0 && a.speed > 0 {
			speedMbps = int(a.speed / 1_000_000)
		}
		mtu := a.mtu
		media := guessSnmpMedia(ifName, a.ifType, speedMbps)
		// Report both directions at one width: the store compares a single width flag per link.
		hc := a.hasInHC && a.hasOutHC
		counters := a.c
		counters.Counter32 = !hc
		if hc {
			counters.RxBytes, counters.TxBytes = a.inHC, a.outHC
		}
		snap.Links = append(snap.Links, model.Link{
			DeviceID:  devID,
			Index:     idx,
			IfName:    ifName,
			Label:     a.alias,
			MAC:       a.mac,
			AdminUp:   a.adminUp,
			LinkUp:    a.adminUp && a.operUp,
			OperState: model.OperFromAdminLink(a.adminUp, a.operUp),
			SpeedMbps: speedMbps,
			MTU:       &mtu,
			Media:     media,
			PoE:       poeByIf[idx],
			Counters:  &counters,
		})
	}

	// dot1qTpFdbTable is indexed by dot1qFdbId, not VLAN: resolve it via dot1qVlanFdbId.
	// A database shared by several VLANs (SVL) cannot attribute a MAC to one VLAN.
	fdbVLANs := map[int]map[int]bool{}
	for _, p := range vlanFdbPDUs {
		parts := strings.Split(strings.TrimPrefix(p.Name, oidVlanFdbID+"."), ".")
		if len(parts) != 2 {
			continue
		}
		vlan, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		fid := int(pduUint64(p))
		if fdbVLANs[fid] == nil {
			fdbVLANs[fid] = map[int]bool{}
		}
		fdbVLANs[fid][vlan] = true
	}

	// Q-BRIDGE-MIB FDB: .1.3.6.1.2.1.17.7.1.2.2.1.2.<fdbId>.<m0>.<m1>.<m2>.<m3>.<m4>.<m5> = bridgePort
	for _, p := range fdbPDUs {
		rest := strings.TrimPrefix(p.Name, oidDot1qFdb+".")
		parts := strings.Split(rest, ".")
		if len(parts) != 7 {
			continue
		}
		fid, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		vlan := 0 // unknown
		if vs := fdbVLANs[fid]; len(vs) == 1 {
			for v := range vs {
				vlan = v
			}
		}
		b := make(net.HardwareAddr, 6)
		valid := true
		for i := range b {
			v, err := strconv.Atoi(parts[i+1])
			if err != nil || v < 0 || v > 255 {
				valid = false
				break
			}
			b[i] = byte(v)
		}
		if !valid {
			continue
		}
		bp := int(pduUint64(p))
		if bp == 0 { // port 0: location not learned
			continue
		}
		ifIdx := bp
		if mapped, ok := bridgePortToIf[bp]; ok {
			ifIdx = mapped
		}
		linkName := ifIdxToName[ifIdx]
		if linkName == "" {
			continue
		}
		snap.FDB = append(snap.FDB, model.FdbEntry{
			DeviceID: devID,
			LinkName: linkName,
			MAC:      b.String(),
			VLAN:     vlan,
		})
	}

	// lldpRemLocalPortNum is an lldpLocPortNum, not an ifIndex: resolve it via lldpLocPortTable
	// (portId/portDesc matched against ifName or ifDescr).
	nameToIf := map[string]string{}
	for _, idx := range idxs {
		if n := ifaces[idx].name; n != "" {
			nameToIf[n] = ifIdxToName[idx]
		}
	}
	for _, idx := range idxs {
		if d := ifaces[idx].descr; d != "" {
			if _, ok := nameToIf[d]; !ok {
				nameToIf[d] = ifIdxToName[idx]
			}
		}
	}
	locPortID := map[int]string{}
	locPortDesc := map[int]string{}
	for _, p := range lldpLocPDUs {
		col, lpNum, ok := parseTableOID(p.Name, oidLldpLoc)
		if !ok {
			continue
		}
		switch col {
		case 3:
			locPortID[lpNum] = pduString(p)
		case 4:
			locPortDesc[lpNum] = pduString(p)
		}
	}
	lldpNeededBridgePorts := false // a name fell through to the bridge-port mapping
	localPortName := func(lpNum int) string {
		if n := nameToIf[locPortID[lpNum]]; n != "" {
			return n
		}
		if n := nameToIf[locPortDesc[lpNum]]; n != "" {
			return n
		}
		lldpNeededBridgePorts = true
		if ifIdx, ok := bridgePortToIf[lpNum]; ok && ifIdxToName[ifIdx] != "" {
			return ifIdxToName[ifIdx]
		}
		if n := ifIdxToName[lpNum]; n != "" {
			return n
		}
		return ifIndexName(lpNum)
	}

	// LLDP-MIB: .1.0.8802.1.1.2.1.4.1.1.<col>.<timeMark>.<localPortNum>.<remIndex>
	type lldpKey struct{ localPort, remIdx string }
	lldpAcc := map[lldpKey]*model.LldpNeighbor{}
	for _, p := range lldpPDUs {
		rest := strings.TrimPrefix(p.Name, oidLldpRem+".")
		parts := strings.Split(rest, ".")
		if len(parts) < 4 {
			continue
		}
		col, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		k := lldpKey{localPort: parts[2], remIdx: parts[3]}
		n := lldpAcc[k]
		if n == nil {
			lpNum, _ := strconv.Atoi(parts[2])
			n = &model.LldpNeighbor{DeviceID: devID, LocalLink: localPortName(lpNum)}
			lldpAcc[k] = n
		}
		switch col {
		case 5: // lldpRemChassisId
			if mac := pduMAC(p); mac != "" {
				n.ChassisID = mac
			} else {
				n.ChassisID = pduString(p)
			}
		case 7: // lldpRemPortId
			n.RemotePort = pduString(p)
		case 9: // lldpRemSysName
			n.RemoteSysName = pduString(p)
		}
	}
	for _, n := range lldpAcc {
		snap.LLDP = append(snap.LLDP, *n)
	}

	// A failed base-port walk only matters for LLDP when a local port needed that mapping.
	if lldpErr != nil || lldpLocErr != nil || (basePortErr != nil && lldpNeededBridgePorts) {
		snap.MarkUnavailable(driver.SectionLLDP)
	}
	return snap, nil
}

type goSnmpWalker struct {
	g *gosnmp.GoSNMP
}

func (w *goSnmpWalker) Walk(ctx context.Context, rootOID string) ([]gosnmp.SnmpPDU, error) {
	w.g.Context = ctx
	return w.g.BulkWalkAll(rootOID)
}

func (w *goSnmpWalker) Close() error {
	if w.g.Conn != nil {
		return w.g.Conn.Close()
	}
	return nil
}

// splitEndpoint returns host and port, defaulting to 161 only when no port is given.
func splitEndpoint(endpoint string) (string, uint16, error) {
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		return endpoint, 161, nil
	}
	p, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || p == 0 {
		return "", 0, fmt.Errorf("invalid port %q in endpoint %q", portStr, endpoint)
	}
	return host, uint16(p), nil
}

func defaultWalkerFactory(ctx context.Context, cfg driver.TargetConfig) (Walker, error) {
	host, port, err := splitEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}

	g := &gosnmp.GoSNMP{
		Context:        ctx,
		Target:         host,
		Port:           port,
		Timeout:        5 * time.Second,
		Retries:        1,
		MaxRepetitions: 25,
	}

	if snmpVersion(cfg) == "3" {
		g.Version = gosnmp.Version3
		g.SecurityModel = gosnmp.UserSecurityModel
		authProto := parseAuthProto(cfg.StringSetting("auth_protocol", ""))
		privProto := parsePrivProto(cfg.StringSetting("priv_protocol", ""))
		flags := gosnmp.NoAuthNoPriv
		if authProto != gosnmp.NoAuth && privProto != gosnmp.NoPriv {
			flags = gosnmp.AuthPriv
		} else if authProto != gosnmp.NoAuth {
			flags = gosnmp.AuthNoPriv
		}
		g.MsgFlags = flags
		g.SecurityParameters = &gosnmp.UsmSecurityParameters{
			UserName:                 cfg.StringSetting("username", cfg.ResolvedSecrets["username"]),
			AuthenticationProtocol:   authProto,
			AuthenticationPassphrase: cfg.ResolvedSecrets["auth_passphrase"],
			PrivacyProtocol:          privProto,
			PrivacyPassphrase:        cfg.ResolvedSecrets["priv_passphrase"],
		}
	} else {
		g.Version = gosnmp.Version2c
		g.Community = cfg.ResolvedSecrets["community"]
	}

	if err := g.Connect(); err != nil {
		return nil, err
	}
	return &goSnmpWalker{g: g}, nil
}

func snmpVersion(cfg driver.TargetConfig) string {
	v := strings.ToLower(strings.TrimSpace(cfg.StringSetting("version", "2c")))
	if v == "2" {
		return "2c"
	}
	return v
}

func parseAuthProto(s string) gosnmp.SnmpV3AuthProtocol {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "MD5":
		return gosnmp.MD5
	case "SHA", "SHA1":
		return gosnmp.SHA
	case "SHA224":
		return gosnmp.SHA224
	case "SHA256":
		return gosnmp.SHA256
	case "SHA384":
		return gosnmp.SHA384
	case "SHA512":
		return gosnmp.SHA512
	default:
		return gosnmp.NoAuth
	}
}

func parsePrivProto(s string) gosnmp.SnmpV3PrivProtocol {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DES":
		return gosnmp.DES
	case "AES", "AES128":
		return gosnmp.AES
	case "AES192":
		return gosnmp.AES192
	case "AES256":
		return gosnmp.AES256
	default:
		return gosnmp.NoPriv
	}
}

func parseTableOID(oid, root string) (col, idx int, ok bool) {
	prefix := root + "."
	if !strings.HasPrefix(oid, prefix) {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(oid, prefix), ".")
	if len(parts) != 2 {
		return 0, 0, false
	}
	c, err1 := strconv.Atoi(parts[0])
	i, err2 := strconv.Atoi(parts[1])
	return c, i, err1 == nil && err2 == nil
}

func pduString(p gosnmp.SnmpPDU) string {
	switch v := p.Value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func pduMAC(p gosnmp.SnmpPDU) string {
	b, ok := p.Value.([]byte)
	if !ok || len(b) != 6 {
		return ""
	}
	return net.HardwareAddr(b).String()
}

func pduUint64(p gosnmp.SnmpPDU) uint64 {
	return gosnmp.ToBigInt(p.Value).Uint64()
}

// cageNames maps Cisco names of ports that are always a given cage to it, whatever the module
// or a down port reports as its speed.
var cageNames = []struct {
	re    *regexp.Regexp
	media model.MediaType
}{
	{regexp.MustCompile(`^(fiftygig|hundredgig|twohundredgig|fourhundredgig|hu\d)`), model.MediaQSFP28},
	{regexp.MustCompile(`^(fortygig|fo\d)`), model.MediaQSFPPl},
	{regexp.MustCompile(`^(twentyfivegig|twe\d)`), model.MediaSFP28},
}

// tenGigName matches TenGigabitEthernet and Ten-GigabitEthernet. The short form Te also names
// copper mGig ports, so it is left to the speed.
var tenGigName = regexp.MustCompile(`^ten-?gig`)

// ifTypeLag is IANAifType ieee8023adLag, which agents give link aggregates.
const ifTypeLag = 161

// lagName matches link aggregates by name for agents that give them another ifType (Cisco
// Port-channel, Po, Bundle-Ether and BE, Juniper ae, Linux bond and team, HPE Trk, Comware
// Bridge-Aggregation and Route-Aggregation, Huawei Eth-Trunk, and lag).
var lagName = regexp.MustCompile(`^(port-channel|po\d|bundle-ether|be\d|ae\d|bond|team|trk\d|bridge-aggregation|route-aggregation|eth-trunk|lag ?\d)`)

// guessSnmpMedia classifies an interface. A link aggregate's speed is the sum of its members',
// so it is other.
func guessSnmpMedia(ifName string, ifType uint64, speedMbps int) model.MediaType {
	lower := strings.ToLower(ifName)
	if ifType == ifTypeLag || lagName.MatchString(lower) {
		return model.MediaOther
	}
	for _, c := range cageNames {
		if c.re.MatchString(lower) {
			return c.media
		}
	}
	pluggable := strings.Contains(lower, "sfp") || strings.Contains(lower, "fiber") || tenGigName.MatchString(lower)
	// Agents report 10 Mb/s for virtual interfaces of unknown speed (net-snmp's default).
	if !pluggable && speedMbps < 100 {
		return model.MediaOther
	}
	return model.MediaForSpeed(speedMbps, pluggable)
}

// ifIndexName names an interface that has no ifName or ifDescr after its ifIndex.
func ifIndexName(idx int) string { return "if" + strconv.Itoa(idx) }
