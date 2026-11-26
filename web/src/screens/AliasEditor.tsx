import { Alert, Button, Drawer, Fieldset, Group, MultiSelect, NativeSelect, Stack, TagsInput, Textarea, TextInput } from '@mantine/core';
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { icmpTypes } from '../aliasLiteral';
import { ApiClient, ApiError, fieldErrors, saveFailure } from '../api';
import {
  defaultHostnamesRefreshSeconds,
  defaultUrlRefreshSeconds,
  isInsecureHttpUrl,
  normalizeAliasHostnames,
  validateAliasHostnames,
  validateAliasRefreshSeconds,
  validateAliasUrl,
} from '../dynamicAlias';
import { isDynamicAddressAlias, type AddressAlias, type AliasKinds, type PortAlias, type SavedObject } from '../types';
import { AliasMultiSelect } from './AliasPicker';
import type { Row } from './AliasesScreen';

interface AliasEditorProps {
  api: ApiClient;
  // A row to edit, or the kind of alias to add.
  editing: Row | keyof AliasKinds;
  rows: Row[];
  onClose(): void;
  onSessionLost(): void;
  // A save failed after the panel was closed.
  onAbandoned(message: string): void;
  // The panel refused a save from the aliases it was given: they may be stale, and are read again.
  onRefused(): void;
  onAliasCreated?(): void;
  onSave<K extends keyof AliasKinds>(kind: K, alias: Omit<AliasKinds[K], 'id'>, saved?: SavedObject<AliasKinds[K]>): Promise<void>;
}

const field = /^\/(name|description|kind|protocol|address|prefix|first|last|members|url|names|refreshSeconds|ports|types)(?:\/|$)/;

export function AliasEditor({ api, editing, rows, onClose, onSessionLost, onAbandoned, onRefused, onAliasCreated, onSave }: AliasEditorProps) {
  const kind = typeof editing === 'string' ? editing : editing.kind;
  const saved = typeof editing === 'string' ? undefined : editing;
  const savedAddress = saved?.kind === 'addresses' ? saved.item : undefined;
  const savedPort = saved?.kind === 'ports' ? saved.item : undefined;
  const address = savedAddress?.value;
  const port = savedPort?.value;
  const noun = kind === 'addresses' ? 'address alias' : 'port alias';
  const title = saved ? `Edit ${noun}: ${saved.item.value.name}` : `Add ${noun}`;
  const [name, setName] = useState(saved?.item.value.name ?? '');
  const [description, setDescription] = useState(saved?.item.value.description ?? '');
  const [what, setWhat] = useState<string>(address?.kind ?? port?.protocol ?? (kind === 'addresses' ? 'host' : 'tcp'));
  const [value, setValue] = useState(address?.address ?? address?.prefix ?? address?.first ?? '');
  const [last, setLast] = useState(address?.last ?? '');
  const [url, setUrl] = useState(address?.url ?? '');
  const [hostnames, setHostnames] = useState<string[]>(address?.names ?? []);
  const [refreshSeconds, setRefreshSeconds] = useState(
    address?.refreshSeconds !== undefined
      ? String(address.refreshSeconds)
      : String(address?.kind === 'hostnames' ? defaultHostnamesRefreshSeconds : defaultUrlRefreshSeconds),
  );
  const [ports, setPorts] = useState((port?.ports ?? []).join(', '));
  const [types, setTypes] = useState(port?.types ?? []);
  const [members, setMembers] = useState(saved?.item.value.members ?? []);
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const [creatingAliases, setCreatingAliases] = useState(0);
  const onCreatingChange = useCallback((creating: boolean) => {
    setCreatingAliases((count) => Math.max(0, count + (creating ? 1 : -1)));
  }, []);
  // A save may answer after this panel was closed and another opened: it then closes nothing.
  const open = useRef(true);
  useEffect(() => { open.current = true; return () => { open.current = false; }; }, []);
  // A refusal shows above the form, which may be scrolled away from it: it comes into view once, when it arrives.
  const failureRef = useRef<HTMLDivElement>(null);
  useEffect(() => { if (failure) failureRef.current?.scrollIntoView?.({ block: 'nearest' }); }, [failure]);

  // A group holds aliases of its own kind that are not groups (and for addresses, not dynamic url/hostnames), and never itself.
  const candidates = rows
    .filter((row) => (
      row.kind === kind
      && row.item.value.id !== saved?.item.value.id
      && (row.kind === 'addresses'
        ? row.item.value.kind !== 'group' && !isDynamicAddressAlias(row.item.value)
        : row.item.value.protocol !== 'group')
    ))
    .map((row) => ({ value: row.item.value.id, label: row.item.value.name }));

  function clearError(key: string) {
    setErrors((cur) => {
      if (!cur.has(key)) return cur;
      const next = new Map(cur);
      next.delete(key);
      return next;
    });
  }

  // Another kind shows other fields: what was said of the old ones goes with them, and the rest stays.
  function change(next: string) {
    if (kind === 'addresses') {
      const currentRefresh = refreshSeconds.trim();
      if (next === 'hostnames' && currentRefresh === String(defaultUrlRefreshSeconds)) {
        setRefreshSeconds(String(defaultHostnamesRefreshSeconds));
      } else if (next === 'url' && currentRefresh === String(defaultHostnamesRefreshSeconds)) {
        setRefreshSeconds(String(defaultUrlRefreshSeconds));
      }
    }
    setWhat(next);
    setErrors(new Map([...errors].filter(([k]) => k === 'name' || k === 'description')));
  }

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
      onRefused();
      return;
    }
    // A group holds no groups nor dynamic aliases, so an alias that is in one cannot become one.
    const nextIsDisallowedMember = what === 'group' || (kind === 'addresses' && (what === 'url' || what === 'hostnames'));
    const holder = saved && nextIsDisallowedMember ? rows.find((row) => row.item.value.members?.includes(saved.item.value.id)) : undefined;
    if (holder) {
      const reason = what === 'group'
        ? `This alias is in the group ${holder.item.value.name}, and a group cannot hold a group.`
        : `This alias is in the group ${holder.item.value.name}, and a group cannot hold a URL or host-name alias yet.`;
      setErrors(new Map([[kind === 'addresses' ? 'kind' : 'protocol', reason]]));
      onRefused();
      return;
    }
    if (kind === 'addresses' && (what === 'url' || what === 'hostnames')) {
      const localErrors = new Map<string, string>();
      if (what === 'url') {
        const urlErr = validateAliasUrl(url);
        if (urlErr) localErrors.set('url', urlErr);
      } else {
        const namesErr = validateAliasHostnames(hostnames);
        if (namesErr) localErrors.set('names', namesErr);
      }
      const refreshErr = validateAliasRefreshSeconds(refreshSeconds);
      if (refreshErr) localErrors.set('refreshSeconds', refreshErr);
      if (localErrors.size > 0) {
        setErrors(localErrors);
        return;
      }
    }
    setBusy(true);
    setErrors(new Map());
    const identity = { name: name.trim(), ...(description ? { description } : {}) };
    try {
      if (kind === 'addresses') {
        const body: Omit<AddressAlias, 'id'> = {
          ...identity,
          kind: what as AddressAlias['kind'],
          ...(what === 'host'
            ? { address: value.trim() }
            : what === 'network'
              ? { prefix: value.trim() }
              : what === 'range'
                ? { first: value.trim(), last: last.trim() }
                : what === 'url'
                  ? { url: url.trim(), refreshSeconds: Number(refreshSeconds.trim()) }
                  : what === 'hostnames'
                    ? { names: normalizeAliasHostnames(hostnames), refreshSeconds: Number(refreshSeconds.trim()) }
                    : { members }),
        };
        await onSave('addresses', body, savedAddress);
      } else {
        const body: Omit<PortAlias, 'id'> = { ...identity, protocol: what as PortAlias['protocol'],
          ...(what === 'group' ? { members } : what === 'tcp' || what === 'udp' ? { ports: ports.replace(/\s*-\s*/g, '-').split(/[\s,]+/).filter(Boolean) } : types.length > 0 ? { types } : {}) };
        await onSave('ports', body, savedPort);
      }
      if (open.current) onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      // The panel was closed while the save was out: the page says what became of it.
      if (!open.current) return onAbandoned(saveFailure(error, new Map(), noun));
      const fields = fieldErrors(error, field);
      setErrors(fields);
      setFailure(saveFailure(error, fields, noun));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Drawer opened onClose={onClose} position="right" size="md" title={title} lockScroll={false} closeButtonProps={{ 'aria-label': 'Close' }}>
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert ref={failureRef} color="red" role="alert" title="The alias was not saved">{failure}</Alert>}
          {/* What is sent is what the form held when Save was pressed: nothing is typed over it meanwhile. */}
          <Fieldset variant="unstyled" disabled={busy}>
            <Stack>
          <TextInput data-autofocus label="Name" maxLength={128} required value={name} error={errors.get('name')} onChange={(event) => { setName(event.currentTarget.value); clearError('name'); }} />
          <Textarea autosize label="Description" maxLength={4096} minRows={1} maxRows={4} value={description} error={errors.get('description')} onChange={(event) => { setDescription(event.currentTarget.value); clearError('description'); }} />
          {kind === 'addresses' ? (
            <>
              <NativeSelect label="Kind" value={what} error={errors.get('kind')} onChange={(event) => change(event.currentTarget.value)}
                data={[
                  { value: 'host', label: 'One address' },
                  { value: 'network', label: 'A network' },
                  { value: 'range', label: 'A range of addresses' },
                  { value: 'group', label: 'A group of address aliases' },
                  { value: 'url', label: 'A list from a URL' },
                  { value: 'hostnames', label: 'Host names (DNS)' },
                ]} />
              {what === 'host' && <TextInput label="Address" description="IPv4 or IPv6." placeholder="192.168.1.10" value={value} error={errors.get('address')} onChange={(event) => setValue(event.currentTarget.value)} />}
              {what === 'network' && <TextInput label="Network" description="A prefix with its length." placeholder="192.168.1.0/24" value={value} error={errors.get('prefix')} onChange={(event) => setValue(event.currentTarget.value)} />}
              {what === 'range' && (
                <>
                  <TextInput label="First address" placeholder="192.168.1.100" value={value} error={errors.get('first')} onChange={(event) => setValue(event.currentTarget.value)} />
                  <TextInput label="Last address" description="The same family as the first address, and not below it." placeholder="192.168.1.199" value={last} error={errors.get('last')} onChange={(event) => setLast(event.currentTarget.value)} />
                </>
              )}
              {what === 'url' && (
                <>
                  <TextInput
                    label="URL"
                    description="https:// or http:// URL returning IPv4/IPv6 addresses or prefixes, one per line (# comments and blank lines ignored)."
                    placeholder="https://www.cloudflare.com/ips-v4"
                    maxLength={2048}
                    required
                    value={url}
                    error={errors.get('url')}
                    onChange={(event) => { setUrl(event.currentTarget.value); clearError('url'); }}
                  />
                  {isInsecureHttpUrl(url) && (
                    <Alert color="orange" role="status" title="HTTP is not secure">
                      Not secure: anyone on the path can change the list, and with it what your rules allow.
                    </Alert>
                  )}
                  <TextInput
                    label="Refresh interval (seconds)"
                    description="From 60 to 86400 seconds (24 hours)."
                    placeholder="3600"
                    inputMode="numeric"
                    required
                    value={refreshSeconds}
                    error={errors.get('refreshSeconds')}
                    onChange={(event) => { setRefreshSeconds(event.currentTarget.value); clearError('refreshSeconds'); }}
                  />
                </>
              )}
              {what === 'hostnames' && (
                <>
                  <TagsInput
                    label="Host names"
                    description="1 to 20 distinct host names resolved for A and AAAA records (not IP addresses). Press Enter after each name."
                    placeholder="vpn.example.com"
                    required
                    clearable
                    value={hostnames}
                    error={errors.get('names')}
                    onChange={(next) => { setHostnames(next); clearError('names'); }}
                  />
                  <TextInput
                    label="Refresh interval (seconds)"
                    description="From 60 to 86400 seconds (24 hours)."
                    placeholder="300"
                    inputMode="numeric"
                    required
                    value={refreshSeconds}
                    error={errors.get('refreshSeconds')}
                    onChange={(event) => { setRefreshSeconds(event.currentTarget.value); clearError('refreshSeconds'); }}
                  />
                </>
              )}
            </>
          ) : (
            <>
              <NativeSelect label="Protocol" value={what} error={errors.get('protocol')} onChange={(event) => { change(event.currentTarget.value); setTypes([]); }}
                data={[{ value: 'tcp', label: 'TCP' }, { value: 'udp', label: 'UDP' }, { value: 'icmp', label: 'ICMP (IPv4)' }, { value: 'icmpv6', label: 'ICMPv6' }, { value: 'group', label: 'A group of port aliases' }]} />
              {(what === 'tcp' || what === 'udp') && <TextInput label="Ports" description="Ports and ranges, separated by commas: 80, 443, 8000-8100." value={ports} error={errors.get('ports')} onChange={(event) => setPorts(event.currentTarget.value)} />}
              {(what === 'icmp' || what === 'icmpv6') && <MultiSelect label="Types" description="Empty is every type." data={icmpTypes[what]} value={types} error={errors.get('types')} clearable onChange={setTypes} />}
            </>
          )}
          {what === 'group' && (
            <AliasMultiSelect
              api={api}
              aliasType={kind}
              excludeGroups
              exclude={saved?.item.value.id}
              label="Members"
              description={kind === 'addresses' ? 'Address aliases or literal IPs, CIDRs, or ranges (not groups, URL lists, or host-name aliases).' : 'Port aliases or literal ports (not groups).'}
              placeholder={kind === 'addresses' ? 'Alias name, 192.168.1.50, or 10.0.0.0/24' : 'Alias name, 443, or tcp/8000-8080'}
              initialOptions={candidates}
              value={members}
              error={errors.get('members')}
              clearable
              onAliasCreated={onAliasCreated}
              onCreatingChange={onCreatingChange}
              onSessionLost={onSessionLost}
              onChange={setMembers}
            />
          )}
            </Stack>
          </Fieldset>
          <Group justify="flex-end" className="drawer-actions">
            <Button variant="default" onClick={onClose}>Cancel</Button>
            <Button loading={busy} disabled={creatingAliases > 0} type="submit">Save alias</Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}
