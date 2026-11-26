import { Alert, Button, Drawer, Fieldset, Group, List, MultiSelect, NativeSelect, PasswordInput, Select, SimpleGrid, Stack, Switch, Text, TextInput } from '@mantine/core';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { ApiError, saveFailure } from '../api';
import { bondModes, draftOf, hashPolicyModes, interfaceErrors, interfaceOperations, ipv4Modes, ipv6Modes, lacpRates, lldpEmitModes, lldpReceiveModes, memberLinkOptions, modeLabel, newZoneOption, raModeDescription, raModes, raPriorities, removeOperations, supportsRouterAdvertisements, transmitHashPolicies, validatePppoePassword, validatePppoeUsername, validatePref64, vlanParentOptions, type InterfaceDraft } from '../interfaceEdit';
import type { BondMode, ConfigBatchOperation, LacpRate, LinkStatus, LldpEmitMode, LldpReceiveMode, RaMode, RaPriority, SavedLink, SavedObject, Segment, Snapshot, TransmitHashPolicy, VirtualLinkKind } from '../types';
import { ZonePill } from './ZoneBadge';

interface InterfaceEditorProps {
  link?: SavedObject<SavedLink>;
  segment?: SavedObject<Segment>;
  live?: LinkStatus;
  newKind?: VirtualLinkKind;
  snapshot: Snapshot;
  onClose(): void;
  onSessionLost(): void;
  // A save failed after the panel was closed.
  onAbandoned(message: string): void;
  onSave(operations: ConfigBatchOperation[]): Promise<void>;
}

// Addresses, gateways and IDs read in a fixed-width face, as they do in the table, so 0 and O, 1 and l differ.
const mono = { input: 'mono-input' };
const placeholderOption = { value: '', label: 'Choose…', disabled: true };
const newKindTitle: Record<VirtualLinkKind, string> = { vlan: 'Add VLAN', bridge: 'Add bridge', bond: 'Add bond', pppoe: 'Add PPPoE' };

// Walk member links to the top-level master (e.g. ethernet -> bond -> bridge), guarded against cycles.
function topMasterOf(links: SavedLink[], id: string): SavedLink | undefined {
  let currentId = id;
  let master: SavedLink | undefined;
  const visited = new Set<string>([id]);
  while (true) {
    const next = links.find((item) => item.members?.includes(currentId));
    if (!next || visited.has(next.id)) return master;
    visited.add(next.id);
    master = next;
    currentId = next.id;
  }
}

// What keeps an interface from being removed, as far as the saved configuration says; the engine has the last word.
function removalBlockers(snapshot: Snapshot, link: SavedLink, segment: Segment | undefined) {
  const blockers: string[] = [];
  if (segment && snapshot.value.management?.segment === segment.id) blockers.push('You manage Andaru through this interface.');
  for (const other of snapshot.value.segments) {
    const from = (other.addressing.ipv6 as { from?: string }).from;
    if (segment && other.id !== segment.id && from === segment.id) blockers.push(`Segment ${other.name} takes its IPv6 prefix from this one.`);
  }
  for (const other of snapshot.value.links) {
    if (other.parent === link.id) blockers.push(other.kind === 'pppoe' ? `${other.name} is a PPPoE link on this interface.` : `${other.name} is a VLAN on this interface.`);
    if (other.members?.includes(link.id)) blockers.push(`This interface is a member of ${other.name}.`);
  }
  return blockers;
}

// The panel edits an interface as the configuration holds it: the link, and the segment that puts a network on it.
// What it does not show (DHCPv6 client options) is kept as saved.
export function InterfaceEditor({ link, segment, live, newKind, snapshot, onClose, onSessionLost, onAbandoned, onSave }: InterfaceEditorProps) {
  const [draft, setDraft] = useState<InterfaceDraft>(() => draftOf(link?.value, segment?.value, live, newKind));
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState(false);
  const mtuEdited = useRef(false);
  // A save may answer after this panel was closed and another opened: it then closes nothing.
  const open = useRef(true);
  useEffect(() => { open.current = true; return () => { open.current = false; }; }, []);
  const failureRef = useRef<HTMLDivElement>(null);
  // Remove… leaves the bar when a removal is blocked: the focus goes to Cancel, not to the page.
  const cancelRef = useRef<HTMLButtonElement>(null);
  useEffect(() => { if (failure) failureRef.current?.scrollIntoView?.({ block: 'nearest' }); }, [failure]);
  // A refused field may be scrolled out of view: the first one comes into view and takes the focus.
  const formRef = useRef<HTMLFormElement>(null);
  useEffect(() => {
    if (errors.size === 0) return;
    formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.scrollIntoView?.({ block: 'center' });
    formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]:not(:disabled)')?.focus();
  }, [errors]);

  const set = (next: Partial<InterfaceDraft>) => setDraft((current) => ({ ...current, ...next }));
  const managed = segment !== undefined && snapshot.value.management?.segment === segment.value.id;
  const master = link ? topMasterOf(snapshot.value.links, link.value.id) : undefined;
  const zones = [...snapshot.value.zones.map((zone) => ({ value: zone.id, label: zone.name })), { value: newZoneOption, label: 'New zone…' }];
  // A delegated prefix comes from another segment's DHCPv6 client.
  const delegators = snapshot.value.segments.filter((item) => item.id !== segment?.value.id && item.addressing.ipv6.mode === 'dhcp6').map((item) => ({ value: item.id, label: item.name }));
  // PPPoE segments negotiate IPv4 via PPP (or none) and IPv6 via SLAAC, DHCPv6, or none.
  const allowedModes4 = draft.kind === 'pppoe'
    ? ipv4Modes.filter((mode) => mode.value === 'ppp' || mode.value === 'none')
    : ipv4Modes.filter((mode) => mode.value !== 'ppp');
  const modes4 = draft.ipv4Mode && !allowedModes4.some((mode) => mode.value === draft.ipv4Mode)
    ? [{ value: draft.ipv4Mode, label: `${modeLabel(ipv4Modes, draft.ipv4Mode)} (unsupported)`, disabled: true }, ...allowedModes4]
    : allowedModes4;
  const allowedModes6 = draft.kind === 'pppoe'
    ? ipv6Modes.filter((mode) => mode.value === 'none' || mode.value === 'slaac' || mode.value === 'dhcp6')
    : ipv6Modes;
  const modes6 = draft.ipv6Mode && !allowedModes6.some((mode) => mode.value === draft.ipv6Mode)
    ? [{ value: draft.ipv6Mode, label: `${modeLabel(ipv6Modes, draft.ipv6Mode)} (unsupported on PPPoE)`, disabled: true }, ...allowedModes6]
    : allowedModes6;
  const parentOptions = draft.kind === 'vlan' || draft.kind === 'pppoe' ? vlanParentOptions(snapshot, link?.value.id, draft.parent) : [];
  const memberOptions = draft.kind === 'bridge' || draft.kind === 'bond' ? memberLinkOptions(snapshot, draft.kind, link?.value.id, draft.members) : [];
  const pppoeParentHasDhcp4 = draft.kind === 'pppoe' && Boolean(draft.parent)
    && snapshot.value.segments.some((item) => item.link === draft.parent && item.addressing.ipv4.mode === 'dhcp');
  const pppoePasswordUnresolved = draft.kind === 'pppoe' && Boolean(link?.value.password?.unresolved);
  const pppoePasswordRequired = draft.kind === 'pppoe' && (!link || (pppoePasswordUnresolved && draft.enabled));
  const blockers = link ? removalBlockers(snapshot, link.value, segment?.value) : [];
  const dropsSegment = Boolean(segment) && !draft.assigned;

  // Checks the engine would make too, said before anything is sent.
  function check(): Map<string, string> {
    const found = new Map<string, string>();
    if (!draft.name.trim()) found.set('name', 'An interface needs a name.');
    const rawMtu = draft.mtu.trim();
    const mtu = Number(rawMtu);
    if (!/^\d+$/.test(rawMtu) || mtu < 68 || mtu > 65535) found.set('mtu', 'From 68 to 65535.');
    const checkOptionalInt = (raw: string, min: number, max: number, field: string, message: string) => {
      const trimmed = raw.trim();
      if (!trimmed) return;
      const value = Number(trimmed);
      if (!/^\d+$/.test(trimmed) || value < min || value > max) found.set(field, message);
    };
    if (draft.kind === 'vlan') {
      const parent = snapshot.value.links.find((item) => item.id === draft.parent);
      if (!draft.parent) found.set('parent', 'Choose the parent interface.');
      else if (parent && !found.has('mtu') && mtu > parent.mtu) found.set('mtu', `Cannot exceed parent MTU (${parent.mtu}).`);
      const rawVlan = draft.vlanId.trim();
      const vlanId = Number(rawVlan);
      if (!/^\d+$/.test(rawVlan) || vlanId < 1 || vlanId > 4094) found.set('vlanId', 'A VLAN ID is a whole number from 1 to 4094.');
      else if (draft.parent) {
        const duplicate = snapshot.value.links.find((item) => item.kind === 'vlan' && item.id !== link?.value.id && item.parent === draft.parent && item.vlanId === vlanId);
        if (duplicate) found.set('vlanId', `VLAN ${vlanId} already exists on this parent (${duplicate.name}).`);
      }
    } else if (draft.kind === 'pppoe') {
      const parent = snapshot.value.links.find((item) => item.id === draft.parent);
      if (!draft.parent) found.set('parent', 'Choose the parent interface.');
      else if (parent && !found.has('mtu') && mtu > parent.mtu - 8) found.set('mtu', `Cannot exceed parent MTU minus 8 bytes for PPPoE (${parent.mtu - 8}).`);
      const usernameErr = validatePppoeUsername(draft.pppoeUsername);
      if (usernameErr) found.set('pppoeUsername', usernameErr);
      const passwordErr = validatePppoePassword(draft.pppoePassword, pppoePasswordRequired);
      if (passwordErr) found.set('pppoePassword', passwordErr);
    } else if (draft.kind === 'bridge' && draft.stp) {
      checkOptionalInt(draft.stpPriority, 0, 65535, 'stpPriority', 'From 0 to 65535.');
      checkOptionalInt(draft.forwardDelay, 2, 30, 'forwardDelay', 'From 2 to 30 seconds.');
      checkOptionalInt(draft.helloTime, 1, 10, 'helloTime', 'From 1 to 10 seconds.');
      checkOptionalInt(draft.maxAge, 6, 40, 'maxAge', 'From 6 to 40 seconds.');
      if (!found.has('forwardDelay') && !found.has('helloTime') && !found.has('maxAge') && (draft.forwardDelay.trim() || draft.helloTime.trim() || draft.maxAge.trim())) {
        const hello = draft.helloTime.trim() ? Number(draft.helloTime.trim()) : 2;
        const fwd = draft.forwardDelay.trim() ? Number(draft.forwardDelay.trim()) : 15;
        const maxAge = draft.maxAge.trim() ? Number(draft.maxAge.trim()) : 20;
        if (maxAge < 2 * (hello + 1)) found.set('maxAge', 'Max age must be at least 2 × (hello time + 1) seconds.');
        if (2 * (fwd - 1) < maxAge) found.set('forwardDelay', 'Forward delay must satisfy 2 × (forward delay − 1) ≥ max age.');
      }
    } else if (draft.kind === 'bond') {
      if (!draft.bondMode) found.set('bondMode', 'Choose a bond mode.');
      if (draft.members.length === 0) found.set('members', 'A bond needs at least one Ethernet member.');
      checkOptionalInt(draft.miiMonitorMs, 0, 60000, 'miiMonitorMs', 'From 0 to 60000 ms.');
    }
    if (!draft.assigned) return found;
    if (!draft.segmentName.trim()) found.set('segmentName', 'The segment needs a name.');
    if (!draft.zone) found.set('zone', 'Choose the zone this segment is in.');
    if (draft.zone === newZoneOption && !draft.newZone.trim()) found.set('newZone', 'Name the new zone.');
    if (!draft.ipv4Mode) found.set('ipv4', 'Choose how this segment gets its IPv4 address.');
    else if (!allowedModes4.some((mode) => mode.value === draft.ipv4Mode)) found.set('ipv4', draft.kind === 'pppoe' ? 'IPv4 on a PPPoE segment can be PPP or None.' : 'PPP is only supported on a PPPoE interface.');
    else if (draft.ipv4Mode === 'static' && !draft.ipv4Addresses.trim()) found.set('ipv4Addresses', 'A static address needs at least one address, such as 192.168.2.1/24.');
    if (!draft.ipv6Mode) found.set('ipv6', 'Choose how this segment gets its IPv6 address.');
    else if (!allowedModes6.some((mode) => mode.value === draft.ipv6Mode)) found.set('ipv6', 'IPv6 on a PPPoE segment can be None, SLAAC, or DHCPv6.');
    else if (draft.ipv6Mode === 'static' && !draft.ipv6Addresses.trim()) found.set('ipv6Addresses', 'A static address needs at least one address, such as 2001:db8::1/64.');
    else if (draft.ipv6Mode === 'dhcp6' && draft.ipv6PrefixLength.trim() && !/^([1-9]|[1-5][0-9]|6[0-4])$/.test(draft.ipv6PrefixLength.trim())) found.set('ipv6PrefixLength', 'A prefix length is a whole number from 1 to 64.');
    else if (draft.ipv6Mode === 'delegated') {
      if (!draft.ipv6From) found.set('ipv6From', 'Choose the segment whose prefix is delegated.');
      if (!draft.ipv6SubnetId.trim()) found.set('ipv6SubnetId', 'Give a subnet ID, such as 1.');
    }
    if (draft.kind === 'pppoe' && draft.ipv4Mode === 'none' && draft.ipv6Mode === 'none') {
      found.set('ipv4', 'A PPPoE segment needs IPv4 (PPP) or IPv6.');
    }
    if (draft.ipv6Mode && draft.ipv6Mode !== 'none' && !found.has('mtu') && mtu < 1280) {
      found.set('mtu', 'IPv6 requires an MTU of at least 1280.');
    }
    if (supportsRouterAdvertisements(draft.kind, draft.ipv6Mode) && draft.raMode !== 'disabled') {
      const rawRouterLife = draft.raRouterLifetime.trim();
      if (rawRouterLife) {
        const value = Number(rawRouterLife);
        if (!/^\d+$/.test(rawRouterLife) || (value !== 0 && (value < 4 || value > 9000))) {
          found.set('raRouterLifetime', 'Expected 0, or 4 to 9000 seconds.');
        }
      }
      if (draft.ipv6Mode === 'static' && draft.raMode !== 'router') {
        checkOptionalInt(draft.raValidLifetime, 1, 4294967294, 'raValidLifetime', 'Expected 1 to 4294967294 seconds.');
        checkOptionalInt(draft.raPreferredLifetime, 1, 4294967294, 'raPreferredLifetime', 'Expected 1 to 4294967294 seconds.');
        if (!found.has('raValidLifetime') && !found.has('raPreferredLifetime') && (draft.raValidLifetime.trim() || draft.raPreferredLifetime.trim())) {
          const valid = draft.raValidLifetime.trim() ? Number(draft.raValidLifetime.trim()) : 86400;
          const preferred = draft.raPreferredLifetime.trim() ? Number(draft.raPreferredLifetime.trim()) : 14400;
          if (preferred > valid) {
            found.set('raPreferredLifetime', 'Preferred lifetime cannot exceed valid lifetime.');
          }
        }
      }
      const pref64Err = validatePref64(draft.raPref64);
      if (pref64Err) found.set('raPref64', pref64Err);
      if (draft.raDnsOverride) {
        const raServers = draft.raDnsServers.split(/[\s,]+/).filter(Boolean);
        if (raServers.length > 8) found.set('raDnsServers', 'At most 8 IPv6 DNS servers.');
        else if (raServers.some((item) => !item.includes(':'))) found.set('raDnsServers', 'Router advertisements carry IPv6 DNS servers only.');
        const raDomains = draft.raDnsSearchDomains.split(/[\s,]+/).filter(Boolean);
        if (raDomains.length > 8) found.set('raDnsSearchDomains', 'At most 8 search domains.');
      }
    }
    return found;
  }

  async function send(operations: ConfigBatchOperation[]) {
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      if (operations.length > 0) await onSave(operations);
      if (open.current) onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      if (!open.current) return onAbandoned(saveFailure(error, new Map(), 'interface'));
      const fields = interfaceErrors(error, operations);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'interface'));
    } finally {
      setBusy(false);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = check();
    if (found.size > 0) {
      setErrors(found);
      setFailure('');
      return;
    }
    void send(interfaceOperations(draft, link, segment, live, snapshot));
  }

  function remove() {
    if (!link) return;
    if (!removing) {
      setRemoving(true);
      if (blockers.length > 0) requestAnimationFrame(() => cancelRef.current?.focus());
      return;
    }
    void send(removeOperations(link, segment));
  }

  const title = link ? `Edit interface: ${link.value.name}` : newKind ? newKindTitle[newKind] : `Add interface: ${live?.name ?? ''}`;
  const mac = link ? link.value.permanentMac : live?.permanentAddress;
  const assignedDescription = managed
    ? 'On, and it stays: you manage Andaru through this segment. Move management to another segment first.'
    : master && !segment
      ? `Off: this interface is a member of ${master.name}. Put the segment on ${master.name} instead.`
      : 'A segment is the network on this interface: its zone and addresses. Off for a port or bond that only carries VLANs or is a bridge member.';
  return (
    <Drawer opened onClose={onClose} position="right" size="md" title={title} lockScroll={false} closeButtonProps={{ 'aria-label': 'Close' }}>
      <form ref={formRef} onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert ref={failureRef} color="red" role="alert" title="The interface was not saved">{failure}</Alert>}
          {/* Focus starts on the warning, so whoever hears the page hears it before the fields. */}
          {managed && (
            <Alert color="yellow" title="You manage Andaru through this interface" tabIndex={-1} data-autofocus>
              A change here can cut this browser off. When you apply it, confirm from the new address in time; otherwise the apply rolls back on its own.
            </Alert>
          )}
          {!link && <Text size="sm" c="dimmed">Saving adds the {newKind ? 'virtual interface' : 'port'} to the configuration. Nothing changes on the box until the change is applied.</Text>}
          {/* While a removal waits for its confirmation, nothing else is edited. */}
          <Fieldset variant="unstyled" disabled={busy || removing}>
            <Stack>
              <Fieldset legend="Interface">
                <Stack gap="xs">
                  <Group align="flex-start" wrap="nowrap">
                    <TextInput className="grow" data-autofocus={managed ? undefined : true} label="Name" maxLength={128} required value={draft.name} error={errors.get('name')}
                      onChange={(event) => {
                        const name = event.currentTarget.value;
                        setDraft((current) => ({ ...current, name, ...(!link && !live && current.segmentName === current.name ? { segmentName: name } : {}) }));
                      }} />
                    <TextInput w="6rem" classNames={mono} label="MTU" inputMode="numeric" required value={draft.mtu} error={errors.get('mtu')} onChange={(event) => { mtuEdited.current = true; set({ mtu: event.currentTarget.value }); }} />
                  </Group>
                  {(draft.kind === 'vlan' || draft.kind === 'pppoe') && (
                    <Select label="Parent interface" placeholder="Choose…" required searchable allowDeselect={false} data={parentOptions} value={draft.parent || null} error={errors.get('parent')} nothingFoundMessage="No eligible interface by that name."
                      description={parentOptions.length === 0
                        ? 'No eligible non-PPPoE parent interface is available yet.'
                        : draft.kind === 'vlan'
                          ? 'The interface that carries tagged traffic for this VLAN.'
                          : 'The Ethernet, VLAN, bridge, or bond interface carrying PPPoE frames.'}
                      onChange={(value) => {
                        const parentId = value ?? '';
                        const parent = snapshot.value.links.find((item) => item.id === parentId);
                        const overhead = draft.kind === 'pppoe' ? 8 : 0;
                        setDraft((current) => ({
                          ...current,
                          parent: parentId,
                          ...(!link && !mtuEdited.current && parent ? { mtu: String(Math.min(1500 - overhead, Math.max(68, parent.mtu - overhead))) } : {}),
                        }));
                      }} />
                  )}
                  {draft.kind === 'vlan' && (
                    <TextInput classNames={mono} label="VLAN ID" description="802.1Q tag from 1 to 4094." inputMode="numeric" required placeholder="20" value={draft.vlanId} error={errors.get('vlanId')} onChange={(event) => set({ vlanId: event.currentTarget.value })} />
                  )}
                  {draft.kind === 'pppoe' && (
                    <>
                      {pppoeParentHasDhcp4 && (
                        <Alert color="yellow" role="status" title="Parent currently uses DHCPv4">
                          Switching a live DHCPv4 port directly to PPPoE in one apply currently rolls back. Remove DHCPv4 from the parent and apply first, or use a dedicated VLAN/port.
                        </Alert>
                      )}
                      <TextInput classNames={mono} label="PPPoE username" autoComplete="off" required value={draft.pppoeUsername} error={errors.get('pppoeUsername')} onChange={(event) => set({ pppoeUsername: event.currentTarget.value })} />
                      {pppoePasswordUnresolved && (
                        <Alert color="yellow" role="status" title="Password unresolved">
                          The stored secret (<Text span ff="monospace" inherit>{link?.value.password?.secret}</Text>) is missing after restore. Enter the PPPoE password to resolve it before applying.
                        </Alert>
                      )}
                      <PasswordInput classNames={mono} label="PPPoE password" autoComplete="new-password" required={pppoePasswordRequired}
                        placeholder={link && !pppoePasswordUnresolved ? 'Leave empty to keep saved password' : undefined}
                        description={link?.value.password?.isSet ? `Stored in secret ${link.value.password.secret}. Enter a new password only to replace it.` : undefined}
                        value={draft.pppoePassword} error={errors.get('pppoePassword')} onChange={(event) => set({ pppoePassword: event.currentTarget.value })} />
                    </>
                  )}
                  {draft.kind === 'bridge' && (
                    <>
                      <MultiSelect label="Member interfaces" searchable clearable clearButtonProps={{ 'aria-label': 'Clear member interfaces' }} data={memberOptions} value={draft.members} error={errors.get('members')} nothingFoundMessage="No eligible interface by that name."
                        description={memberOptions.length === 0 ? 'No unassigned eligible interfaces are available yet. A bridge can start empty.' : 'Unassigned Ethernet, VLAN, or bond interfaces bridged together. Leave empty for an isolated bridge.'}
                        onChange={(value) => set({ members: value })} />
                      <Switch label="Spanning Tree Protocol (STP)" description="Prevents Ethernet loops across bridged ports." checked={draft.stp} error={errors.get('stp')} onChange={(event) => set({ stp: event.currentTarget.checked })} />
                      {draft.stp && (
                        <SimpleGrid cols={{ base: 1, xs: 2 }} spacing="xs" role="group" aria-label="STP timers">
                          <TextInput classNames={mono} label="STP priority" description="Lower prefers root (0–65535, default 32768)." inputMode="numeric" placeholder="32768" value={draft.stpPriority} error={errors.get('stpPriority')} onChange={(event) => set({ stpPriority: event.currentTarget.value })} />
                          <TextInput classNames={mono} label="Forward delay (s)" description="2–30s (default 15)." inputMode="numeric" placeholder="15" value={draft.forwardDelay} error={errors.get('forwardDelay')} onChange={(event) => set({ forwardDelay: event.currentTarget.value })} />
                          <TextInput classNames={mono} label="Hello time (s)" description="1–10s (default 2)." inputMode="numeric" placeholder="2" value={draft.helloTime} error={errors.get('helloTime')} onChange={(event) => set({ helloTime: event.currentTarget.value })} />
                          <TextInput classNames={mono} label="Max age (s)" description="6–40s (default 20)." inputMode="numeric" placeholder="20" value={draft.maxAge} error={errors.get('maxAge')} onChange={(event) => set({ maxAge: event.currentTarget.value })} />
                        </SimpleGrid>
                      )}
                      <Switch label="IGMP/MLD multicast snooping" description="Limits multicast flooding to subscribed bridge ports." checked={draft.multicastSnooping} error={errors.get('multicastSnooping')} onChange={(event) => set({ multicastSnooping: event.currentTarget.checked })} />
                    </>
                  )}
                  {draft.kind === 'bond' && (
                    <>
                      <Select label="Bond mode" required allowDeselect={false} data={bondModes} value={draft.bondMode || null} error={errors.get('bondMode')} onChange={(value) => set({ bondMode: (value ?? '') as BondMode | '' })} />
                      <MultiSelect label="Member interfaces" required searchable clearable clearButtonProps={{ 'aria-label': 'Clear member interfaces' }} data={memberOptions} value={draft.members} error={errors.get('members')} nothingFoundMessage="No eligible Ethernet port by that name."
                        description={memberOptions.length === 0 ? 'Add at least one unassigned Ethernet port without VLANs first.' : 'Unassigned Ethernet ports aggregated in this bond.'}
                        onChange={(value) => set({ members: value })} />
                      {draft.bondMode === '802.3ad' && (
                        <NativeSelect label="LACP rate" description="Rate requested from the link partner for LACPDU packets." data={lacpRates} value={draft.lacpRate} error={errors.get('lacpRate')} onChange={(event) => set({ lacpRate: event.currentTarget.value as LacpRate | '' })} />
                      )}
                      {draft.bondMode && hashPolicyModes.has(draft.bondMode) && (
                        <NativeSelect label="Transmit hash policy" description="Packet header fields hashed to choose a member link." data={transmitHashPolicies} value={draft.transmitHashPolicy} error={errors.get('transmitHashPolicy')} onChange={(event) => set({ transmitHashPolicy: event.currentTarget.value as TransmitHashPolicy | '' })} />
                      )}
                      <TextInput classNames={mono} label="MII monitor interval (ms)" description="Link carrier check interval (0–60000 ms). 0 or empty disables MII monitoring; 100 ms is typical for LACP and failover." inputMode="numeric" placeholder="0" value={draft.miiMonitorMs} error={errors.get('miiMonitorMs')} onChange={(event) => set({ miiMonitorMs: event.currentTarget.value })} />
                    </>
                  )}
                  {draft.kind === 'ethernet' && (
                    <SimpleGrid cols={{ base: 1, xs: 2 }} spacing="xs" role="group" aria-label="LLDP">
                      <NativeSelect label="LLDP reception" description="Listen for neighbor advertisements." data={lldpReceiveModes} value={draft.lldp} error={errors.get('lldp')} onChange={(event) => set({ lldp: event.currentTarget.value as LldpReceiveMode })} />
                      <NativeSelect label="LLDP emission" description="Advertise this host over LLDP. Keep off on untrusted links." data={lldpEmitModes} value={draft.emitLldp} error={errors.get('emitLldp')} onChange={(event) => set({ emitLldp: event.currentTarget.value as LldpEmitMode })} />
                    </SimpleGrid>
                  )}
                  {/* A saved link shows the MAC it names, if any; a port being added, the one it will be saved with. */}
                  {mac && <Text size="sm" c="dimmed">{link?.value.kind ?? 'ethernet'} · <Text span ff="monospace" inherit>{mac}</Text></Text>}
                  <Switch label="Enabled" checked={draft.enabled} onChange={(event) => set({ enabled: event.currentTarget.checked })} />
                  {managed && !draft.enabled && <Alert color="red" role="alert">Disabled, this interface stops carrying the connection you manage Andaru through, until the apply rolls back.</Alert>}
                </Stack>
              </Fieldset>
              <Switch label="Carries a segment" checked={draft.assigned} disabled={managed || (Boolean(master) && !segment)} onChange={(event) => set({ assigned: event.currentTarget.checked })}
                description={assignedDescription} />
              {draft.kind === 'pppoe' && !draft.assigned && (
                <Alert color="yellow" role="status">
                  A PPPoE interface needs a segment to dial and negotiate IPv4 or IPv6.
                </Alert>
              )}
              {dropsSegment && (
                <Alert color="red" role="alert" title={`Saving removes the segment ${segment?.value.name}`}>
                  Its zone, IPv4 and IPv6 addresses, DNS and router advertisement settings go with it.
                </Alert>
              )}
              {draft.assigned && (
                <>
                  <Fieldset legend="Segment">
                    <Stack gap="xs">
                      <TextInput label="Segment name" maxLength={128} required value={draft.segmentName} error={errors.get('segmentName')} onChange={(event) => set({ segmentName: event.currentTarget.value })} />
                      <Select label="Zone" description="Firewall rules name zones, not interfaces." required data={zones} value={draft.zone || null} error={errors.get('zone')} allowDeselect={false}
                        renderOption={({ option }) => option.value === newZoneOption ? option.label : <ZonePill name={option.label} color={snapshot.value.zones.find((z) => z.id === option.value)?.color} />}
                        onChange={(value) => set({ zone: value ?? '' })} />
                      {draft.zone === newZoneOption && <TextInput label="New zone name" maxLength={128} required value={draft.newZone} error={errors.get('newZone')} onChange={(event) => set({ newZone: event.currentTarget.value })} />}
                    </Stack>
                  </Fieldset>
                  <Fieldset legend="IPv4">
                    <Stack gap="xs">
                      <NativeSelect label="IPv4 method" data={draft.ipv4Mode ? modes4 : [placeholderOption, ...modes4]} value={draft.ipv4Mode} error={errors.get('ipv4')} onChange={(event) => set({ ipv4Mode: event.currentTarget.value })} />
                      {draft.ipv4Mode === 'static' && (
                        <>
                          <TextInput classNames={mono} label="IPv4 addresses" description="With prefix length; separate several with commas." required placeholder="192.168.2.1/24" value={draft.ipv4Addresses} error={errors.get('ipv4Addresses')} onChange={(event) => set({ ipv4Addresses: event.currentTarget.value })} />
                          <TextInput classNames={mono} label="IPv4 gateway" description="The upstream router. Leave empty on an internal segment." placeholder="203.0.113.1" value={draft.ipv4Gateway} error={errors.get('ipv4Gateway')} onChange={(event) => set({ ipv4Gateway: event.currentTarget.value })} />
                        </>
                      )}
                    </Stack>
                  </Fieldset>
                  <Fieldset legend="IPv6">
                    <Stack gap="xs">
                      <NativeSelect label="IPv6 method" data={draft.ipv6Mode ? modes6 : [placeholderOption, ...modes6]} value={draft.ipv6Mode} error={errors.get('ipv6')} onChange={(event) => set({ ipv6Mode: event.currentTarget.value })} />
                      {draft.ipv6Mode === 'static' && (
                        <>
                          <TextInput classNames={mono} label="IPv6 addresses" description="With prefix length; separate several with commas." required placeholder="2001:db8::1/64" value={draft.ipv6Addresses} error={errors.get('ipv6Addresses')} onChange={(event) => set({ ipv6Addresses: event.currentTarget.value })} />
                          <TextInput classNames={mono} label="IPv6 gateway" description="The upstream router. Leave empty on an internal segment." placeholder="2001:db8::ffff" value={draft.ipv6Gateway} error={errors.get('ipv6Gateway')} onChange={(event) => set({ ipv6Gateway: event.currentTarget.value })} />
                        </>
                      )}
                      {draft.ipv6Mode === 'dhcp6' && (
                        <TextInput classNames={mono} label="Prefix length to request" description="Empty asks for an address only. 56 or 60 gives prefixes to delegate to other segments." inputMode="numeric" placeholder="56" value={draft.ipv6PrefixLength} error={errors.get('ipv6PrefixLength')} onChange={(event) => set({ ipv6PrefixLength: event.currentTarget.value })} />
                      )}
                      {draft.ipv6Mode === 'delegated' && (
                        <>
                          <Select label="Prefix from" required data={delegators} value={draft.ipv6From || null} error={errors.get('ipv6From')} disabled={delegators.length === 0}
                            description={delegators.length === 0 ? 'No segment has a DHCPv6 client yet: set one on the upstream segment first.' : 'A segment whose DHCPv6 client receives the prefix.'}
                            onChange={(value) => set({ ipv6From: value ?? '' })} />
                          <TextInput classNames={mono} label="Subnet ID" description="Hexadecimal, 1 to 4 digits: which /64 of the delegated prefix." required placeholder="1" value={draft.ipv6SubnetId} error={errors.get('ipv6SubnetId')} onChange={(event) => set({ ipv6SubnetId: event.currentTarget.value })} />
                          <TextInput classNames={mono} label="Static IPv6 addresses" description="Optional, beside the delegated prefix, such as a unique local address. The DNS service answers on the first." placeholder="fd00:1::1/64" value={draft.ipv6Addresses} error={errors.get('ipv6Addresses')} onChange={(event) => set({ ipv6Addresses: event.currentTarget.value })} />
                        </>
                      )}
                      {supportsRouterAdvertisements(draft.kind, draft.ipv6Mode) && (
                        <>
                          <NativeSelect
                            label="Router advertisements (RA)"
                            description={raModeDescription[draft.raMode]}
                            data={raModes}
                            value={draft.raMode}
                            error={errors.get('raMode')}
                            onChange={(event) => set({ raMode: event.currentTarget.value as RaMode })}
                          />
                          {draft.raMode !== 'disabled' && (
                            <Stack gap="xs" role="group" aria-label="Router advertisement options">
                              <SimpleGrid cols={{ base: 1, xs: 2 }} spacing="xs">
                                <NativeSelect
                                  label="Router priority"
                                  description="Default router preference (RFC 4191)."
                                  data={raPriorities}
                                  value={draft.raPriority}
                                  error={errors.get('raPriority')}
                                  onChange={(event) => set({ raPriority: event.currentTarget.value as '' | RaPriority })}
                                />
                                <TextInput
                                  classNames={mono}
                                  label="Router lifetime (s)"
                                  description="0 (not a default router) or 4–9000s (default 1800)."
                                  inputMode="numeric"
                                  placeholder="1800"
                                  value={draft.raRouterLifetime}
                                  error={errors.get('raRouterLifetime')}
                                  onChange={(event) => set({ raRouterLifetime: event.currentTarget.value })}
                                />
                              </SimpleGrid>
                              {draft.ipv6Mode === 'static' && draft.raMode !== 'router' && (
                                <SimpleGrid cols={{ base: 1, xs: 2 }} spacing="xs">
                                  <TextInput
                                    classNames={mono}
                                    label="Valid lifetime (s)"
                                    description="Prefix validity (default 86400s)."
                                    inputMode="numeric"
                                    placeholder="86400"
                                    value={draft.raValidLifetime}
                                    error={errors.get('raValidLifetime')}
                                    onChange={(event) => set({ raValidLifetime: event.currentTarget.value })}
                                  />
                                  <TextInput
                                    classNames={mono}
                                    label="Preferred lifetime (s)"
                                    description="Preferred duration ≤ valid (default 14400s)."
                                    inputMode="numeric"
                                    placeholder="14400"
                                    value={draft.raPreferredLifetime}
                                    error={errors.get('raPreferredLifetime')}
                                    onChange={(event) => set({ raPreferredLifetime: event.currentTarget.value })}
                                  />
                                </SimpleGrid>
                              )}
                              {draft.ipv6Mode === 'delegated' && draft.raMode !== 'router' && (
                                <Text size="xs" c="dimmed">Prefix valid and preferred lifetimes follow the upstream delegated lease.</Text>
                              )}
                              <TextInput
                                classNames={mono}
                                label="PREF64 prefix"
                                description="Optional NAT64 prefix (RFC 8781), length /32, /40, /48, /56, /64, or /96."
                                placeholder="64:ff9b::/96"
                                maxLength={64}
                                value={draft.raPref64}
                                error={errors.get('raPref64')}
                                onChange={(event) => set({ raPref64: event.currentTarget.value })}
                              />
                              <Switch
                                label="Custom RA DNS"
                                description={draft.raDnsOverride
                                  ? 'Advertise only the IPv6 DNS servers and search domains below (leave empty to advertise none).'
                                  : 'Off: automatically advertise Andaru DNS when enabled on this segment.'}
                                checked={draft.raDnsOverride}
                                onChange={(event) => set({ raDnsOverride: event.currentTarget.checked })}
                              />
                              {draft.raDnsOverride && (
                                <>
                                  <TextInput
                                    classNames={mono}
                                    label="RA DNS servers (RDNSS)"
                                    description="Up to 8 IPv6 addresses, separated by commas. Leave empty to advertise no DNS servers."
                                    placeholder="2606:4700:4700::1111, 2001:4860:4860::8888"
                                    value={draft.raDnsServers}
                                    error={errors.get('raDnsServers')}
                                    onChange={(event) => set({ raDnsServers: event.currentTarget.value })}
                                  />
                                  <TextInput
                                    classNames={mono}
                                    label="RA search domains (DNSSL)"
                                    description="Optional DNS search list (up to 8 domains), separated by commas."
                                    placeholder="home.arpa, lab.example"
                                    value={draft.raDnsSearchDomains}
                                    error={errors.get('raDnsSearchDomains')}
                                    onChange={(event) => set({ raDnsSearchDomains: event.currentTarget.value })}
                                  />
                                </>
                              )}
                            </Stack>
                          )}
                        </>
                      )}
                    </Stack>
                  </Fieldset>
                  <Fieldset legend="DNS">
                    <Stack gap="xs">
                      <TextInput classNames={mono} label="DNS servers" description="Resolvers for Andaru itself, on this segment. Empty uses those DHCP or router advertisements give, if any." placeholder="1.1.1.1, 2606:4700:4700::1111" value={draft.dnsServers} error={errors.get('dns')} onChange={(event) => set({ dnsServers: event.currentTarget.value })} />
                      <TextInput classNames={mono} label="Search domains" description={draft.dnsServers.trim() ? undefined : 'Name a DNS server first.'} disabled={!draft.dnsServers.trim()} placeholder="lan.example" value={draft.searchDomains} error={errors.get('searchDomains')} onChange={(event) => set({ searchDomains: event.currentTarget.value })} />
                    </Stack>
                  </Fieldset>
                </>
              )}
            </Stack>
          </Fieldset>
          <div className="drawer-actions">
            {/* The question sits by the buttons that answer it, wherever the form is scrolled. */}
            {removing && (
              <Alert color="red" role="alert" mb="sm" title={blockers.length > 0 ? 'This interface cannot be removed yet' : 'Remove this interface from the configuration?'}>
                {blockers.length > 0
                  ? <List size="sm">{blockers.map((blocker) => <List.Item key={blocker}>{blocker}</List.Item>)}</List>
                  : segment ? `Its segment ${segment.value.name} goes too. Rules and services that still use them keep it from being removed.` : 'Nothing changes on the box until the change is applied.'}
              </Alert>
            )}
            <Group justify="space-between">
              {link && !(removing && blockers.length > 0) ? <Button color="red" variant={removing ? 'filled' : 'subtle'} loading={busy && removing} onClick={remove}>{removing ? 'Remove it' : 'Remove…'}</Button> : <span />}
              <Group>
                <Button ref={cancelRef} variant="default" onClick={removing ? () => setRemoving(false) : onClose}>Cancel</Button>
                {!removing && <Button loading={busy} type="submit">Save interface</Button>}
              </Group>
            </Group>
          </div>
        </Stack>
      </form>
    </Drawer>
  );
}
