import { Alert, Anchor, Badge, Button, Drawer, Fieldset, Group, Modal, NativeSelect, ScrollArea, Stack, Table, Text, Textarea, TextInput, Title, VisuallyHidden } from '@mantine/core';
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { Link } from 'react-router';
import { ApiClient, ApiError, fieldErrors, reportError, saveFailure } from '../api';
import { useResource } from '../useResource';
import type { SavedObject, Segment, Session, VirtualAddress, VirtualAddressKind } from '../types';
import {
  checkVirtualIp,
  isProxyArpEligibleSegment,
  isSingleVirtualAddress,
  maxVirtualAddresses,
  staticIpv4Subnets,
  staticIpv6Subnets,
  virtualAddressFamily,
  virtualAddressKind,
  virtualAddressKindLabel,
  virtualAddressUses,
  type VirtualIpUse,
} from '../virtualIps';
import { Address } from './Address';
import { Loaded } from './Loaded';

interface VirtualIpsScreenProps {
  api: ApiClient;
  session: Session;
  onSaved(): void;
  onSessionLost(): void;
}

const mono = { input: 'mono-input' };
const vipFieldPattern = /^(?:\/virtualAddresses\/\d+)?\/(name|kind|segment|hostSegment|address|description)(?:\/|$)/;

const kindOptions: Array<{ value: VirtualAddressKind; label: string }> = [
  { value: 'address', label: 'Held address or block (andaru-vip)' },
  { value: 'proxyArp', label: 'Proxy ARP (IPv4)' },
  { value: 'proxyNdp', label: 'Proxy NDP (IPv6)' },
];

const ipv6ModeLabels: Record<string, string> = {
  slaac: 'SLAAC',
  dhcp6: 'DHCPv6',
  delegated: 'delegated',
};

function ndpSegmentLabel(s: Segment): string {
  const subnets = staticIpv6Subnets(s);
  if (subnets.length > 0) return `${s.name} (${subnets.map((sub) => sub.cidr).join(', ')})`;
  const mode = ipv6ModeLabels[s.addressing.ipv6.mode];
  return mode ? `${s.name} (${mode})` : s.name;
}

function VirtualIpEditor({
  saved,
  existingVips,
  segments,
  uses,
  onClose,
  onSessionLost,
  onRefused,
  onSave,
}: {
  saved?: SavedObject<VirtualAddress>;
  existingVips: Array<SavedObject<VirtualAddress>>;
  segments: Segment[];
  uses: VirtualIpUse[];
  onClose(): void;
  onSessionLost(): void;
  onRefused(): void;
  onSave(vip: Omit<VirtualAddress, 'id'>, saved?: SavedObject<VirtualAddress>): Promise<void>;
}) {
  const [name, setName] = useState(saved?.value.name ?? '');
  const [kind, setKind] = useState<VirtualAddressKind>(saved ? virtualAddressKind(saved.value) : 'address');
  const [segment, setSegment] = useState(saved?.value.segment ?? '');
  const [hostSegment, setHostSegment] = useState(saved?.value.hostSegment ?? '');
  const [address, setAddress] = useState(saved?.value.address ?? '');
  const [description, setDescription] = useState(saved?.value.description ?? '');
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const [errorFocusTick, setErrorFocusTick] = useState(0);

  const open = useRef(true);
  useEffect(() => { open.current = true; return () => { open.current = false; }; }, []);
  const formRef = useRef<HTMLFormElement>(null);
  const failureRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!failure) return;
    failureRef.current?.scrollIntoView?.({ block: 'nearest' });
    if (errors.size === 0 || errors.has('')) failureRef.current?.focus?.();
  }, [failure, errors]);

  useEffect(() => {
    if (errorFocusTick === 0) return;
    const invalid = formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]:not(:disabled)');
    if (invalid) {
      invalid.scrollIntoView?.({ block: 'nearest' });
      invalid.focus?.();
    }
  }, [errorFocusTick]);

  const sortedSegments = [...segments].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));

  const eligibleSegments = sortedSegments.filter(isProxyArpEligibleSegment);

  const segmentOptions = (() => {
    const items = eligibleSegments.map((s) => ({
      value: s.id,
      label: `${s.name} (${staticIpv4Subnets(s).filter((sub) => sub.prefix.bits < 32).map((sub) => sub.cidr).join(', ')})`,
    }));
    if (segment && !eligibleSegments.some((s) => s.id === segment)) {
      const found = segments.find((s) => s.id === segment);
      items.unshift({
        value: segment,
        label: found ? `${found.name} (no static IPv4 subnet)` : `${segment} (missing segment)`,
      });
    }
    if (!segment) {
      items.unshift({
        value: '',
        label: eligibleSegments.length === 0 ? 'No segment with a static IPv4 subnet' : 'Choose a segment…',
      });
    }
    return items;
  })();

  const ndpSegmentOptions = (selectedId: string, emptyLabel: string) => {
    const items = sortedSegments.map((s) => ({ value: s.id, label: ndpSegmentLabel(s) }));
    if (selectedId && !sortedSegments.some((s) => s.id === selectedId)) {
      items.unshift({ value: selectedId, label: `${selectedId} (missing segment)` });
    }
    if (!selectedId) {
      items.unshift({ value: '', label: sortedSegments.length === 0 ? 'No segments available' : emptyLabel });
    }
    return items;
  };

  function changeKind(next: VirtualAddressKind) {
    setKind(next);
    if (
      next === 'proxyArp'
      && segment
      && !eligibleSegments.some((s) => s.id === segment)
      && !(saved && virtualAddressKind(saved.value) === 'proxyArp' && saved.value.segment === segment)
    ) {
      setSegment('');
    }
    setFailure('');
    setErrors((prev) => {
      if (!prev.has('kind') && !prev.has('segment') && !prev.has('hostSegment') && !prev.has('address')) return prev;
      const copy = new Map(prev);
      copy.delete('kind');
      copy.delete('segment');
      copy.delete('hostSegment');
      copy.delete('address');
      return copy;
    });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    setFailure('');
    const { errors: localErrors, stale } = checkVirtualIp(name, address, existingVips, saved?.value.id, {
      kind,
      segment,
      hostSegment,
      segments,
    });
    if (localErrors.size > 0) {
      setErrors(localErrors);
      setErrorFocusTick((tick) => tick + 1);
      if (stale) onRefused();
      return;
    }

    setBusy(true);
    setErrors(new Map());
    const body: Omit<VirtualAddress, 'id'> = {
      name: name.trim(),
      ...(kind === 'proxyArp' ? { kind: 'proxyArp', segment: segment.trim() } : {}),
      ...(kind === 'proxyNdp' ? { kind: 'proxyNdp', segment: segment.trim(), hostSegment: hostSegment.trim() } : {}),
      address: address.trim(),
      ...(description.trim() ? { description: description.trim() } : {}),
    };
    try {
      await onSave(body, saved);
      if (open.current) onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      if (!open.current) return;
      const fields = fieldErrors(error, (path) => {
        const field = vipFieldPattern.exec(path)?.[1] ?? '';
        if (field === 'segment' && kind === 'address') return '';
        if (field === 'hostSegment' && kind !== 'proxyNdp') return '';
        return field;
      });
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'virtual IP'));
      if (fields.size > 0 && !fields.has('')) setErrorFocusTick((tick) => tick + 1);
    } finally {
      if (open.current) setBusy(false);
    }
  }

  const addressLocks = uses.filter((u) => u.locksAddress);
  const usedByDns = uses.some((u) => u.key === 'dns');
  const singleLock = addressLocks.length === 1;
  const isProxyArp = kind === 'proxyArp';
  const isProxyNdp = kind === 'proxyNdp';
  const isHeldBlockDraft = kind === 'address'
    && virtualAddressFamily({ kind: 'address', address }) !== undefined
    && !isSingleVirtualAddress({ kind: 'address', address });

  return (
    <Drawer
      opened
      onClose={() => { if (!busy) onClose(); }}
      closeOnClickOutside={!busy}
      closeOnEscape={!busy}
      withCloseButton={!busy}
      position="right"
      size="md"
      title={saved ? `Edit virtual IP: ${saved.value.name}` : 'Add virtual IP'}
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close' }}
    >
      <form ref={formRef} onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert ref={failureRef} tabIndex={-1} color="red" role="alert" title="The virtual IP was not saved">{failure}</Alert>}
          <Fieldset variant="unstyled" disabled={busy}>
            <Stack>
              <TextInput
                data-autofocus
                label="Name"
                maxLength={128}
                required
                value={name}
                error={errors.get('name')}
                onChange={(event) => setName(event.currentTarget.value)}
              />
              <NativeSelect
                label="Kind"
                description={isProxyArp
                  ? 'Answered by ARP on a segment without being held on the firewall, for 1:1 NAT and port forwards.'
                  : isProxyNdp
                    ? 'Answered by neighbour discovery on the uplink and routed as a /128 to an inside host segment without translation.'
                    : 'Held by the firewall on andaru-vip for services such as DNS (single address) and port forwards (single address or routed block).'}
                data={kindOptions}
                value={kind}
                error={errors.get('kind')}
                onChange={(event) => changeKind(event.currentTarget.value as VirtualAddressKind)}
              />
              {isProxyArp && (
                <NativeSelect
                  label="Segment"
                  description="Segment whose link answers ARP. Requires a static IPv4 subnet (DHCP and PPPoE segments cannot use proxy ARP)."
                  required
                  data={segmentOptions}
                  value={segment}
                  error={errors.get('segment')}
                  onChange={(event) => setSegment(event.currentTarget.value)}
                />
              )}
              {isProxyNdp && (
                <>
                  <NativeSelect
                    label="Uplink segment"
                    description="Segment on whose link the firewall answers IPv6 neighbour discovery for this address."
                    required
                    data={ndpSegmentOptions(segment, 'Choose an uplink segment…')}
                    value={segment}
                    error={errors.get('segment')}
                    onChange={(event) => setSegment(event.currentTarget.value)}
                  />
                  <NativeSelect
                    label="Host segment"
                    description="Inside segment where the host lives. The firewall adds a /128 on-link route there."
                    required
                    data={ndpSegmentOptions(hostSegment, 'Choose a host segment…')}
                    value={hostSegment}
                    error={errors.get('hostSegment')}
                    onChange={(event) => setHostSegment(event.currentTarget.value)}
                  />
                </>
              )}
              <TextInput
                classNames={mono}
                label={isProxyNdp ? 'Address' : 'Address or prefix'}
                description={isProxyArp
                  ? "IPv4 address or /24–/32 prefix inside the selected segment's static IPv4 subnet (host bits zero)."
                  : isProxyNdp
                    ? "Single global unicast IPv6 address from the uplink's prefix, without a length."
                    : 'Unicast IPv4 (/24–/32) or IPv6 (/48–/128) address or routed block outside any configured interface subnet (host bits zero). A /32 or /128 is stored bare.'}
                placeholder={isProxyArp ? '203.0.113.50 or 203.0.113.56/29' : isProxyNdp ? '2001:db8:1::50' : '172.16.53.53, 198.51.100.0/29, or 2001:db8:53::/64'}
                maxLength={64}
                required
                value={address}
                error={errors.get('address')}
                onChange={(event) => setAddress(event.currentTarget.value)}
              />
              {isProxyNdp && (
                <Alert color="blue" role="note" title="Host configuration">
                  Configure this address on the inside host as a <Text span ff="monospace" inherit>/128</Text> with its default route via the firewall (router advertisements on the host segment provide that route). Using a <Text span ff="monospace" inherit>/64</Text> would make the host look for uplink neighbours on its own link.
                </Alert>
              )}
              {saved && usedByDns && (isProxyArp || isProxyNdp || isHeldBlockDraft) && (
                <Alert
                  color="yellow"
                  role="status"
                  title="Address in use by DNS service"
                >
                  {isProxyArp || isProxyNdp ? (
                    <>
                      The DNS service listens on this virtual IP. Remove it from{' '}
                      <Anchor component={Link} to="/network/dns" size="sm" onClick={onClose}>DNS service</Anchor>{' '}
                      before switching it to {isProxyArp ? 'Proxy ARP' : 'Proxy NDP'}.
                    </>
                  ) : (
                    <>
                      The DNS service listens on single addresses; name one address, not a block. Remove this virtual IP from{' '}
                      <Anchor component={Link} to="/network/dns" size="sm" onClick={onClose}>DNS service</Anchor>{' '}
                      before changing it to a block.
                    </>
                  )}
                </Alert>
              )}
              {saved && addressLocks.length > 0 && (
                <Alert
                  color="yellow"
                  role="status"
                  title={singleLock ? 'Address in use by port forward' : 'Address in use by port forwards'}
                >
                  {addressLocks.map((u) => u.label).join(', ')} {singleLock ? 'targets' : 'target'} this virtual IP. You can still rename or describe it, or change the prefix while keeping {singleLock ? 'the targeted address' : 'the targeted addresses'} covered; moving {singleLock ? 'it' : 'them'} out of range or switching kind requires updating or removing {singleLock ? 'the port forward' : 'those port forwards'} first.
                </Alert>
              )}
              <Textarea
                autosize
                label="Description"
                maxLength={4096}
                minRows={2}
                maxRows={4}
                value={description}
                error={errors.get('description')}
                onChange={(event) => setDescription(event.currentTarget.value)}
              />
            </Stack>
          </Fieldset>
          <Group justify="flex-end" className="drawer-actions">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button loading={busy} type="submit">{saved ? 'Save virtual IP' : 'Add virtual IP'}</Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}

// Held firewall virtual IP addresses/blocks (on andaru-vip), IPv4 proxy ARP entries, and IPv6 proxy NDP entries.
export function VirtualIpsScreen({ api, session, onSaved, onSessionLost }: VirtualIpsScreenProps) {
  const canWrite = session.capabilities.includes('configuration.write');
  // Retry up to twice when a save lands between the two reads so virtual IPs and snapshot match versions.
  const load = useCallback(async () => {
    for (let tries = 0; ; tries++) {
      const [virtualAddresses, snapshot] = await Promise.all([api.virtualAddresses(), api.config()]);
      if (virtualAddresses.savedVersion === snapshot.savedVersion || tries === 2) return { virtualAddresses, snapshot };
    }
  }, [api]);
  const resource = useResource(load, onSessionLost);

  const [filter, setFilter] = useState('');
  const [editing, setEditing] = useState<SavedObject<VirtualAddress> | 'new'>();
  const [deletingId, setDeletingId] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  if (deletingId && resource.data && !resource.data.virtualAddresses.items.some((item) => item.value.id === deletingId)) {
    setDeletingId(undefined);
  }

  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);

  async function remove(item: SavedObject<VirtualAddress>) {
    if (!canWrite || busy) return;
    setBusy(true);
    setError('');
    let sessionLost = false;
    try {
      await api.deleteVirtualAddress(item.value.id, item.version);
      onSaved();
    } catch (requestError) {
      if (requestError instanceof ApiError && requestError.status === 401) {
        sessionLost = true;
        onSessionLost();
      } else if (mounted.current) {
        if (requestError instanceof ApiError && requestError.status === 412) {
          setError(`Virtual IP ${item.value.name} changed elsewhere. The list now shows what is saved.`);
        } else if (requestError instanceof ApiError && requestError.status === 404) {
          setError(`Virtual IP ${item.value.name} was already deleted elsewhere. The list now shows what is saved.`);
        } else if (requestError instanceof ApiError && requestError.status === 422) {
          const portForwardDetail = requestError.details.find((d) => d.path === '/id' && d.message)?.message;
          setError(portForwardDetail ?? `Virtual IP ${item.value.name} is in use and cannot be deleted. The list now shows current references.`);
        } else {
          reportError(requestError, onSessionLost, setError);
        }
      }
    } finally {
      if (!sessionLost && mounted.current) {
        await resource.reload();
      }
      if (mounted.current) {
        setDeletingId(undefined);
        setBusy(false);
      }
    }
  }

  const atLimit = (resource.data?.virtualAddresses.items.length ?? 0) >= maxVirtualAddresses;

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div>
          <Title order={2}>Virtual IPs</Title>
          <Text c="dimmed">
            Held addresses and routed blocks on <Text span ff="monospace" inherit>andaru-vip</Text> for firewall services, IPv4 proxy ARP entries, and IPv6 proxy NDP entries routed to inside hosts.
          </Text>
        </div>
        <Group gap="xs">
          <Button size="xs" variant="default" loading={resource.loading} onClick={() => void resource.reload()}>Refresh</Button>
          {canWrite && (
            <Button size="xs" disabled={!resource.data || atLimit} onClick={() => setEditing('new')}>Add virtual IP</Button>
          )}
        </Group>
      </Group>

      {error && <Alert color="red" role="alert" title="The virtual IPs were not changed" withCloseButton onClose={() => setError('')}>{error}</Alert>}

      <Loaded resource={resource} what="virtual IPs">
        {({ virtualAddresses, snapshot }) => {
          const segmentById = new Map(snapshot.value.segments.map((s) => [s.id, s]));
          const sorted = [...virtualAddresses.items].sort((a, b) => a.value.name.localeCompare(b.value.name, undefined, { sensitivity: 'base' }));
          const words = filter.trim().toLowerCase().split(/\s+/).filter(Boolean);
          const rows = sorted.map((item) => {
            const kind = virtualAddressKind(item.value);
            const segmentName = kind !== 'address' && item.value.segment
              ? (segmentById.get(item.value.segment)?.name ?? item.value.segment)
              : undefined;
            const hostSegmentName = kind === 'proxyNdp' && item.value.hostSegment
              ? (segmentById.get(item.value.hostSegment)?.name ?? item.value.hostSegment)
              : undefined;
            return {
              item,
              kind,
              segmentName,
              hostSegmentName,
              family: virtualAddressFamily(item.value),
              uses: virtualAddressUses(item.value, snapshot),
            };
          });
          const shown = words.length === 0 ? rows : rows.filter(({ item, kind, segmentName, hostSegmentName, family, uses }) => {
            const v = item.value;
            const haystack = [
              v.name,
              virtualAddressKindLabel[kind],
              kind,
              segmentName ?? '',
              hostSegmentName ?? '',
              v.address,
              family ?? '',
              v.description ?? '',
              ...uses.map((u) => u.label),
            ].join(' ').toLowerCase();
            return words.every((w) => haystack.includes(w));
          });
          const deletingRow = deletingId ? rows.find((r) => r.item.value.id === deletingId) : undefined;
          const deleting = deletingRow?.item;
          const blockers = deletingRow?.uses ?? [];
          const editingSaved = editing && editing !== 'new' ? editing : undefined;
          const editingUses = editingSaved
            ? (rows.find((r) => r.item.value.id === editingSaved.value.id)?.uses ?? [])
            : [];

          return (
            <Stack>
              {canWrite && atLimit && (
                <Alert role="status" title="Virtual IP limit reached">
                  All {maxVirtualAddresses} virtual IP slots are in use. Delete an unused virtual IP before adding another.
                </Alert>
              )}

              {sorted.length > 0 && (
                <TextInput
                  label="Filter virtual IPs"
                  placeholder="Name, kind, segment, address, description, or use"
                  value={filter}
                  onChange={(event) => setFilter(event.currentTarget.value)}
                />
              )}

              {shown.length === 0 ? (
                <Alert role="status" title="No virtual IPs">
                  {sorted.length === 0
                    ? (canWrite
                      ? 'No virtual IP is saved yet. Add a held address or block for firewall services, a proxy ARP entry for IPv4, or a proxy NDP entry to route an uplink IPv6 address to an inside host.'
                      : 'No virtual IP is saved yet.')
                    : 'No virtual IP matches the filter.'}
                </Alert>
              ) : (
                <ScrollArea type="auto">
                  <Table className="data-table" verticalSpacing="xs">
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>Name</Table.Th>
                        <Table.Th>Kind</Table.Th>
                        <Table.Th>Address</Table.Th>
                        <Table.Th>Segment</Table.Th>
                        <Table.Th>Used by</Table.Th>
                        {canWrite && <Table.Th><VisuallyHidden>Actions</VisuallyHidden></Table.Th>}
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {shown.map(({ item, kind, segmentName, hostSegmentName, family, uses }) => {
                        const v = item.value;
                        const segmentLabel = kind === 'proxyNdp'
                          ? (segmentName || hostSegmentName ? `${segmentName ?? '—'} → ${hostSegmentName ?? '—'}` : undefined)
                          : segmentName;
                        return (
                          <Table.Tr key={v.id}>
                            <Table.Td data-label="Name">
                              <Stack gap={2} align="flex-start">
                                <Text size="sm" fw={600}>{v.name}</Text>
                                {v.description && <Text size="xs" c="dimmed">{v.description}</Text>}
                              </Stack>
                            </Table.Td>
                            <Table.Td data-label="Kind">
                              <Badge size="xs" variant="light" color={kind === 'proxyArp' ? 'teal' : kind === 'proxyNdp' ? 'indigo' : 'gray'}>
                                {virtualAddressKindLabel[kind]}
                              </Badge>
                            </Table.Td>
                            <Table.Td data-label="Address">
                              <Group gap="xs" wrap="wrap">
                                <Address value={v.address} />
                                {family && <Badge size="xs" variant="light" color={family === 'IPv6' ? 'violet' : 'blue'}>{family}</Badge>}
                              </Group>
                            </Table.Td>
                            <Table.Td data-label="Segment">
                              {segmentLabel ? (
                                <Text size="sm">{segmentLabel}</Text>
                              ) : (
                                <Text size="sm" c="dimmed">—</Text>
                              )}
                            </Table.Td>
                            <Table.Td data-label="Used by">
                              {uses.length === 0 ? (
                                <Text size="sm" c="dimmed">Not used</Text>
                              ) : (
                                <Group gap="xs" wrap="wrap">
                                  {uses.map((u) => (
                                    <Anchor key={u.key} component={Link} to={u.to} size="sm">{u.label}</Anchor>
                                  ))}
                                </Group>
                              )}
                            </Table.Td>
                            {canWrite && (
                              <Table.Td>
                                <Group gap="xs" justify="flex-end" wrap="nowrap">
                                  <Button size="compact-xs" variant="subtle" aria-label={`Edit virtual IP ${v.name}`} onClick={() => setEditing(item)}>Edit</Button>
                                  <Button size="compact-xs" variant="subtle" color="red" aria-label={`Delete virtual IP ${v.name}`} onClick={() => setDeletingId(v.id)}>Delete</Button>
                                </Group>
                              </Table.Td>
                            )}
                          </Table.Tr>
                        );
                      })}
                    </Table.Tbody>
                  </Table>
                </ScrollArea>
              )}

              {editing && (
                <VirtualIpEditor
                  key={editing === 'new' ? 'new' : editing.value.id}
                  saved={editingSaved}
                  existingVips={virtualAddresses.items}
                  segments={snapshot.value.segments}
                  uses={editingUses}
                  onClose={() => setEditing(undefined)}
                  onSessionLost={onSessionLost}
                  onRefused={() => { if (mounted.current) void resource.reload(); }}
                  onSave={async (vip, saved) => {
                    try {
                      await api.saveVirtualAddress(vip, saved);
                    } catch (requestError) {
                      if (!(requestError instanceof ApiError && requestError.status === 401) && mounted.current) {
                        await resource.reload();
                      }
                      throw requestError;
                    }
                    onSaved();
                    if (!saved && mounted.current) setFilter('');
                    if (mounted.current) await resource.reload();
                  }}
                />
              )}

              <Modal
                opened={Boolean(deleting)}
                onClose={() => { if (!busy) setDeletingId(undefined); }}
                closeOnClickOutside={!busy}
                closeOnEscape={!busy}
                withCloseButton={!busy}
                title="Delete virtual IP"
                centered
                lockScroll={false}
                closeButtonProps={{ 'aria-label': 'Close' }}
              >
                {deleting && (blockers.length > 0 ? (
                  <Stack>
                    <Text>
                      <strong>{deleting.value.name}</strong> (<Text span ff="monospace" inherit>{deleting.value.address}</Text>) cannot be deleted while these use it. Remove it from them first:
                    </Text>
                    <ul className="fixed-rules">
                      {blockers.map((use) => (
                        <li key={use.key}>
                          <Anchor component={Link} to={use.to} size="sm" onClick={() => setDeletingId(undefined)}>{use.label}</Anchor>
                        </li>
                      ))}
                    </ul>
                    <Group justify="flex-end">
                      <Button variant="default" onClick={() => setDeletingId(undefined)}>Keep it</Button>
                    </Group>
                  </Stack>
                ) : (
                  <Stack>
                    <Text>
                      Delete the virtual IP <strong>{deleting.value.name}</strong> (<Text span ff="monospace" inherit>{deleting.value.address}</Text>)? Nothing uses it.
                    </Text>
                    <Group justify="flex-end">
                      <Button variant="default" disabled={busy} onClick={() => setDeletingId(undefined)}>Cancel</Button>
                      <Button color="red" loading={busy} onClick={() => void remove(deleting)}>Delete virtual IP</Button>
                    </Group>
                  </Stack>
                ))}
              </Modal>
            </Stack>
          );
        }}
      </Loaded>
    </Stack>
  );
}
