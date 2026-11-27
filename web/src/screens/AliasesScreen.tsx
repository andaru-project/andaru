import { Alert, Anchor, Badge, Button, Group, Modal, ScrollArea, SegmentedControl, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useCallback, useEffect, useRef, useState } from 'react';
import { Link } from 'react-router';
import { ApiError, apiErrorMessage, reportError, type ApiClient } from '../api';
import { formatRefreshInterval, isInsecureHttpUrl } from '../dynamicAlias';
import { formatAddressAlias, formatPortAlias, plural } from '../format';
import { formatTimestamp, useTimeZone } from '../timezone';
import { isDynamicAddressAlias, type AddressAlias, type AliasKinds, type DynamicAliasStatus, type PortAlias, type SavedObject, type Session, type Snapshot } from '../types';
import { useResource } from '../useResource';
import { Address } from './Address';
import { AliasEditor } from './AliasEditor';
import { Loaded } from './Loaded';

interface AliasesScreenProps { api: ApiClient; session: Session; onSaved(): void; onSessionLost(): void }

type Kind = keyof AliasKinds;
// One row of the page: an alias of either kind, with the version that guards it.
export type Row = { kind: 'addresses'; item: SavedObject<AddressAlias> } | { kind: 'ports'; item: SavedObject<PortAlias> };

const kinds: Record<AddressAlias['kind'], string> = {
  host: 'Host',
  network: 'Network',
  range: 'Range',
  group: 'Address group',
  url: 'URL list',
  hostnames: 'Host names',
};
const kindLabel = (row: Row) => (row.kind === 'addresses' ? kinds[row.item.value.kind] : row.item.value.protocol === 'group' ? 'Port group' : 'Port');
const formatEntryCount = (count: number) => (count === 1 ? '1 entry' : `${count} entries`);

function DynamicStatus({ status, timeZone }: { status: DynamicAliasStatus; timeZone: string }) {
  const cleanError = (status.lastError ?? '').trim().replace(/\.+$/, '').trim();
  const hasFailure = Boolean(cleanError) || status.failures > 0;
  const summary = status.lastSuccess
    ? `${formatEntryCount(status.entries)} · Last updated ${formatTimestamp(status.lastSuccess, timeZone)}`
    : status.lastAttempt
      ? `${formatEntryCount(status.entries)} · ${hasFailure ? 'Last attempt' : 'Fetch requested'} ${formatTimestamp(status.lastAttempt, timeZone)} — matches nothing until its first fetch.`
      : 'Not fetched yet — matches nothing until its first fetch.';

  let failureLine = '';
  if (hasFailure) {
    const whenFailed = status.lastSuccess && status.lastAttempt ? ` at ${formatTimestamp(status.lastAttempt, timeZone)}` : '';
    const streak = status.failures > 0 ? ` (${plural(status.failures, 'failure')} in a row)` : '';
    const punctuated = cleanError ? (/[!?]$/.test(cleanError) ? cleanError : `${cleanError}.`) : '';
    const detail = punctuated ? `: ${punctuated}` : '.';
    const kept = status.lastSuccess ? ' Keeping the last good list.' : '';
    failureLine = `Last refresh failed${whenFailed}${streak}${detail}${kept}`;
  }

  const nameEntries = Array.isArray(status.names) ? status.names : [];
  const resolvedCount = nameEntries.filter((entry) => Array.isArray(entry.addresses) && entry.addresses.length > 0).length;
  const failedCount = nameEntries.filter((entry) => Boolean(entry.error?.trim())).length;

  return (
    <>
      <Text size="xs" c="dimmed" className="tabular">{summary}</Text>
      {failureLine && (
        <Text size="xs" className={status.failures >= 3 ? 'error-text' : 'warning-text'}>{failureLine}</Text>
      )}
      {status.kind === 'hostnames' && nameEntries.length > 0 && (
        <details>
          <summary>
            <Text component="span" size="xs" className={failedCount > 0 ? 'warning-text' : undefined}>
              Resolved names ({resolvedCount}/{nameEntries.length}{failedCount > 0 ? `, ${plural(failedCount, 'error')}` : ''})
            </Text>
          </summary>
          <ul className="fixed-rules">
            {nameEntries.map((entry) => {
              const addresses = Array.isArray(entry.addresses) ? entry.addresses : [];
              const entryError = entry.error?.trim() ?? '';
              return (
                <li key={entry.name}>
                  <strong>{entry.name}</strong>
                  {': '}
                  {addresses.length > 0 && (
                    <span className="channel-target">{addresses.join(', ')}</span>
                  )}
                  {entryError && (
                    <span className="error-text">{addresses.length > 0 ? ` (${entryError})` : entryError}</span>
                  )}
                  {addresses.length === 0 && !entryError && 'Not resolved yet'}
                  {entry.resolvedAt && (
                    <span className="tabular"> · {formatTimestamp(entry.resolvedAt, timeZone)}</span>
                  )}
                </li>
              );
            })}
          </ul>
        </details>
      )}
    </>
  );
}

// What an address alias holds, with addresses in their own type so they break only where they may.
function Holds({ row, names, status, timeZone }: { row: Row; names: Map<string, string>; status?: DynamicAliasStatus; timeZone: string }) {
  if (row.kind === 'addresses') {
    const alias = row.item.value;
    if (alias.kind === 'host' && alias.address) return <Address value={alias.address} />;
    if (alias.kind === 'network' && alias.prefix) return <Address value={alias.prefix} />;
    if (alias.kind === 'range' && alias.first && alias.last) return <Group gap={4} wrap="wrap"><Address value={alias.first} />{' '}<Text size="sm">to</Text>{' '}<Address value={alias.last} /></Group>;
    if (alias.kind === 'url') {
      return (
        <Stack gap={2} align="flex-start">
          {alias.url && <Text size="sm" className="channel-target">{alias.url}</Text>}
          {alias.refreshSeconds !== undefined && (
            <Text size="xs" c="dimmed">Refreshes every {formatRefreshInterval(alias.refreshSeconds)}</Text>
          )}
          {alias.url && isInsecureHttpUrl(alias.url) && (
            <Text size="xs" className="warning-text">Not secure: anyone on the path can change the list.</Text>
          )}
          {status && <DynamicStatus status={status} timeZone={timeZone} />}
        </Stack>
      );
    }
    if (alias.kind === 'hostnames') {
      return (
        <Stack gap={2} align="flex-start">
          <Text size="sm" className="channel-target">{(alias.names ?? []).join(', ')}</Text>
          {alias.refreshSeconds !== undefined && (
            <Text size="xs" c="dimmed">Refreshes every {formatRefreshInterval(alias.refreshSeconds)}</Text>
          )}
          {status && <DynamicStatus status={status} timeZone={timeZone} />}
        </Stack>
      );
    }
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
  const timeZone = useTimeZone();
  // The aliases with their versions, and the configuration that names them. They are three reads: when a save
  // landed between them they are read again, twice at most, so "nothing names it" is seldom said of an alias
  // something just began to name.
  const resource = useResource(useCallback(async () => {
    for (let tries = 0; ; tries++) {
      const [addresses, ports, snapshot] = await Promise.all([api.aliases('addresses'), api.aliases('ports'), api.config()]);
      if ((addresses.savedVersion === snapshot.savedVersion && ports.savedVersion === snapshot.savedVersion) || tries === 2) {
        let statusById = new Map<string, DynamicAliasStatus>();
        let statusError = '';
        if (addresses.items.some((item) => isDynamicAddressAlias(item.value))) {
          try {
            const status = await api.dynamicAliasStatus();
            statusById = new Map((status?.items ?? []).map((item) => [item.id, item]));
          } catch (requestError) {
            if (requestError instanceof ApiError && requestError.status === 401) throw requestError;
            statusError = apiErrorMessage(requestError);
          }
        }
        return { addresses, ports, snapshot, statusById, statusError };
      }
    }
  }, [api]), onSessionLost);
  const [show, setShow] = useState<'all' | Kind>('all');
  const [filter, setFilter] = useState('');
  const [editing, setEditing] = useState<Row | Kind>();
  const [deleting, setDeleting] = useState<Row>();
  const [busy, setBusy] = useState(false);
  const [refreshingId, setRefreshingId] = useState<string>();
  const [reloadingStatus, setReloadingStatus] = useState(false);
  const [error, setError] = useState<{ title: string; message: string }>();
  const [notice, setNotice] = useState('');
  const setAliasError = (message: string) => { setNotice(''); setError({ title: 'The aliases were not changed', message }); };
  const setRefreshError = (message: string) => { setNotice(''); setError({ title: 'Could not refresh alias', message }); };
  const actionInFlight = useRef(false);
  const reloadInFlight = useRef(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);

  async function remove(row: Row) {
    if (actionInFlight.current) return;
    actionInFlight.current = true;
    setBusy(true);
    setError(undefined);
    setNotice('');
    try {
      try {
        await api.deleteAlias(row.kind, row.item.value.id, row.item.version);
        onSaved();
      } catch (requestError) {
        if (requestError instanceof ApiError && requestError.status === 401) {
          onSessionLost();
          return;
        }
        if (!mounted.current) return;
        if (requestError instanceof ApiError && requestError.status === 412) setAliasError('The alias was changed by someone else. The list now shows what is saved; nothing was deleted.');
        // Refused as in use: something began to name it after this page read the configuration.
        else if (requestError instanceof ApiError && requestError.status === 422) setAliasError('Something began to name this alias, so it was not deleted. The list now shows what names it.');
        else if (requestError instanceof ApiError && requestError.status === 404) setAliasError('The alias was already deleted by someone else.');
        else reportError(requestError, onSessionLost, setAliasError);
      }
      if (!mounted.current) return;
      await resource.reload();
      if (!mounted.current) return;
      // The dialog may have been closed and another opened meanwhile: that one stays.
      setDeleting((current) => (current === row ? undefined : current));
    } finally {
      actionInFlight.current = false;
      if (mounted.current) setBusy(false);
    }
  }

  async function refresh(row: Row) {
    if (actionInFlight.current) return;
    actionInFlight.current = true;
    setRefreshingId(row.item.value.id);
    setError(undefined);
    setNotice('');
    try {
      await api.refreshDynamicAlias(row.item.value.id);
      if (!mounted.current) return;
      setNotice(`Refresh requested for ${row.item.value.name}. Its status updates once the fetch finishes.`);
      await resource.reload();
    } catch (requestError) {
      if (requestError instanceof ApiError && requestError.status === 401) {
        onSessionLost();
        return;
      }
      if (!mounted.current) return;
      if (requestError instanceof ApiError && requestError.status === 404) {
        setRefreshError('This alias was deleted or is no longer fed from a URL or host names.');
        await resource.reload();
      } else {
        reportError(requestError, onSessionLost, setRefreshError);
      }
    } finally {
      actionInFlight.current = false;
      if (mounted.current) setRefreshingId(undefined);
    }
  }

  async function reloadStatus() {
    if (actionInFlight.current || reloadInFlight.current) return;
    reloadInFlight.current = true;
    setError(undefined);
    setNotice('');
    setReloadingStatus(true);
    try {
      await resource.reload();
    } finally {
      reloadInFlight.current = false;
      if (mounted.current) setReloadingStatus(false);
    }
  }

  const hasDynamic = Boolean(resource.data?.addresses.items.some((item) => isDynamicAddressAlias(item.value)));
  const actionBusy = busy || refreshingId !== undefined;

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div><Title order={2}>Aliases</Title><Text c="dimmed">Names for addresses and for ports, which rules and NAT refer to. Change an alias and everything that names it follows.</Text></div>
        {resource.data && (hasDynamic || canWrite) && (
          <Group gap="xs">
            {hasDynamic && (
              <Button size="xs" variant="default" loading={reloadingStatus} disabled={actionBusy} onClick={() => void reloadStatus()}>Reload status</Button>
            )}
            {canWrite && (
              <>
                <Button size="xs" onClick={() => setEditing('addresses')}>Add address alias</Button>
                <Button size="xs" onClick={() => setEditing('ports')}>Add port alias</Button>
              </>
            )}
          </Group>
        )}
      </Group>
      {error && <Alert color="red" role="alert" title={error.title} withCloseButton closeButtonLabel="Dismiss error" onClose={() => setError(undefined)}>{error.message}</Alert>}
      {notice && <Alert color="blue" role="status" title="Refresh requested" withCloseButton closeButtonLabel="Dismiss refresh notice" onClose={() => setNotice('')}>{notice}</Alert>}
      <Loaded resource={resource} what="aliases">
        {({ addresses, ports, snapshot, statusById, statusError }) => {
          const rows: Row[] = [
            ...addresses.items.map((item): Row => ({ kind: 'addresses', item })),
            ...ports.items.map((item): Row => ({ kind: 'ports', item })),
          ].sort((a, b) => a.item.value.name.localeCompare(b.item.value.name));
          const names = new Map(rows.map((row) => [row.item.value.id, row.item.value.name]));
          const statusFor = (row: Row): DynamicAliasStatus | undefined => {
            if (row.kind !== 'addresses' || !isDynamicAddressAlias(row.item.value)) return undefined;
            const raw = statusById.get(row.item.value.id);
            return raw && raw.kind === row.item.value.kind ? raw : undefined;
          };
          // Every word of the filter must be somewhere in what the row shows.
          const words = filter.toLowerCase().split(/\s+/).filter(Boolean);
          const matches = (row: Row) => {
            const status = statusFor(row);
            const nameStatusWords = (Array.isArray(status?.names) ? status.names : [])
              .flatMap((entry) => [...(Array.isArray(entry.addresses) ? entry.addresses : []), entry.error ?? '']);
            const text = [row.item.value.name, row.item.value.description ?? '', kindLabel(row), holds(row, names) ?? '', status?.lastError ?? '', ...nameStatusWords].join(' ').toLowerCase();
            return words.every((word) => text.includes(word));
          };
          const shown = rows.filter((row) => (show === 'all' || row.kind === show) && matches(row));
          const usedBy = (row: Row) => uses(row.item.value.id, rows, snapshot);
          const blockers = deleting ? usedBy(deleting) : [];
          const noun = (row: Row) => (row.kind === 'addresses' ? 'address alias' : 'port alias');
          return (
            <Stack>
              {statusError && (
                <Alert color="orange" role="alert" title="Dynamic alias status unavailable">{statusError}</Alert>
              )}
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
                        const dynamic = row.kind === 'addresses' && isDynamicAddressAlias(row.item.value);
                        const status = statusFor(row);
                        return (
                          <Table.Tr key={alias.id}>
                            <Table.Td data-label="Name"><Text size="sm" fw={600}>{alias.name}</Text>{alias.description && <Text size="sm" c="dimmed">{alias.description}</Text>}</Table.Td>
                            <Table.Td data-label="Kind"><Badge variant={row.kind === 'addresses' ? 'default' : 'light'} color="gray" className="alias">{kindLabel(row)}</Badge></Table.Td>
                            <Table.Td data-label="Holds"><Holds row={row} names={names} status={status} timeZone={timeZone} /></Table.Td>
                            <Table.Td data-label="Used by">
                              {used.length === 0 ? <Text size="sm" c="dimmed">nothing</Text> : (
                                <details><summary>{used.length === 1 ? '1 use' : `${used.length} uses`}</summary><ul className="fixed-rules">{used.map((use) => <li key={use.label}>{use.to ? <Anchor component={Link} to={use.to} size="sm">{use.label}</Anchor> : use.label}</li>)}</ul></details>
                              )}
                            </Table.Td>
                            {canWrite && (
                              <Table.Td>
                                <Group gap="xs" wrap="wrap">
                                  {dynamic && (
                                    <Button size="compact-xs" variant="default" loading={refreshingId === alias.id} disabled={actionBusy} aria-label={`Refresh ${noun(row)} ${alias.name}`} onClick={() => void refresh(row)}>Refresh</Button>
                                  )}
                                  <Button size="compact-xs" variant="default" disabled={actionBusy} aria-label={`Edit ${noun(row)} ${alias.name}`} onClick={() => setEditing(row)}>Edit</Button>
                                  <Button size="compact-xs" variant="subtle" color="red" disabled={actionBusy} aria-label={`Delete ${noun(row)} ${alias.name}`} onClick={() => setDeleting(row)}>Delete</Button>
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
                <AliasEditor key={typeof editing === 'string' ? editing : editing.item.value.id} api={api} editing={editing} rows={rows} snapshot={snapshot} onClose={() => setEditing(undefined)} onSessionLost={onSessionLost} onAbandoned={setAliasError} onRefused={() => void resource.reload()}
                  onSave={async (kind, alias, saved) => {
                    try {
                      await api.saveAlias(kind, alias, saved);
                    } catch (requestError) {
                      // The alias may have changed underneath, or been saved with the answer lost.
                      if (!(requestError instanceof ApiError && requestError.status === 401)) await resource.reload();
                      throw requestError;
                    }
                    setError(undefined);
                    setNotice('');
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
