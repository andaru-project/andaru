import { Alert, Anchor, Button, ColorSwatch, Drawer, Fieldset, Group, Modal, ScrollArea, Stack, Table, Text, Textarea, TextInput, Title, Tooltip, UnstyledButton } from '@mantine/core';
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { Link } from 'react-router';
import { ApiClient, ApiError, fieldErrors, reportError, saveFailure } from '../api';
import { useResource } from '../useResource';
import type { SavedObject, Session, Snapshot, Zone, ZoneColor } from '../types';
import { zoneColor, zoneColors } from '../zones';
import { Loaded } from './Loaded';
import { ZoneBadge, ZonePill } from './ZoneBadge';

interface ZonesScreenProps {
  api: ApiClient;
  session: Session;
  onSaved(): void;
  onSessionLost(): void;
}

interface Use {
  label: string;
  to?: string;
}

const colorLabel: Record<ZoneColor, string> = {
  green: 'Green',
  red: 'Red',
  blue: 'Blue',
  orange: 'Orange',
  teal: 'Teal',
  pink: 'Pink',
  cyan: 'Cyan',
  lime: 'Lime',
  violet: 'Violet',
  indigo: 'Indigo',
};

const zoneFieldPattern = /^\/(name|description|color)(?:\/|$)/;

// Where a saved zone is named across interfaces, firewall rules, and NAT.
function zoneUses(id: string, snapshot: Snapshot): Use[] {
  const found: Use[] = [];
  for (const segment of snapshot.value.segments) {
    if (segment.zone === id) found.push({ label: `Interface ${segment.name}`, to: '/network/interfaces' });
  }
  (snapshot.value.rules ?? []).forEach((rule, index) => {
    if (rule.from.zones.includes(id) || rule.to.zones.includes(id)) {
      found.push({ label: `Rule ${index + 1} (${rule.name})`, to: '/firewall/rules' });
    }
  });
  (snapshot.value.nat?.portForwards ?? []).forEach((pf, index) => {
    if (pf.ingressZones.includes(id)) {
      found.push({ label: `Port forward ${index + 1} (${pf.name})`, to: '/firewall/nat' });
    }
  });
  (snapshot.value.nat?.oneToOne ?? []).forEach((entry, index) => {
    if (entry.zone === id) {
      found.push({ label: `1:1 NAT ${index + 1} (${entry.name})`, to: '/firewall/nat' });
    }
  });
  return found;
}

function ZoneEditor({
  saved,
  existingZones,
  onClose,
  onSessionLost,
  onAbandoned,
  onRefused,
  onSave,
}: {
  saved?: SavedObject<Zone>;
  existingZones: Array<SavedObject<Zone>>;
  onClose(): void;
  onSessionLost(): void;
  onAbandoned(message: string): void;
  onRefused(): void;
  onSave(zone: Omit<Zone, 'id'>, saved?: SavedObject<Zone>): Promise<void>;
}) {
  const [name, setName] = useState(saved?.value.name ?? '');
  const [description, setDescription] = useState(saved?.value.description ?? '');
  const [color, setColor] = useState<ZoneColor | ''>(saved?.value.color ?? '');
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);

  const open = useRef(true);
  useEffect(() => { open.current = true; return () => { open.current = false; }; }, []);
  const failureRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!failure) return;
    failureRef.current?.scrollIntoView?.({ block: 'nearest' });
    if (errors.size === 0 || errors.has('')) failureRef.current?.focus?.();
  }, [failure, errors]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    setFailure('');
    const trimmed = name.trim();
    if (!trimmed) {
      setErrors(new Map([['name', 'A zone needs a name.']]));
      return;
    }
    if (existingZones.some((z) => z.value.id !== saved?.value.id && z.value.name === trimmed)) {
      setErrors(new Map([['name', 'Another zone has this name.']]));
      onRefused();
      return;
    }
    setBusy(true);
    setErrors(new Map());
    const body: Omit<Zone, 'id'> = {
      name: trimmed,
      ...(description.trim() ? { description: description.trim() } : {}),
      ...(color ? { color } : {}),
    };
    try {
      await onSave(body, saved);
      if (open.current) onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      if (!open.current) return onAbandoned(saveFailure(error, new Map(), 'zone'));
      const fields = fieldErrors(error, zoneFieldPattern);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'zone'));
    } finally {
      if (open.current) setBusy(false);
    }
  }

  const previewName = name.trim() || 'Preview';
  const autoShade = zoneColor(previewName);

  return (
    <Drawer
      opened
      onClose={onClose}
      position="right"
      size="md"
      title={saved ? `Edit zone: ${saved.value.name}` : 'Add zone'}
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close' }}
    >
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert ref={failureRef} tabIndex={-1} color="red" role="alert" title="The zone was not saved">{failure}</Alert>}
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
              <Stack gap="xs">
                <Group justify="space-between" align="center">
                  <Text size="sm" fw={500}>Colour</Text>
                  <ZonePill name={previewName} color={color || undefined} />
                </Group>
                <Text size="xs" c="dimmed">
                  Choose a badge colour for this zone across interfaces, rules, NAT, and traffic graphs, or leave on automatic ({colorLabel[autoShade]}).
                </Text>
                <Group gap="xs" role="radiogroup" aria-label="Zone colour">
                  <Button
                    type="button"
                    size="compact-xs"
                    variant={color === '' ? 'filled' : 'default'}
                    role="radio"
                    aria-checked={color === ''}
                    onClick={() => setColor('')}
                  >
                    Automatic
                  </Button>
                  {zoneColors.map((shade) => {
                    const selected = color === shade;
                    return (
                      <Tooltip key={shade} label={colorLabel[shade]}>
                        <UnstyledButton
                          type="button"
                          role="radio"
                          aria-checked={selected}
                          aria-label={colorLabel[shade]}
                          onClick={() => setColor(shade)}
                          style={{
                            padding: 2,
                            borderRadius: 999,
                            outline: selected ? `2px solid var(--mantine-color-${shade}-filled)` : '2px solid transparent',
                            outlineOffset: 2,
                          }}
                        >
                          <ColorSwatch color={`var(--mantine-color-${shade}-6)`} size={22} />
                        </UnstyledButton>
                      </Tooltip>
                    );
                  })}
                </Group>
                {errors.get('color') && <Text size="xs" className="error-text">{errors.get('color')}</Text>}
              </Stack>
            </Stack>
          </Fieldset>
          <Group justify="flex-end" className="drawer-actions">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button loading={busy} type="submit">{saved ? 'Save zone' : 'Add zone'}</Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}

// Firewall zones: security boundaries grouping interfaces for rules and NAT.
export function ZonesScreen({ api, session, onSaved, onSessionLost }: ZonesScreenProps) {
  const canWrite = session.capabilities.includes('configuration.write');
  const load = useCallback(async () => {
    const [zones, snapshot] = await Promise.all([api.zones(), api.config()]);
    return { zones, snapshot };
  }, [api]);
  const resource = useResource(load, onSessionLost);

  const [filter, setFilter] = useState('');
  const [editing, setEditing] = useState<SavedObject<Zone> | 'new'>();
  const [deleting, setDeleting] = useState<SavedObject<Zone>>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);

  async function remove(item: SavedObject<Zone>) {
    if (busy) return;
    setBusy(true);
    setError('');
    try {
      await api.deleteZone(item.value.id, item.version);
      onSaved();
    } catch (requestError) {
      if (requestError instanceof ApiError && requestError.status === 412) {
        setError(`Zone ${item.value.name} changed elsewhere. The list now shows what is saved.`);
      } else {
        reportError(requestError, onSessionLost, setError);
      }
      if (requestError instanceof ApiError && requestError.status === 401) return;
    }
    await resource.reload();
    if (mounted.current) {
      setDeleting(undefined);
      setBusy(false);
    }
  }

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div>
          <Title order={2}>Zones</Title>
          <Text c="dimmed">Security boundaries grouping interfaces for firewall rules and NAT. Each zone keeps its badge colour across every screen.</Text>
        </div>
        <Group gap="xs">
          <Button size="xs" variant="default" loading={resource.loading} onClick={() => void resource.reload()}>Refresh</Button>
          {canWrite && <Button size="xs" onClick={() => setEditing('new')}>Add zone</Button>}
        </Group>
      </Group>

      {error && <Alert color="red" role="alert" title="The zones were not changed" withCloseButton onClose={() => setError('')}>{error}</Alert>}

      <Loaded resource={resource} what="zones">
        {({ zones, snapshot }) => {
          const sorted = [...zones.items].sort((a, b) => a.value.name.localeCompare(b.value.name, undefined, { sensitivity: 'base' }));
          const words = filter.trim().toLowerCase().split(/\s+/).filter(Boolean);
          const rows = sorted.map((item) => ({ item, uses: zoneUses(item.value.id, snapshot) }));
          const shown = words.length === 0 ? rows : rows.filter(({ item, uses }) => {
            const z = item.value;
            const autoLabel = colorLabel[zoneColor(z.name, z.color)];
            const haystack = [z.name, z.description ?? '', z.color ?? 'automatic', autoLabel, ...uses.map((u) => u.label)].join(' ').toLowerCase();
            return words.every((w) => haystack.includes(w));
          });
          const blockers = deleting ? zoneUses(deleting.value.id, snapshot) : [];

          return (
            <Stack>
              {sorted.length > 0 && (
                <TextInput
                  label="Filter zones"
                  placeholder="Name, description, colour, or interface"
                  value={filter}
                  onChange={(event) => setFilter(event.currentTarget.value)}
                />
              )}

              {shown.length === 0 ? (
                <Alert role="status" title="No zones">
                  {sorted.length === 0 ? 'No zone is saved yet. Add one here or assign a new zone when editing an interface.' : 'No zone matches the filter.'}
                </Alert>
              ) : (
                <ScrollArea type="auto">
                  <Table className="data-table" verticalSpacing="xs">
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>Zone</Table.Th>
                        <Table.Th>Colour</Table.Th>
                        <Table.Th>Used by</Table.Th>
                        {canWrite && <Table.Th><span className="sr-only">Actions</span></Table.Th>}
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {shown.map(({ item, uses }) => {
                        const z = item.value;
                        const resolved = zoneColor(z.name, z.color);
                        return (
                          <Table.Tr key={z.id}>
                            <Table.Td>
                              <Stack gap={2} align="flex-start">
                                <ZoneBadge name={z.name} color={z.color} />
                                {z.description && <Text size="xs" c="dimmed">{z.description}</Text>}
                              </Stack>
                            </Table.Td>
                            <Table.Td>
                              {z.color ? (
                                <Text size="sm">{colorLabel[z.color]}</Text>
                              ) : (
                                <Text size="sm" c="dimmed">Automatic ({colorLabel[resolved]})</Text>
                              )}
                            </Table.Td>
                            <Table.Td>
                              {uses.length === 0 ? (
                                <Text size="sm" c="dimmed">Not used</Text>
                              ) : (
                                <Group gap="xs" wrap="wrap">
                                  {uses.map((u) => (
                                    u.to
                                      ? <Anchor key={u.label} component={Link} to={u.to} size="sm">{u.label}</Anchor>
                                      : <Text key={u.label} size="sm">{u.label}</Text>
                                  ))}
                                </Group>
                              )}
                            </Table.Td>
                            {canWrite && (
                              <Table.Td>
                                <Group gap="xs" justify="flex-end" wrap="nowrap">
                                  <Button size="compact-xs" variant="subtle" aria-label={`Edit zone ${z.name}`} onClick={() => setEditing(item)}>Edit</Button>
                                  <Button size="compact-xs" variant="subtle" color="red" aria-label={`Delete zone ${z.name}`} onClick={() => setDeleting(item)}>Delete</Button>
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
                <ZoneEditor
                  key={editing === 'new' ? 'new' : `${editing.value.id}:${editing.version}`}
                  saved={editing === 'new' ? undefined : editing}
                  existingZones={zones.items}
                  onClose={() => setEditing(undefined)}
                  onSessionLost={onSessionLost}
                  onAbandoned={setError}
                  onRefused={() => void resource.reload()}
                  onSave={async (zone, saved) => {
                    try {
                      await api.saveZone(zone, saved);
                    } catch (requestError) {
                      if (!(requestError instanceof ApiError && requestError.status === 401)) await resource.reload();
                      throw requestError;
                    }
                    onSaved();
                    if (!saved) setFilter('');
                    await resource.reload();
                  }}
                />
              )}

              <Modal
                opened={Boolean(deleting)}
                onClose={() => setDeleting(undefined)}
                title="Delete zone"
                centered
                lockScroll={false}
                closeButtonProps={{ 'aria-label': 'Close' }}
              >
                {deleting && (blockers.length > 0 ? (
                  <Stack>
                    <Text><strong>{deleting.value.name}</strong> cannot be deleted while these name it. Take it out of them first:</Text>
                    <ul className="fixed-rules">
                      {blockers.map((use) => (
                        <li key={use.label}>
                          {use.to ? <Anchor component={Link} to={use.to} size="sm" onClick={() => setDeleting(undefined)}>{use.label}</Anchor> : use.label}
                        </li>
                      ))}
                    </ul>
                    <Group justify="flex-end">
                      <Button variant="default" onClick={() => setDeleting(undefined)}>Keep it</Button>
                    </Group>
                  </Stack>
                ) : (
                  <Stack>
                    <Text>Delete the zone <strong>{deleting.value.name}</strong>? Nothing names it.</Text>
                    <Group justify="flex-end">
                      <Button variant="default" disabled={busy} onClick={() => setDeleting(undefined)}>Cancel</Button>
                      <Button color="red" loading={busy} onClick={() => void remove(deleting)}>Delete zone</Button>
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
