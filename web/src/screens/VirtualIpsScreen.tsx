import { Alert, Anchor, Badge, Button, Drawer, Fieldset, Group, Modal, ScrollArea, Stack, Table, Text, Textarea, TextInput, Title, VisuallyHidden } from '@mantine/core';
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { Link } from 'react-router';
import { ApiClient, ApiError, fieldErrors, reportError, saveFailure } from '../api';
import { useResource } from '../useResource';
import type { SavedObject, Session, VirtualAddress } from '../types';
import { checkVirtualIp, ipFamily, maxVirtualAddresses, virtualAddressUses, type VirtualIpUse } from '../virtualIps';
import { Address } from './Address';
import { Loaded } from './Loaded';

interface VirtualIpsScreenProps {
  api: ApiClient;
  session: Session;
  onSaved(): void;
  onSessionLost(): void;
}

const mono = { input: 'mono-input' };
const vipFieldPattern = /^(?:\/virtualAddresses\/\d+)?\/(name|address|description)(?:\/|$)/;

function VirtualIpEditor({
  saved,
  existingVips,
  addressLocks,
  onClose,
  onSessionLost,
  onRefused,
  onSave,
}: {
  saved?: SavedObject<VirtualAddress>;
  existingVips: Array<SavedObject<VirtualAddress>>;
  addressLocks: VirtualIpUse[];
  onClose(): void;
  onSessionLost(): void;
  onRefused(): void;
  onSave(vip: Omit<VirtualAddress, 'id'>, saved?: SavedObject<VirtualAddress>): Promise<void>;
}) {
  const [name, setName] = useState(saved?.value.name ?? '');
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

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    setFailure('');
    const { errors: localErrors, duplicate } = checkVirtualIp(name, address, existingVips, saved?.value.id);
    if (localErrors.size > 0) {
      setErrors(localErrors);
      setErrorFocusTick((tick) => tick + 1);
      if (duplicate) onRefused();
      return;
    }

    setBusy(true);
    setErrors(new Map());
    const body: Omit<VirtualAddress, 'id'> = {
      name: name.trim(),
      address: address.trim(),
      ...(description.trim() ? { description: description.trim() } : {}),
    };
    try {
      await onSave(body, saved);
      if (open.current) onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      if (!open.current) return;
      const fields = fieldErrors(error, vipFieldPattern);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'virtual IP'));
      if (fields.size > 0 && !fields.has('')) setErrorFocusTick((tick) => tick + 1);
    } finally {
      if (open.current) setBusy(false);
    }
  }

  const singleLock = addressLocks.length === 1;

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
              <TextInput
                classNames={mono}
                label="Address"
                description="Single unicast IPv4 or IPv6 address without a prefix length, outside any configured interface subnet."
                placeholder="172.16.53.53 or fd00:53::1"
                maxLength={64}
                required
                value={address}
                error={errors.get('address')}
                onChange={(event) => setAddress(event.currentTarget.value)}
              />
              {saved && addressLocks.length > 0 && (
                <Alert
                  color="yellow"
                  role="status"
                  title={singleLock ? 'Address in use by port forward' : 'Address in use by port forwards'}
                >
                  {addressLocks.map((u) => u.label).join(', ')} {singleLock ? 'targets' : 'target'} this address. You can still rename or describe it, but changing the address requires updating or removing {singleLock ? 'the port forward' : 'those port forwards'} first.
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

// Standalone firewall virtual IP addresses held on andaru-vip for services such as DNS and port forwards.
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
            Standalone host addresses held by the firewall outside interface subnets (on <Text span ff="monospace" inherit>andaru-vip</Text>) for services such as DNS and port forwards.
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
          const sorted = [...virtualAddresses.items].sort((a, b) => a.value.name.localeCompare(b.value.name, undefined, { sensitivity: 'base' }));
          const words = filter.trim().toLowerCase().split(/\s+/).filter(Boolean);
          const rows = sorted.map((item) => ({
            item,
            family: ipFamily(item.value.address),
            uses: virtualAddressUses(item.value, snapshot),
          }));
          const shown = words.length === 0 ? rows : rows.filter(({ item, family, uses }) => {
            const v = item.value;
            const haystack = [v.name, v.address, family ?? '', v.description ?? '', ...uses.map((u) => u.label)].join(' ').toLowerCase();
            return words.every((w) => haystack.includes(w));
          });
          const deletingRow = deletingId ? rows.find((r) => r.item.value.id === deletingId) : undefined;
          const deleting = deletingRow?.item;
          const blockers = deletingRow?.uses ?? [];
          const editingSaved = editing && editing !== 'new' ? editing : undefined;
          const editingAddressLocks = editingSaved
            ? (rows.find((r) => r.item.value.id === editingSaved.value.id)?.uses ?? []).filter((u) => u.locksAddress)
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
                  placeholder="Name, address, description, or use"
                  value={filter}
                  onChange={(event) => setFilter(event.currentTarget.value)}
                />
              )}

              {shown.length === 0 ? (
                <Alert role="status" title="No virtual IPs">
                  {sorted.length === 0
                    ? 'No virtual IP is saved yet. Add one here to give firewall services such as DNS or port forwards their own address.'
                    : 'No virtual IP matches the filter.'}
                </Alert>
              ) : (
                <ScrollArea type="auto">
                  <Table className="data-table" verticalSpacing="xs">
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>Name</Table.Th>
                        <Table.Th>Address</Table.Th>
                        <Table.Th>Used by</Table.Th>
                        {canWrite && <Table.Th><VisuallyHidden>Actions</VisuallyHidden></Table.Th>}
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {shown.map(({ item, family, uses }) => {
                        const v = item.value;
                        return (
                          <Table.Tr key={v.id}>
                            <Table.Td data-label="Name">
                              <Stack gap={2} align="flex-start">
                                <Text size="sm" fw={600}>{v.name}</Text>
                                {v.description && <Text size="xs" c="dimmed">{v.description}</Text>}
                              </Stack>
                            </Table.Td>
                            <Table.Td data-label="Address">
                              <Group gap="xs" wrap="wrap">
                                <Address value={v.address} />
                                {family && <Badge size="xs" variant="light" color={family === 'IPv6' ? 'violet' : 'blue'}>{family}</Badge>}
                              </Group>
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

              {editing && !(editing === 'new' && atLimit) && (
                <VirtualIpEditor
                  key={editing === 'new' ? 'new' : editing.value.id}
                  saved={editingSaved}
                  existingVips={virtualAddresses.items}
                  addressLocks={editingAddressLocks}
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
