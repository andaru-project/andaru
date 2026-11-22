import { Alert, Button, Drawer, Group, MultiSelect, NativeSelect, Stack, Textarea, TextInput } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { ApiError, fieldErrors, saveFailure } from '../api';
import type { AddressAlias, AliasKinds, PortAlias, SavedObject } from '../types';
import type { Row } from './AliasesScreen';

interface AliasEditorProps {
  // A row to edit, or the kind of alias to add.
  editing: Row | keyof AliasKinds;
  rows: Row[];
  onClose(): void;
  onSessionLost(): void;
  onSave<K extends keyof AliasKinds>(kind: K, alias: Omit<AliasKinds[K], 'id'>, saved?: SavedObject<AliasKinds[K]>): Promise<void>;
}

// The types an ICMP alias may name, as the engine knows them.
const icmpTypes = {
  icmp: ['echo-request', 'echo-reply', 'destination-unreachable', 'time-exceeded', 'parameter-problem', 'timestamp-request', 'timestamp-reply'],
  icmpv6: ['echo-request', 'echo-reply', 'destination-unreachable', 'packet-too-big', 'time-exceeded', 'parameter-problem'],
};

const field = /^\/(name|description|kind|protocol|address|prefix|first|last|members|ports|types)(?:\/|$)/;

export function AliasEditor({ editing, rows, onClose, onSessionLost, onSave }: AliasEditorProps) {
  const kind = typeof editing === 'string' ? editing : editing.kind;
  const saved = typeof editing === 'string' ? undefined : editing;
  const savedAddress = saved?.kind === 'addresses' ? saved.item : undefined;
  const savedPort = saved?.kind === 'ports' ? saved.item : undefined;
  const address = savedAddress?.value;
  const port = savedPort?.value;
  const noun = kind === 'addresses' ? 'address alias' : 'port alias';
  const title = saved ? `Edit ${noun} ${saved.item.value.name}` : `Add ${noun}`;
  const [name, setName] = useState(saved?.item.value.name ?? '');
  const [description, setDescription] = useState(saved?.item.value.description ?? '');
  const [what, setWhat] = useState<string>(address?.kind ?? port?.protocol ?? (kind === 'addresses' ? 'host' : 'tcp'));
  const [value, setValue] = useState(address?.address ?? address?.prefix ?? address?.first ?? '');
  const [last, setLast] = useState(address?.last ?? '');
  const [ports, setPorts] = useState((port?.ports ?? []).join(', '));
  const [types, setTypes] = useState(port?.types ?? []);
  const [members, setMembers] = useState(saved?.item.value.members ?? []);
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);

  // A group holds aliases of its own kind that are not groups, and never itself.
  const candidates = rows.filter((row) => row.kind === kind && row.item.value.id !== saved?.item.value.id && (row.kind === 'addresses' ? row.item.value.kind : row.item.value.protocol) !== 'group')
    .map((row) => ({ value: row.item.value.id, label: row.item.value.name }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure('');
    if (!name.trim()) {
      setErrors(new Map([['name', 'An alias needs a name.']]));
      return;
    }
    // The engine refuses a name another alias of the kind has, and may blame the other one by its ID.
    if (rows.some((row) => row.kind === kind && row.item.value.id !== saved?.item.value.id && row.item.value.name === name.trim())) {
      setErrors(new Map([['name', `Another ${noun} has this name.`]]));
      return;
    }
    setBusy(true);
    setErrors(new Map());
    const identity = { name: name.trim(), ...(description ? { description } : {}) };
    try {
      if (kind === 'addresses') {
        const body: Omit<AddressAlias, 'id'> = { ...identity, kind: what as AddressAlias['kind'],
          ...(what === 'host' ? { address: value.trim() } : what === 'network' ? { prefix: value.trim() } : what === 'range' ? { first: value.trim(), last: last.trim() } : { members }) };
        await onSave('addresses', body, savedAddress);
      } else {
        const body: Omit<PortAlias, 'id'> = { ...identity, protocol: what as PortAlias['protocol'],
          ...(what === 'group' ? { members } : what === 'tcp' || what === 'udp' ? { ports: ports.replace(/\s*-\s*/g, '-').split(/[\s,]+/).filter(Boolean) } : types.length > 0 ? { types } : {}) };
        await onSave('ports', body, savedPort);
      }
      onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      const fields = fieldErrors(error, field);
      setErrors(fields);
      setFailure(saveFailure(error, fields, noun));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Drawer opened onClose={onClose} position="right" size="md" title={title} lockScroll={false} closeButtonProps={{ 'aria-label': 'Close', disabled: busy }} closeOnEscape={!busy} closeOnClickOutside={!busy}>
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert color="red" role="alert" title="The alias was not saved">{failure}</Alert>}
          <TextInput data-autofocus label="Name" maxLength={128} required value={name} error={errors.get('name')} onChange={(event) => setName(event.currentTarget.value)} />
          <Textarea autosize label="Description" maxLength={4096} minRows={1} maxRows={4} value={description} error={errors.get('description')} onChange={(event) => setDescription(event.currentTarget.value)} />
          {kind === 'addresses' ? (
            <>
              <NativeSelect label="Kind" value={what} error={errors.get('kind')} onChange={(event) => setWhat(event.currentTarget.value)}
                data={[{ value: 'host', label: 'One address' }, { value: 'network', label: 'A network' }, { value: 'range', label: 'A range of addresses' }, { value: 'group', label: 'A group of address aliases' }]} />
              {what === 'host' && <TextInput label="Address" description="IPv4 or IPv6." placeholder="192.168.1.10" value={value} error={errors.get('address')} onChange={(event) => setValue(event.currentTarget.value)} />}
              {what === 'network' && <TextInput label="Network" description="A prefix with its length." placeholder="192.168.1.0/24" value={value} error={errors.get('prefix')} onChange={(event) => setValue(event.currentTarget.value)} />}
              {what === 'range' && (
                <>
                  <TextInput label="First address" value={value} error={errors.get('first')} onChange={(event) => setValue(event.currentTarget.value)} />
                  <TextInput label="Last address" description="Of the same family, not before the first." value={last} error={errors.get('last')} onChange={(event) => setLast(event.currentTarget.value)} />
                </>
              )}
            </>
          ) : (
            <>
              <NativeSelect label="Protocol" value={what} error={errors.get('protocol')} onChange={(event) => { setWhat(event.currentTarget.value); setTypes([]); }}
                data={[{ value: 'tcp', label: 'TCP' }, { value: 'udp', label: 'UDP' }, { value: 'icmp', label: 'ICMP (IPv4)' }, { value: 'icmpv6', label: 'ICMPv6' }, { value: 'group', label: 'A group of port aliases' }]} />
              {(what === 'tcp' || what === 'udp') && <TextInput label="Ports" description="Ports and ranges, separated by commas: 80, 443, 8000-8100." value={ports} error={errors.get('ports')} onChange={(event) => setPorts(event.currentTarget.value)} />}
              {(what === 'icmp' || what === 'icmpv6') && <MultiSelect label="Types" description="Empty is every type." data={icmpTypes[what]} value={types} error={errors.get('types')} clearable onChange={setTypes} />}
            </>
          )}
          {what === 'group' && <MultiSelect label="Members" description="Aliases of the same kind that are not groups themselves." data={candidates} value={members} error={errors.get('members')} searchable clearable onChange={setMembers} />}
          <Group justify="flex-end">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button loading={busy} type="submit">Save alias</Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}
