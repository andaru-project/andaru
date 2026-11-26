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
  onRefresh?(): void;
  onSessionLost(): void;
}

const fieldPattern = /^\/(?:management\/)?(segment|allowedSources)(?:\/|$)/;

function AliasesLink({ children }: { children: string }) {
  const inRouter = useInRouterContext();
  return inRouter
    ? <Anchor component={Link} to="/firewall/aliases" size="sm">{children}</Anchor>
    : <Anchor href="/firewall/aliases" size="sm">{children}</Anchor>;
}

function isLinkChainEnabled(linkId: string, linksById: Map<string, SavedLink>): boolean {
  const seen = new Set<string>();
  let current = linksById.get(linkId);
  while (current) {
    if (!current.enabled) return false;
    if (seen.has(current.id)) break;
    seen.add(current.id);
    current = current.parent ? linksById.get(current.parent) : undefined;
  }
  return true;
}

function segmentSummaryLabel(segment: Segment, zonesById: Map<string, string>, linksById: Map<string, SavedLink>): string {
  const zone = zonesById.get(segment.zone) ?? segment.zone;
  const parts = [zone, !isLinkChainEnabled(segment.link, linksById) ? 'disabled' : undefined].filter(Boolean);
  return parts.length > 0 ? `${segment.name} (${parts.join(' · ')})` : segment.name;
}

interface ManagementEditorProps {
  api: ApiClient;
  initial: SavedObject<ManagementConfig> | null;
  snapshot: Snapshot;
  onClose(): void;
  onSaved(): void;
  onRefresh?(): void;
  onAbandoned(message: string): void;
  onSessionLost(): void;
}

function ManagementEditor({ api, initial, snapshot, onClose, onSaved, onRefresh, onAbandoned, onSessionLost }: ManagementEditorProps) {
  const [segmentId, setSegmentId] = useState(initial?.value.segment ?? '');
  const [allowedSources, setAllowedSources] = useState<string[]>(() => Array.from(new Set(initial?.value.allowedSources ?? [])));
  const [errors, setErrors] = useState(new Map<string, string>());
  const [errorFocusTick, setErrorFocusTick] = useState(0);
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);

  const open = useRef(true);
  const submitting = useRef(false);
  useEffect(() => { open.current = true; return () => { open.current = false; }; }, []);
  const failureRef = useRef<HTMLDivElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  useEffect(() => { if (failure) failureRef.current?.scrollIntoView?.({ block: 'nearest' }); }, [failure]);
  useEffect(() => {
    if (errorFocusTick === 0) return;
    const invalid = formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]:not(:disabled)');
    invalid?.scrollIntoView?.({ block: 'nearest' });
    invalid?.focus?.();
  }, [errorFocusTick]);

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
  const movingSegment = Boolean(initial?.value.segment && segmentId && initial.value.segment !== segmentId);
  const disabledSegment = Boolean(selectedSegment && !isLinkChainEnabled(selectedSegment.link, linksById));
  const dynamicAddressSegment = Boolean(
    selectedSegment && (selectedSegment.addressing.ipv4.mode === 'dhcp' || selectedSegment.addressing.ipv4.mode === 'ppp'),
  );
  const unaddressedSegment = Boolean(
    selectedSegment && selectedSegment.addressing.ipv4.mode === 'none' && selectedSegment.addressing.ipv6.mode === 'none',
  );

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;
    if (!segmentId) {
      setErrors(new Map([['segment', 'Choose a management segment.']]));
      setFailure('');
      setErrorFocusTick((t) => t + 1);
      return;
    }
    submitting.current = true;
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    const uniqueSources = Array.from(new Set(allowedSources));
    const payload: ManagementConfig = {
      segment: segmentId,
      ...(uniqueSources.length > 0 ? { allowedSources: uniqueSources } : {}),
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
      if (error instanceof ApiError && error.status === 412) {
        onRefresh?.();
      }
      if (!open.current) {
        onAbandoned(saveFailure(error, new Map(), 'management access'));
        return;
      }
      const fields = fieldErrors(error, fieldPattern);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'management access'));
      if (fields.size > 0) setErrorFocusTick((t) => t + 1);
    } finally {
      submitting.current = false;
      if (open.current) setBusy(false);
    }
  }

  return (
    <Drawer
      opened
      onClose={() => { if (!submitting.current) onClose(); }}
      closeOnClickOutside={!busy}
      closeOnEscape={!busy}
      position="right"
      size="md"
      title={initial ? 'Edit management access' : 'Configure management access'}
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close', disabled: busy }}
    >
      <form ref={formRef} onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert ref={failureRef} color="red" role="alert" title="Management access was not saved">{failure}</Alert>}
          <Fieldset variant="unstyled" disabled={busy}>
            <Stack>
              <Select
                data-autofocus
                label="Management segment"
                description="Andaru enables the packet filter, NAT, and IP forwarding only when a management segment is configured."
                placeholder="Choose a segment"
                required
                searchable
                allowDeselect={false}
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
                  This segment&apos;s interface (or its parent link) is disabled. Applying changes will be refused until the link is enabled.
                </Alert>
              )}
              {dynamicAddressSegment && (
                <Alert color="yellow" role="status" title="Dynamic management address">
                  A DHCP or PPPoE management segment does not have a fixed address in advance; if upstream addressing fails you will need local console access.
                </Alert>
              )}
              {unaddressedSegment && (
                <Alert color="orange" role="status" title="Unaddressed management segment">
                  This segment has neither IPv4 nor IPv6 addressing configured; you may not be able to reach Andaru over it.
                </Alert>
              )}
              <AliasMultiSelect
                api={api}
                aliasType="addresses"
                label="Allowed SSH sources"
                description="Restricts the fixed SSH anti-lockout rule on the management segment to these address aliases. Leave empty to allow SSH from any address on the management segment. Web UI access is governed by firewall rules."
                placeholder="Any address on the management segment"
                initialOptions={addressOptions}
                value={allowedSources}
                error={errors.get('allowedSources')}
                clearable
                onSessionLost={onSessionLost}
                onChange={(next) => setAllowedSources(Array.from(new Set(next)))}
              />
              {allowedSources.length > 0 && (
                <Alert color="yellow" role="status" title="Verify SSH access before applying">
                  Make sure your current client address and IP family are covered by the selected aliases, or SSH anti-lockout access will be dropped on apply.
                </Alert>
              )}
              <Text size="xs" c="dimmed">
                Need a new address alias?{' '}
                <Anchor href="/firewall/aliases" target="_blank" rel="noopener noreferrer" size="xs">Open Firewall ▸ Aliases in a new tab</Anchor>.
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

export function ManagementPanel({ api, snapshot, canWrite, onSaved, onRefresh, onSessionLost }: ManagementPanelProps) {
  const [opening, setOpening] = useState(false);
  const [openError, setOpenError] = useState('');
  const [editing, setEditing] = useState<{ current: SavedObject<ManagementConfig> | null; freshSnapshot: Snapshot } | null>(null);
  const openingRef = useRef(false);
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const mgmt = snapshot.value.management;
  const segmentsById = new Map(snapshot.value.segments.map((s) => [s.id, s]));
  const zonesById = new Map(snapshot.value.zones.map((z) => [z.id, z]));
  const aliasesById = new Map((snapshot.value.aliases?.addresses ?? []).map((a) => [a.id, a.name]));

  const mgmtSegment = mgmt ? segmentsById.get(mgmt.segment) : undefined;
  const mgmtZone = mgmtSegment ? zonesById.get(mgmtSegment.zone) : undefined;
  const sources = mgmt?.allowedSources ?? [];

  async function openEditor() {
    if (openingRef.current) return;
    openingRef.current = true;
    setOpening(true);
    setOpenError('');
    try {
      let current: SavedObject<ManagementConfig> | null = null;
      let freshSnapshot!: Snapshot;
      for (let attempt = 0; attempt < 2; attempt++) {
        [current, freshSnapshot] = await Promise.all([api.management(), api.config()]);
        if (JSON.stringify(current?.value ?? null) === JSON.stringify(freshSnapshot.value.management ?? null)) break;
      }
      if (!mountedRef.current) return;
      setEditing({ current, freshSnapshot });
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (!mountedRef.current) return;
      reportError(error, onSessionLost, setOpenError);
    } finally {
      openingRef.current = false;
      if (mountedRef.current) setOpening(false);
    }
  }

  const canConfigure = canWrite && (snapshot.value.segments.length > 0 || Boolean(mgmt));

  return (
    <Paper withBorder p="md">
      <Stack gap="sm">
        <Group justify="space-between" align="flex-end" wrap="wrap">
          <div>
            <Title order={3} size="h4">Management access</Title>
            <Text size="sm" c="dimmed">Which segment administers Andaru, and which address aliases are permitted by the fixed SSH anti-lockout rule.</Text>
          </div>
          {canConfigure && (
            <Button size="xs" variant="default" loading={opening} onClick={() => void openEditor()}>
              {mgmt ? 'Edit management access' : 'Configure management access'}
            </Button>
          )}
        </Group>

        {openError && <Alert color="red" role="alert" title="Management access error" withCloseButton onClose={() => setOpenError('')}>{openError}</Alert>}

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
                  <ZoneBadge name={mgmtZone?.name ?? mgmtSegment.zone} color={mgmtZone?.color} />
                </>
              ) : (
                <Text size="sm" className="error-text" ff="monospace">{mgmt.segment} (missing)</Text>
              )}
            </Group>
            <Group gap="xs" wrap="wrap">
              <Text size="sm" c="dimmed">Allowed SSH sources:</Text>
              {sources.length === 0 ? (
                <Text size="sm">Any address on the management segment</Text>
              ) : (
                <>
                  {sources.map((id, idx) => {
                    const name = aliasesById.get(id);
                    return (
                      <Badge key={`${id}-${idx}`} variant={name ? 'default' : 'light'} color={name ? undefined : 'red'} className={name ? 'alias' : 'alias error-text'} tt="none">
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
          onRefresh={onRefresh}
          onAbandoned={setOpenError}
          onSessionLost={onSessionLost}
        />
      )}
    </Paper>
  );
}
