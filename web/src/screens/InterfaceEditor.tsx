import { Alert, Button, Drawer, Fieldset, Group, Select, Stack, Switch, Text, TextInput } from '@mantine/core';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { ApiError, saveFailure } from '../api';
import { draftOf, interfaceErrors, interfaceOperations, newZoneOption, removeOperations, type InterfaceDraft } from '../interfaceEdit';
import type { ConfigBatchOperation, LinkStatus, SavedLink, SavedObject, Segment, Snapshot } from '../types';
import { ZonePill } from './ZoneBadge';

interface InterfaceEditorProps {
  link?: SavedObject<SavedLink>;
  segment?: SavedObject<Segment>;
  live?: LinkStatus;
  snapshot: Snapshot;
  onClose(): void;
  onSessionLost(): void;
  // A save failed after the panel was closed.
  onAbandoned(message: string): void;
  onSave(operations: ConfigBatchOperation[]): Promise<void>;
}

const ipv4Modes = [{ value: 'none', label: 'None' }, { value: 'dhcp', label: 'DHCP client' }, { value: 'static', label: 'Static' }];
const ipv6Modes = [
  { value: 'none', label: 'None' }, { value: 'slaac', label: 'SLAAC' }, { value: 'dhcp6', label: 'DHCPv6 client' },
  { value: 'static', label: 'Static' }, { value: 'delegated', label: 'From a delegated prefix' },
];

// The panel edits an interface as the configuration holds it: the link, and the segment that puts a network on it.
// What it does not show (router advertisements, DHCPv6 options) is kept as saved.
export function InterfaceEditor({ link, segment, live, snapshot, onClose, onSessionLost, onAbandoned, onSave }: InterfaceEditorProps) {
  const [draft, setDraft] = useState<InterfaceDraft>(() => draftOf(link?.value, segment?.value, live));
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState(false);
  // A save may answer after this panel was closed and another opened: it then closes nothing.
  const open = useRef(true);
  useEffect(() => { open.current = true; return () => { open.current = false; }; }, []);
  const failureRef = useRef<HTMLDivElement>(null);
  useEffect(() => { if (failure) failureRef.current?.scrollIntoView?.({ block: 'nearest' }); }, [failure]);

  const set = (next: Partial<InterfaceDraft>) => setDraft((current) => ({ ...current, ...next }));
  const managed = segment !== undefined && snapshot.value.management?.segment === segment.value.id;
  const zones = [...snapshot.value.zones.map((zone) => ({ value: zone.id, label: zone.name })), { value: newZoneOption, label: 'New zone…' }];
  // A delegated prefix comes from another segment's DHCPv6 client.
  const delegators = snapshot.value.segments.filter((item) => item.id !== segment?.value.id && item.addressing.ipv6.mode === 'dhcp6').map((item) => ({ value: item.id, label: item.name }));
  const modes4 = link?.value.kind === 'pppoe' ? [...ipv4Modes, { value: 'ppp', label: 'PPP' }] : ipv4Modes;

  // Checks the engine would make too, said before anything is sent.
  function check(): Map<string, string> {
    const found = new Map<string, string>();
    if (!draft.name.trim()) found.set('name', 'An interface needs a name.');
    const mtu = Number(draft.mtu);
    if (!Number.isInteger(mtu) || mtu < 68 || mtu > 65535) found.set('mtu', 'The MTU is a whole number from 68 to 65535.');
    if (!draft.assigned) return found;
    if (!draft.segmentName.trim()) found.set('segmentName', 'The network needs a name.');
    if (!draft.zone) found.set('zone', 'Choose the zone this network is in.');
    if (draft.zone === newZoneOption && !draft.newZone.trim()) found.set('newZone', 'Name the new zone.');
    if (!draft.ipv4Mode) found.set('ipv4', 'Choose how this network gets its IPv4 address.');
    else if (draft.ipv4Mode === 'static' && !draft.ipv4Addresses.trim()) found.set('ipv4Addresses', 'A static address needs at least one address, such as 192.168.2.1/24.');
    if (!draft.ipv6Mode) found.set('ipv6', 'Choose how this network gets its IPv6 address.');
    else if (draft.ipv6Mode === 'static' && !draft.ipv6Addresses.trim()) found.set('ipv6Addresses', 'A static address needs at least one address, such as 2001:db8::1/64.');
    else if (draft.ipv6Mode === 'dhcp6' && draft.ipv6PrefixLength.trim() && !/^([1-9]|[1-5][0-9]|6[0-4])$/.test(draft.ipv6PrefixLength.trim())) found.set('ipv6PrefixLength', 'A prefix length is a whole number from 1 to 64.');
    else if (draft.ipv6Mode === 'delegated') {
      if (!draft.ipv6From) found.set('ipv6From', 'Choose the network whose prefix is delegated.');
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
    if (!removing) return setRemoving(true);
    void send(removeOperations(link, segment));
  }

  const title = link ? `Edit interface: ${link.value.name}` : `Add ${live?.name ?? 'interface'} to the configuration`;
  return (
    <Drawer opened onClose={onClose} position="right" size="md" title={title} lockScroll={false} closeButtonProps={{ 'aria-label': 'Close' }}>
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert ref={failureRef} color="red" role="alert" title="The interface was not saved">{failure}</Alert>}
          {managed && (
            <Alert color="yellow" title="You manage Andaru through this interface">
              A change here can cut this browser off. When you apply it, confirm from the new address in time; otherwise the apply rolls back on its own.
            </Alert>
          )}
          {!link && <Text size="sm" c="dimmed">Saving adds the port to the configuration. Nothing changes on the box until the change is applied.</Text>}
          <Fieldset variant="unstyled" disabled={busy}>
            <Stack>
              <Fieldset legend="Interface">
                <Stack gap="xs">
                  <TextInput data-autofocus label="Name" maxLength={128} required value={draft.name} error={errors.get('name')} onChange={(event) => set({ name: event.currentTarget.value })} />
                  {(live?.linkAddress || link?.value.permanentMac) && <Text size="sm" c="dimmed">{link?.value.kind ?? 'ethernet'} · <Text span ff="monospace" inherit>{link?.value.permanentMac ?? live?.linkAddress}</Text></Text>}
                  <Switch label="Enabled" checked={draft.enabled} onChange={(event) => set({ enabled: event.currentTarget.checked })} />
                  <TextInput label="MTU" inputMode="numeric" required value={draft.mtu} error={errors.get('mtu')} onChange={(event) => set({ mtu: event.currentTarget.value })} />
                </Stack>
              </Fieldset>
              <Switch label="Carries a network" description="Off for a port that only carries VLANs or is a bridge member." checked={draft.assigned}
                onChange={(event) => set({ assigned: event.currentTarget.checked })} />
              {draft.assigned && (
                <>
                  <Fieldset legend="Network">
                    <Stack gap="xs">
                      <TextInput label="Network name" maxLength={128} required value={draft.segmentName} error={errors.get('segmentName')} onChange={(event) => set({ segmentName: event.currentTarget.value })} />
                      <Select label="Zone" description="Firewall rules name zones, not interfaces." data={zones} value={draft.zone || null} error={errors.get('zone')} allowDeselect={false}
                        renderOption={({ option }) => option.value === newZoneOption ? option.label : <ZonePill name={option.label} />}
                        onChange={(value) => set({ zone: value ?? '' })} />
                      {draft.zone === newZoneOption && <TextInput label="New zone name" maxLength={128} required value={draft.newZone} error={errors.get('newZone')} onChange={(event) => set({ newZone: event.currentTarget.value })} />}
                    </Stack>
                  </Fieldset>
                  <Fieldset legend="IPv4">
                    <Stack gap="xs">
                      <Select label="IPv4 address" data={modes4} value={draft.ipv4Mode || null} error={errors.get('ipv4')} allowDeselect={false} onChange={(value) => set({ ipv4Mode: value ?? '' })} />
                      {draft.ipv4Mode === 'static' && (
                        <>
                          <TextInput label="IPv4 addresses" description="With prefix length; separate several with commas." placeholder="192.168.2.1/24" value={draft.ipv4Addresses} error={errors.get('ipv4Addresses')} onChange={(event) => set({ ipv4Addresses: event.currentTarget.value })} />
                          <TextInput label="IPv4 gateway" description="Only on the network that leads to the Internet." placeholder="203.0.113.1" value={draft.ipv4Gateway} error={errors.get('ipv4Gateway')} onChange={(event) => set({ ipv4Gateway: event.currentTarget.value })} />
                        </>
                      )}
                    </Stack>
                  </Fieldset>
                  <Fieldset legend="IPv6">
                    <Stack gap="xs">
                      <Select label="IPv6 address" data={ipv6Modes} value={draft.ipv6Mode || null} error={errors.get('ipv6')} allowDeselect={false} onChange={(value) => set({ ipv6Mode: value ?? '' })} />
                      {draft.ipv6Mode === 'static' && (
                        <>
                          <TextInput label="IPv6 addresses" description="With prefix length; separate several with commas." placeholder="2001:db8::1/64" value={draft.ipv6Addresses} error={errors.get('ipv6Addresses')} onChange={(event) => set({ ipv6Addresses: event.currentTarget.value })} />
                          <TextInput label="IPv6 gateway" placeholder="2001:db8::ffff" value={draft.ipv6Gateway} error={errors.get('ipv6Gateway')} onChange={(event) => set({ ipv6Gateway: event.currentTarget.value })} />
                        </>
                      )}
                      {draft.ipv6Mode === 'dhcp6' && (
                        <TextInput label="Prefix length to request" description="Empty asks for an address only. 56 or 60 gives prefixes to delegate to other networks." inputMode="numeric" placeholder="56" value={draft.ipv6PrefixLength} error={errors.get('ipv6PrefixLength')} onChange={(event) => set({ ipv6PrefixLength: event.currentTarget.value })} />
                      )}
                      {draft.ipv6Mode === 'delegated' && (
                        <>
                          <Select label="Prefix from" description="A network whose DHCPv6 client receives the prefix." data={delegators} value={draft.ipv6From || null} error={errors.get('ipv6From')} nothingFoundMessage="No network has a DHCPv6 client." onChange={(value) => set({ ipv6From: value ?? '' })} />
                          <TextInput label="Subnet ID" description="Hexadecimal, 1 to 4 digits: which /64 of the delegated prefix." placeholder="1" value={draft.ipv6SubnetId} error={errors.get('ipv6SubnetId')} onChange={(event) => set({ ipv6SubnetId: event.currentTarget.value })} />
                        </>
                      )}
                    </Stack>
                  </Fieldset>
                  <Fieldset legend="DNS">
                    <Stack gap="xs">
                      <TextInput label="DNS servers" description="For the box itself, on this network. Empty takes what DHCP gives." placeholder="1.1.1.1, 2606:4700:4700::1111" value={draft.dnsServers} error={errors.get('dns')} onChange={(event) => set({ dnsServers: event.currentTarget.value })} />
                      {draft.dnsServers.trim() && <TextInput label="Search domains" placeholder="lan.example" value={draft.searchDomains} error={errors.get('searchDomains')} onChange={(event) => set({ searchDomains: event.currentTarget.value })} />}
                    </Stack>
                  </Fieldset>
                </>
              )}
            </Stack>
          </Fieldset>
          {removing && <Alert color="red" title="Remove this interface from the configuration?">Its network goes too. Rules and services that still use them keep it from being removed.</Alert>}
          <Group justify="space-between" className="drawer-actions">
            {link ? <Button color="red" variant={removing ? 'filled' : 'subtle'} loading={busy && removing} onClick={remove}>{removing ? 'Remove it' : 'Remove…'}</Button> : <span />}
            <Group>
              <Button variant="default" onClick={removing ? () => setRemoving(false) : onClose}>Cancel</Button>
              {!removing && <Button loading={busy} type="submit">Save interface</Button>}
            </Group>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}
