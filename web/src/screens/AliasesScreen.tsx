import { Alert, Anchor, Badge, Button, Group, Modal, ScrollArea, SegmentedControl, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useCallback, useState } from 'react';
import { Link } from 'react-router';
import { ApiError, reportError, type ApiClient } from '../api';
import { formatAddressAlias, formatPortAlias } from '../format';
import type { AddressAlias, AliasKinds, PortAlias, SavedObject, Session, Snapshot } from '../types';
import { useResource } from '../useResource';
import { Address } from './Address';
import { AliasEditor } from './AliasEditor';
import { Loaded } from './Loaded';

interface AliasesScreenProps { api: ApiClient; session: Session; onSaved(): void; onSessionLost(): void }

type Kind = keyof AliasKinds;
// One row of the page: an alias of either kind, with the version that guards it.
export type Row = { kind: 'addresses'; item: SavedObject<AddressAlias> } | { kind: 'ports'; item: SavedObject<PortAlias> };

const kinds: Record<AddressAlias['kind'], string> = { host: 'Host', network: 'Network', range: 'Range', group: 'Address group' };
const kindLabel = (row: Row) => (row.kind === 'addresses' ? kinds[row.item.value.kind] : row.item.value.protocol === 'group' ? 'Port group' : 'Port');

// What an address alias holds, with addresses in their own type so they break only where they may.
function Holds({ row, names }: { row: Row; names: Map<string, string> }) {
  if (row.kind === 'addresses') {
    const alias = row.item.value;
    if (alias.kind === 'host' && alias.address) return <Address value={alias.address} />;
    if (alias.kind === 'network' && alias.prefix) return <Address value={alias.prefix} />;
    if (alias.kind === 'range' && alias.first && alias.last) return <Group gap={4} wrap="wrap"><Address value={alias.first} />{' '}<Text size="sm">to</Text>{' '}<Address value={alias.last} /></Group>;
  }
  return <Text size="sm">{holds(row, names)}</Text>;
}

// What an alias holds, as words for the filter to search.
function holds(row: Row, names: Map<string, string>) {
  return row.kind === 'addresses' ? formatAddressAlias(row.item.value, names) : formatPortAlias(row.item.value, names);
}

interface Usage { label: string; to?: string }

// uses says what still names an alias, in the words of the pages those things are on. The engine refuses to
// delete an alias in use; the page says by what, so the refusal is never the first the user hears of it.
function uses(id: string, rows: Row[], snapshot: Snapshot): Usage[] {
  const found: Usage[] = [];
  (snapshot.value.rules ?? []).forEach((rule, index) => {
    if ([...rule.from.addresses, ...rule.to.addresses, ...rule.ports].includes(id)) found.push({ label: `Rule ${index + 1}, ${rule.name}`, to: '/firewall/rules' });
  });
  for (const row of rows) if (row.item.value.members?.includes(id)) found.push({ label: `Alias ${row.item.value.name}` });
  if (snapshot.value.management?.allowedSources?.includes(id)) found.push({ label: 'The management segment\'s allowed sources', to: '/network/interfaces' });
  for (const forward of snapshot.value.nat?.portForwards ?? []) if (forward.from?.includes(id) || forward.to?.address === id) found.push({ label: `Port forward ${forward.name}` });
  for (const entry of snapshot.value.nat?.oneToOne ?? []) if (entry.destination?.includes(id)) found.push({ label: `One-to-one NAT ${entry.name}` });
  return found;
}

export function AliasesScreen({ api, session, onSaved, onSessionLost }: AliasesScreenProps) {
  const canWrite = session.capabilities.includes('configuration.write');
  // The aliases with their versions, and the configuration that names them. They are three reads: when a save
  // landed between them they are read again, twice at most, so "nothing names it" is seldom said of an alias
  // something just began to name.
  const resource = useResource(useCallback(async () => {
    for (let tries = 0; ; tries++) {
      const [addresses, ports, snapshot] = await Promise.all([api.aliases('addresses'), api.aliases('ports'), api.config()]);
      if ((addresses.savedVersion === snapshot.savedVersion && ports.savedVersion === snapshot.savedVersion) || tries === 2) return { addresses, ports, snapshot };
    }
  }, [api]), onSessionLost);
  const [show, setShow] = useState<'all' | Kind>('all');
  const [filter, setFilter] = useState('');
  const [editing, setEditing] = useState<Row | Kind>();
  const [deleting, setDeleting] = useState<Row>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  async function remove(row: Row) {
    setBusy(true);
    setError('');
    try {
      await api.deleteAlias(row.kind, row.item.value.id, row.item.version);
      onSaved();
    } catch (requestError) {
      if (requestError instanceof ApiError && requestError.status === 412) setError('The alias was changed by someone else. The list now shows what is saved; nothing was deleted.');
      // Refused as in use: something began to name it after this page read the configuration.
      else if (requestError instanceof ApiError && requestError.status === 422) setError('Something began to name this alias, so it was not deleted. The list now shows what names it.');
      else if (requestError instanceof ApiError && requestError.status === 404) setError('The alias was already deleted by someone else.');
      else reportError(requestError, onSessionLost, setError);
      if (requestError instanceof ApiError && requestError.status === 401) return;
    }
    await resource.reload();
    // The dialog may have been closed and another opened meanwhile: that one stays.
    setDeleting((current) => (current === row ? undefined : current));
    setBusy(false);
  }

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div><Title order={2}>Aliases</Title><Text c="dimmed">Names for addresses and for ports, which rules and NAT refer to. Change an alias and everything that names it follows.</Text></div>
        {canWrite && resource.data && (
          <Group gap="xs">
            <Button size="xs" onClick={() => setEditing('addresses')}>Add address alias</Button>
            <Button size="xs" onClick={() => setEditing('ports')}>Add port alias</Button>
          </Group>
        )}
      </Group>
      {error && <Alert color="red" role="alert" title="The aliases were not changed" withCloseButton onClose={() => setError('')}>{error}</Alert>}
      <Loaded resource={resource} what="aliases">
        {({ addresses, ports, snapshot }) => {
          const rows: Row[] = [
            ...addresses.items.map((item): Row => ({ kind: 'addresses', item })),
            ...ports.items.map((item): Row => ({ kind: 'ports', item })),
          ].sort((a, b) => a.item.value.name.localeCompare(b.item.value.name));
          const names = new Map(rows.map((row) => [row.item.value.id, row.item.value.name]));
          // Every word of the filter must be somewhere in what the row shows.
          const words = filter.toLowerCase().split(/\s+/).filter(Boolean);
          const matches = (row: Row) => { const text = [row.item.value.name, row.item.value.description ?? '', kindLabel(row), holds(row, names) ?? ''].join(' ').toLowerCase(); return words.every((word) => text.includes(word)); };
          const shown = rows.filter((row) => (show === 'all' || row.kind === show) && matches(row));
          const usedBy = (row: Row) => uses(row.item.value.id, rows, snapshot);
          const blockers = deleting ? usedBy(deleting) : [];
          const noun = (row: Row) => (row.kind === 'addresses' ? 'address alias' : 'port alias');
          return (
            <Stack>
              {rows.length > 0 && (
                <Group align="flex-end" gap="sm">
                  <TextInput className="grow" label="Filter aliases" placeholder="A name, address or port" value={filter} onChange={(event) => setFilter(event.currentTarget.value)} />
                  <SegmentedControl aria-label="Kind of alias" value={show} onChange={(next) => setShow(next as typeof show)}
                    data={[{ value: 'all', label: 'All' }, { value: 'addresses', label: 'Addresses' }, { value: 'ports', label: 'Ports' }]} />
                </Group>
              )}
              {shown.length === 0 ? <Alert role="status" title="No aliases">{rows.length === 0 ? 'None is saved yet. A rule can name zones without any; an alias is for naming hosts, networks and ports.' : words.length > 0 ? 'No alias matches the filter.' : 'None of this kind is saved.'}</Alert> : (
                <ScrollArea>
                  <Table className="data-table aliases" verticalSpacing="xs">
                    <Table.Thead><Table.Tr><Table.Th>Name</Table.Th><Table.Th>Kind</Table.Th><Table.Th>Holds</Table.Th><Table.Th>Used by</Table.Th>{canWrite && <Table.Th>Actions</Table.Th>}</Table.Tr></Table.Thead>
                    <Table.Tbody>
                      {shown.map((row) => {
                        const alias = row.item.value;
                        const used = usedBy(row);
                        return (
                          <Table.Tr key={alias.id}>
                            <Table.Td data-label="Name"><Text size="sm" fw={600}>{alias.name}</Text>{alias.description && <Text size="sm" c="dimmed">{alias.description}</Text>}</Table.Td>
                            <Table.Td data-label="Kind"><Badge variant={row.kind === 'addresses' ? 'default' : 'light'} color="gray" className="alias" tt="none">{kindLabel(row)}</Badge></Table.Td>
                            <Table.Td data-label="Holds"><Holds row={row} names={names} /></Table.Td>
                            <Table.Td data-label="Used by">
                              {used.length === 0 ? <Text size="sm" c="dimmed">nothing</Text> : (
                                <details><summary>{used.length === 1 ? '1 use' : `${used.length} uses`}</summary><ul className="fixed-rules">{used.map((use) => <li key={use.label}>{use.to ? <Anchor component={Link} to={use.to} size="sm">{use.label}</Anchor> : use.label}</li>)}</ul></details>
                              )}
                            </Table.Td>
                            {canWrite && (
                              <Table.Td>
                                <Group gap="xs" wrap="nowrap">
                                  <Button size="compact-xs" variant="default" disabled={busy} aria-label={`Edit ${noun(row)} ${alias.name}`} onClick={() => setEditing(row)}>Edit</Button>
                                  <Button size="compact-xs" variant="subtle" color="red" disabled={busy} aria-label={`Delete ${noun(row)} ${alias.name}`} onClick={() => setDeleting(row)}>Delete</Button>
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
                <AliasEditor key={typeof editing === 'string' ? editing : editing.item.value.id} api={api} editing={editing} rows={rows} onClose={() => setEditing(undefined)} onSessionLost={onSessionLost} onAbandoned={setError} onRefused={() => void resource.reload()}
                  onSave={async (kind, alias, saved) => {
                    try {
                      await api.saveAlias(kind, alias, saved);
                    } catch (requestError) {
                      // The alias may have changed underneath, or been saved with the answer lost.
                      if (!(requestError instanceof ApiError && requestError.status === 401)) await resource.reload();
                      throw requestError;
                    }
                    onSaved();
                    // An alias just added is not hidden by the kind the table was narrowed to.
                    if (!saved && show !== kind) setShow('all');
                    if (!saved) setFilter('');
                    await resource.reload();
                  }} />
              )}
              <Modal opened={Boolean(deleting)} onClose={() => setDeleting(undefined)} title="Delete alias" centered lockScroll={false} closeButtonProps={{ 'aria-label': 'Close' }}>
                {deleting && (blockers.length > 0 ? (
                  <Stack>
                    <Text><strong>{deleting.item.value.name}</strong> cannot be deleted while these name it. Take it out of them first:</Text>
                    <ul className="fixed-rules">{blockers.map((use) => <li key={use.label}>{use.to ? <Anchor component={Link} to={use.to} size="sm" onClick={() => setDeleting(undefined)}>{use.label}</Anchor> : use.label}</li>)}</ul>
                    <Group justify="flex-end"><Button variant="default" onClick={() => setDeleting(undefined)}>Keep it</Button></Group>
                  </Stack>
                ) : (
                  <Stack>
                    <Text>Delete the alias <strong>{deleting.item.value.name}</strong>? Nothing names it.</Text>
                    <Group justify="flex-end">
                      <Button variant="default" onClick={() => setDeleting(undefined)}>Cancel</Button>
                      <Button color="red" loading={busy} onClick={() => void remove(deleting)}>Delete alias</Button>
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
