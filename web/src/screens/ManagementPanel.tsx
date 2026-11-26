import { Alert, Anchor, Badge, Button, Drawer, Fieldset, Group, Paper, Select, Stack, Text, Title } from '@mantine/core';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Link, useInRouterContext } from 'react-router';
import { ApiClient, ApiError, fieldErrors, reportError, saveFailure } from '../api';
import type { ManagementConfig, SavedLink, SavedObject, Segment, Snapshot } from '../types';
import { AliasMultiSelect } from './AliasPicker';
import { ZoneBadge } from './ZoneBadge';

interface ManagementPanelProps {
  api: ApiClient;
  snapshot: Snapshot;
  canWrite: boolean;
  onSaved(): void;
  onSessionLost(): void;
}

const fieldPattern = /^\/(?:management\/)?(segment|allowedSources)(?:\/|$)/;

function AliasesLink({ children }: { children: string }) {
  const inRouter = useInRouterContext();
  return inRouter
    ? <Anchor component={Link} to="/firewall/aliases" size="sm">{children}</Anchor>
    : <Anchor href="/firewall/aliases" size="sm">{children}</Anchor>;
}

function segmentSummaryLabel(segment: Segment, zonesById: Map<string, string>, linksById: Map<string, SavedLink>): string {
  const link = linksById.get(segment.link);
  const zone = zonesById.get(segment.zone);
  const parts = [zone, link && !link.enabled ? 'disabled' : undefined].filter(Boolean);
  return parts.length > 0 ? `${segment.name} (${parts.join(' · ')})` : segment.name;
}

interface ManagementEditorProps {
  api: ApiClient;
  initial: SavedObject<ManagementConfig> | null;
  snapshot: Snapshot;
  onClose(): void;
  onSaved(): void;
  onSessionLost(): void;
}

function ManagementEditor({ api, initial, snapshot, onClose, onSaved, onSessionLost }: ManagementEditorProps) {
  const [segmentId, setSegmentId] = useState(initial?.value.segment ?? snapshot.value.segments[0]?.id ?? '');
  const [allowedSources, setAllowedSources] = useState<string[]>(initial?.value.allowedSources ?? []);
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);

  const open = useRef(true);
  useEffect(() => { open.current = true; return () => { open.current = false; }; }, []);
  const failureRef = useRef<HTMLDivElement>(null);
  useEffect(() => { if (failure) failureRef.current?.scrollIntoView?.({ block: 'nearest' }); }, [failure]);

  const zonesById = new Map(snapshot.value.zones.map((z) => [z.id, z.name]));
  const linksById = new Map(snapshot.value.links.map((l) => [l.id, l]));
  const segmentOptions = snapshot.value.segments.map((seg) => ({
    value: seg.id,
    label: segmentSummaryLabel(seg, zonesById, linksById),
  }));
  if (segmentId && !segmentOptions.some((o) => o.value === segmentId)) {
    segmentOptions.push({ value: segmentId, label: `${segmentId} (missing)` });
  }
  const addressOptions = (snapshot.value.aliases?.addresses ?? []).map((a) => ({ value: a.id, label: a.name }));

  const selectedSegment = snapshot.value.segments.find((s) => s.id === segmentId);
  const selectedLink = selectedSegment ? linksById.get(selectedSegment.link) : undefined;
  const movingSegment = Boolean(initial?.value.segment && segmentId && initial.value.segment !== segmentId);
  const dhcpSegment = selectedSegment?.addressing.ipv4.mode === 'dhcp';
  const disabledSegment = Boolean(selectedLink && !selectedLink.enabled);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!segmentId) {
      setErrors(new Map([['segment', 'Choose a management segment.']]));
      setFailure('');
      return;
    }
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    const payload: ManagementConfig = {
      segment: segmentId,
      ...(allowedSources.length > 0 ? { allowedSources } : {}),
    };
    try {
      await api.saveManagement(payload, initial?.version ?? '0');
      onSaved();
      if (open.current) onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (!open.current) return;
      const fields = fieldErrors(error, fieldPattern);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'management access'));
    } finally {
      if (open.current) setBusy(false);
    }
  }

  return (
    <Drawer
      opened
      onClose={onClose}
      position="right"
      size="md"
      title={initial ? 'Edit management access' : 'Configure management access'}
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close' }}
    >
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert ref={failureRef} color="red" role="alert" title="Management access was not saved">{failure}</Alert>}
          <Fieldset variant="unstyled" disabled={busy}>
            <Stack>
              <Select
                label="Management segment"
                description="Andaru enables the firewall, NAT, and IP forwarding only when a management segment is configured."
                placeholder="Choose a segment"
                required
                searchable
                data={segmentOptions}
                value={segmentId || null}
                error={errors.get('segment')}
                onChange={(value) => setSegmentId(value ?? '')}
              />
              {movingSegment && (
                <Alert color="yellow" role="status" title="Moving management segment">
                  Moving management to another segment takes effect when you apply changes. Verify you can reach the new segment before applying so you are not locked out.
                </Alert>
              )}
              {disabledSegment && (
                <Alert color="orange" role="status" title="Selected link is disabled">
                  This segment&apos;s interface is currently disabled. Enable the interface before applying, or firewall rules and NAT will remain inactive.
                </Alert>
              )}
              {dhcpSegment && (
                <Alert color="yellow" role="status" title="DHCP management segment">
                  A DHCP management segment does not have a fixed address in advance; if DHCP is unavailable you will need local console access.
                </Alert>
              )}
              <AliasMultiSelect
                api={api}
                aliasType="addresses"
                label="Allowed sources"
                description="Restrict SSH and web UI access on the management segment to these address aliases. Leave empty to allow any address on the management segment."
                placeholder="Any address on the management segment"
                initialOptions={addressOptions}
                value={allowedSources}
                error={errors.get('allowedSources')}
                clearable
                onSessionLost={onSessionLost}
                onChange={setAllowedSources}
              />
              <Text size="xs" c="dimmed">
                Manage address objects under{' '}
                <Anchor href="/firewall/aliases" target="_blank" rel="noopener noreferrer" size="xs">Firewall ▸ Aliases</Anchor>.
              </Text>
            </Stack>
          </Fieldset>
          <Group justify="flex-end" gap="xs" className="drawer-actions">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={busy}>Save management access</Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}

export function ManagementPanel({ api, snapshot, canWrite, onSaved, onSessionLost }: ManagementPanelProps) {
  const [opening, setOpening] = useState(false);
  const [openError, setOpenError] = useState('');
  const [editing, setEditing] = useState<{ current: SavedObject<ManagementConfig> | null; freshSnapshot: Snapshot } | null>(null);

  const mgmt = snapshot.value.management;
  const segmentsById = new Map(snapshot.value.segments.map((s) => [s.id, s]));
  const zonesById = new Map(snapshot.value.zones.map((z) => [z.id, z]));
  const aliasesById = new Map((snapshot.value.aliases?.addresses ?? []).map((a) => [a.id, a.name]));

  const mgmtSegment = mgmt ? segmentsById.get(mgmt.segment) : undefined;
  const mgmtZone = mgmtSegment ? zonesById.get(mgmtSegment.zone) : undefined;
  const sources = mgmt?.allowedSources ?? [];

  async function openEditor() {
    if (opening) return;
    setOpening(true);
    setOpenError('');
    try {
      const [current, freshSnapshot] = await Promise.all([api.management(), api.config()]);
      setEditing({ current, freshSnapshot });
    } catch (error) {
      reportError(error, onSessionLost, setOpenError);
    } finally {
      setOpening(false);
    }
  }

  const canConfigure = canWrite && (snapshot.value.segments.length > 0 || Boolean(mgmt));

  return (
    <Paper withBorder p="md">
      <Stack gap="sm">
        <Group justify="space-between" align="flex-end" wrap="wrap">
          <div>
            <Title order={3} size="h4">Management access</Title>
            <Text size="sm" c="dimmed">Which segment administers Andaru, and which address aliases may reach SSH and the web UI there.</Text>
          </div>
          {canConfigure && (
            <Button size="xs" variant="default" loading={opening} onClick={() => void openEditor()}>
              {mgmt ? 'Edit management access' : 'Configure management access'}
            </Button>
          )}
        </Group>

        {openError && <Alert color="red" role="alert" title="Could not open management access">{openError}</Alert>}

        {!mgmt ? (
          <Alert color="orange" role="status" title="No management segment is set">
            Firewall rules, NAT, and IP forwarding stay inactive until a management segment is configured.
          </Alert>
        ) : (
          <Stack gap="xs">
            <Group gap="xs" wrap="wrap">
              <Text size="sm" c="dimmed">Segment:</Text>
              {mgmtSegment ? (
                <>
                  <Text size="sm" fw={600}>{mgmtSegment.name}</Text>
                  {mgmtZone && <ZoneBadge name={mgmtZone.name} color={mgmtZone.color} />}
                </>
              ) : (
                <Text size="sm" c="red" ff="monospace">{mgmt.segment} (missing)</Text>
              )}
            </Group>
            <Group gap="xs" wrap="wrap">
              <Text size="sm" c="dimmed">Allowed sources:</Text>
              {sources.length === 0 ? (
                <Text size="sm">Any address on the management segment</Text>
              ) : (
                <>
                  {sources.map((id) => {
                    const name = aliasesById.get(id);
                    return (
                      <Badge key={id} variant="default" className="alias" tt="none" color={name ? undefined : 'red'}>
                        {name ?? `${id} (missing)`}
                      </Badge>
                    );
                  })}
                  <AliasesLink>Manage aliases</AliasesLink>
                </>
              )}
            </Group>
          </Stack>
        )}
      </Stack>

      {editing && (
        <ManagementEditor
          key={`${editing.current?.version ?? '0'}:${editing.freshSnapshot.savedVersion}`}
          api={api}
          initial={editing.current}
          snapshot={editing.freshSnapshot}
          onClose={() => setEditing(null)}
          onSaved={onSaved}
          onSessionLost={onSessionLost}
        />
      )}
    </Paper>
  );
}
