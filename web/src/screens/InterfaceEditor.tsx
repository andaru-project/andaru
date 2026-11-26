import { Alert, Button, Drawer, Fieldset, Group, List, MultiSelect, NativeSelect, Select, Stack, Switch, Text, TextInput } from '@mantine/core';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { ApiError, saveFailure } from '../api';
import { bondModes, draftOf, interfaceErrors, interfaceOperations, ipv4Modes, ipv6Modes, memberLinkOptions, newZoneOption, removeOperations, vlanParentOptions, type InterfaceDraft } from '../interfaceEdit';
import type { BondMode, ConfigBatchOperation, LinkStatus, SavedLink, SavedObject, Segment, Snapshot, VirtualLinkKind } from '../types';
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
const newKindTitle: Record<VirtualLinkKind, string> = { vlan: 'Add VLAN', bridge: 'Add bridge', bond: 'Add bond' };

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
// What it does not show (router advertisements, DHCPv6 options) is kept as saved.
export function InterfaceEditor({ link, segment, live, newKind, snapshot, onClose, onSessionLost, onAbandoned, onSave }: InterfaceEditorProps) {
  const [draft, setDraft] = useState<InterfaceDraft>(() => draftOf(link?.value, segment?.value, live, newKind));
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState(false);
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
  // PPP is offered only where it can be: on a PPPoE link.
  const modes4 = ipv4Modes.filter((mode) => mode.value !== 'ppp' || draft.kind === 'pppoe');
  const parentOptions = draft.kind === 'vlan' ? vlanParentOptions(snapshot, link?.value.id, draft.parent) : [];
  const memberOptions = draft.kind === 'bridge' || draft.kind === 'bond' ? memberLinkOptions(snapshot, draft.kind, link?.value.id, draft.members) : [];
  const blockers = link ? removalBlockers(snapshot, link.value, segment?.value) : [];
  const dropsSegment = Boolean(segment) && !draft.assigned;

  // Checks the engine would make too, said before anything is sent.
  function check(): Map<string, string> {
    const found = new Map<string, string>();
    if (!draft.name.trim()) found.set('name', 'An interface needs a name.');
    const rawMtu = draft.mtu.trim();
    const mtu = Number(rawMtu);
    if (!/^\d+$/.test(rawMtu) || mtu < 68 || mtu > 65535) found.set('mtu', 'From 68 to 65535.');
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
    } else if (draft.kind === 'bond') {
      if (!draft.bondMode) found.set('bondMode', 'Choose a bond mode.');
      if (draft.members.length === 0) found.set('members', 'A bond needs at least one Ethernet member.');
    }
    if (!draft.assigned) return found;
    if (!draft.segmentName.trim()) found.set('segmentName', 'The segment needs a name.');
    if (!draft.zone) found.set('zone', 'Choose the zone this segment is in.');
    if (draft.zone === newZoneOption && !draft.newZone.trim()) found.set('newZone', 'Name the new zone.');
    if (!draft.ipv4Mode) found.set('ipv4', 'Choose how this segment gets its IPv4 address.');
    else if (draft.ipv4Mode === 'static' && !draft.ipv4Addresses.trim()) found.set('ipv4Addresses', 'A static address needs at least one address, such as 192.168.2.1/24.');
    if (!draft.ipv6Mode) found.set('ipv6', 'Choose how this segment gets its IPv6 address.');
    else if (draft.ipv6Mode === 'static' && !draft.ipv6Addresses.trim()) found.set('ipv6Addresses', 'A static address needs at least one address, such as 2001:db8::1/64.');
    else if (draft.ipv6Mode === 'dhcp6' && draft.ipv6PrefixLength.trim() && !/^([1-9]|[1-5][0-9]|6[0-4])$/.test(draft.ipv6PrefixLength.trim())) found.set('ipv6PrefixLength', 'A prefix length is a whole number from 1 to 64.');
    else if (draft.ipv6Mode === 'delegated') {
      if (!draft.ipv6From) found.set('ipv6From', 'Choose the segment whose prefix is delegated.');
      if (!draft.ipv6SubnetId.trim()) found.set('ipv6SubnetId', 'Give a subnet ID, such as 1.');
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
    void send(interfaceOperations(draft, link, segment, live));
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
                    <TextInput w="6rem" classNames={mono} label="MTU" inputMode="numeric" required value={draft.mtu} error={errors.get('mtu')} onChange={(event) => set({ mtu: event.currentTarget.value })} />
                  </Group>
                  {draft.kind === 'vlan' && (
                    <>
                      <Select label="Parent interface" placeholder="Choose…" required searchable allowDeselect={false} data={parentOptions} value={draft.parent || null} error={errors.get('parent')} nothingFoundMessage="No eligible interface by that name."
                        description={parentOptions.length === 0 ? 'No eligible non-PPPoE parent interface is available yet.' : 'The interface that carries tagged traffic for this VLAN.'}
                        onChange={(value) => {
                          const parentId = value ?? '';
                          const parent = snapshot.value.links.find((item) => item.id === parentId);
                          setDraft((current) => {
                            const prevParent = snapshot.value.links.find((item) => item.id === current.parent);
                            const prevDefault = String(Math.min(1500, prevParent?.mtu ?? 1500));
                            return {
                              ...current,
                              parent: parentId,
                              ...(!link && parent && current.mtu === prevDefault ? { mtu: String(Math.min(1500, parent.mtu)) } : {}),
                            };
                          });
                        }} />
                      <TextInput classNames={mono} label="VLAN ID" description="802.1Q tag from 1 to 4094." inputMode="numeric" required placeholder="20" value={draft.vlanId} error={errors.get('vlanId')} onChange={(event) => set({ vlanId: event.currentTarget.value })} />
                    </>
                  )}
                  {draft.kind === 'bridge' && (
                    <MultiSelect label="Member interfaces" searchable clearable clearButtonProps={{ 'aria-label': 'Clear member interfaces' }} data={memberOptions} value={draft.members} error={errors.get('members')} nothingFoundMessage="No eligible interface by that name."
                      description={memberOptions.length === 0 ? 'No unassigned eligible interfaces are available yet. A bridge can start empty.' : 'Unassigned Ethernet, VLAN, or bond interfaces bridged together. Leave empty for an isolated bridge.'}
                      onChange={(value) => set({ members: value })} />
                  )}
                  {draft.kind === 'bond' && (
                    <>
                      <Select label="Bond mode" required allowDeselect={false} data={bondModes} value={draft.bondMode || null} error={errors.get('bondMode')} onChange={(value) => set({ bondMode: (value ?? '') as BondMode | '' })} />
                      <MultiSelect label="Member interfaces" required searchable clearable clearButtonProps={{ 'aria-label': 'Clear member interfaces' }} data={memberOptions} value={draft.members} error={errors.get('members')} nothingFoundMessage="No eligible Ethernet port by that name."
                        description={memberOptions.length === 0 ? 'Add at least one unassigned Ethernet port without VLANs first.' : 'Unassigned Ethernet ports aggregated in this bond.'}
                        onChange={(value) => set({ members: value })} />
                    </>
                  )}
                  {/* A saved link shows the MAC it names, if any; a port being added, the one it will be saved with. */}
                  {mac && <Text size="sm" c="dimmed">{link?.value.kind ?? 'ethernet'} · <Text span ff="monospace" inherit>{mac}</Text></Text>}
                  <Switch label="Enabled" checked={draft.enabled} onChange={(event) => set({ enabled: event.currentTarget.checked })} />
                  {managed && !draft.enabled && <Alert color="red" role="alert">Disabled, this interface stops carrying the connection you manage Andaru through, until the apply rolls back.</Alert>}
                </Stack>
              </Fieldset>
              <Switch label="Carries a segment" checked={draft.assigned} disabled={managed || (Boolean(master) && !segment)} onChange={(event) => set({ assigned: event.currentTarget.checked })}
                description={assignedDescription} />
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
                      <NativeSelect label="IPv6 method" data={draft.ipv6Mode ? ipv6Modes : [placeholderOption, ...ipv6Modes]} value={draft.ipv6Mode} error={errors.get('ipv6')} onChange={(event) => set({ ipv6Mode: event.currentTarget.value })} />
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
