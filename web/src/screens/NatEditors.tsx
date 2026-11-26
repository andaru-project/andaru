import { Alert, Button, Drawer, Fieldset, Group, MultiSelect, Pill, SegmentedControl, Select, Stack, Switch, Text, Textarea, TextInput, type ComboboxRenderPillInput } from '@mantine/core';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { ApiError, saveFailure } from '../api';
import {
  checkOneToOne, checkPortForward, oneToOneDraftOf, oneToOneErrors, oneToOnePayload, portForwardDraftOf, portForwardErrors, portForwardPayload, type OneToOneDraft, type PortForwardDraft,
} from '../natEdit';
import type { OneToOneNat, PortForward, Snapshot } from '../types';
import { zoneColor } from '../zones';
import { ZonePill } from './ZoneBadge';

const mono = { input: 'mono-input' };
const noAlias = 'No alias by that name. Aliases are made under Firewall ▸ Aliases.';

function zonePill({ option, value, onRemove, disabled }: ComboboxRenderPillInput) {
  const label = option?.label ?? String(value);
  const coloured = option !== undefined;
  return (
    <Pill
      withRemoveButton={!disabled && !option?.disabled}
      onRemove={onRemove}
      removeButtonProps={{ 'aria-label': `Remove ${label}` }}
      style={coloured ? { background: `var(--mantine-color-${zoneColor(label)}-light)`, boxShadow: `inset 0 0 0 1px var(--mantine-color-${zoneColor(label)}-outline)` } : undefined}
    >
      {label}
    </Pill>
  );
}

interface PortForwardEditorProps {
  entry?: PortForward;
  snapshot: Snapshot;
  onClose(): void;
  onSessionLost(): void;
  onAbandoned(message: string): void;
  onSave(value: Omit<PortForward, 'id'>): Promise<void>;
}

export function PortForwardEditor({ entry, snapshot, onClose, onSessionLost, onAbandoned, onSave }: PortForwardEditorProps) {
  const wanZone = snapshot.value.zones.find((z) => z.name.toLowerCase() === 'wan')?.id ?? snapshot.value.zones[0]?.id;
  const [draft, setDraft] = useState<PortForwardDraft>(() => portForwardDraftOf(entry, wanZone));
  const [errors, setErrors] = useState(new Map<string, string>());
  const [errorFocusTick, setErrorFocusTick] = useState(0);
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);

  const open = useRef(true);
  useEffect(() => { open.current = true; return () => { open.current = false; }; }, []);
  const failureRef = useRef<HTMLDivElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  useEffect(() => { if (failure) failureRef.current?.scrollIntoView?.({ block: 'nearest' }); }, [failure]);
  useEffect(() => {
    if (errorFocusTick === 0) return;
    formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.scrollIntoView?.({ block: 'center' });
    formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]:not(:disabled)')?.focus();
  }, [errorFocusTick]);

  const patch = (next: Partial<PortForwardDraft>) => setDraft((prev) => ({ ...prev, ...next }));
  const zones = snapshot.value.zones.map((z) => ({ value: z.id, label: z.name }));
  const addresses = (snapshot.value.aliases?.addresses ?? []).map((a) => ({ value: a.id, label: a.name }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    const found = checkPortForward(draft);
    if (found.size > 0) {
      setErrors(found);
      setFailure('');
      setErrorFocusTick((n) => n + 1);
      return;
    }
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      await onSave(portForwardPayload(draft));
      if (open.current) onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      if (!open.current) return onAbandoned(saveFailure(error, new Map(), 'port forward'));
      const fields = portForwardErrors(error, draft);
      setErrors(fields);
      if (fields.size > 0) setErrorFocusTick((n) => n + 1);
      setFailure(saveFailure(error, fields, 'port forward'));
    } finally {
      if (open.current) setBusy(false);
    }
  }

  return (
    <Drawer opened onClose={onClose} position="right" size="lg" title={entry ? `Edit port forward: ${entry.name}` : 'Add port forward'} lockScroll={false} closeButtonProps={{ 'aria-label': 'Close' }}>
      <form ref={formRef} onSubmit={(e) => void submit(e)} noValidate>
        <Stack>
          {failure && <Alert ref={failureRef} color="red" role="alert" title="The port forward was not saved">{failure}</Alert>}
          <Fieldset variant="unstyled" disabled={busy}>
            <Stack>
              <TextInput data-autofocus label="Name" maxLength={128} required value={draft.name} error={errors.get('name')} onChange={(e) => patch({ name: e.currentTarget.value })} />
              <Textarea label="Description" maxLength={512} autosize minRows={2} value={draft.description} error={errors.get('description')} onChange={(e) => patch({ description: e.currentTarget.value })} />
              <Group justify="space-between" align="center" wrap="wrap">
                <Switch label="Enabled" checked={draft.enabled} onChange={(e) => patch({ enabled: e.currentTarget.checked })} />
                <SegmentedControl
                  size="xs"
                  aria-label="Address family"
                  value={draft.family}
                  onChange={(value) => {
                    const family = value as 'ipv4' | 'ipv6';
                    patch({
                      family,
                      ...(family === 'ipv6' && !draft.targetAddress.trim() ? { targetKind: 'self' } : {}),
                      ...(family === 'ipv6' ? { reflection: false } : {}),
                    });
                  }}
                  data={[
                    { value: 'ipv4', label: 'IPv4' },
                    { value: 'ipv6', label: 'IPv6' },
                  ]}
                />
              </Group>
              {draft.family === 'ipv6' && (
                <Text size="xs" c="dimmed">IPv6 port forwards redirect to this firewall itself (or its static/virtual IPv6 addresses) and do not support NAT reflection.</Text>
              )}

              <Fieldset legend="Match">
                <Stack gap="xs">
                  <MultiSelect
                    label="Ingress zones"
                    placeholder="Choose zones"
                    required
                    searchable
                    data={zones}
                    value={draft.ingressZones}
                    error={errors.get('ingressZones')}
                    renderOption={({ option }) => <ZonePill name={option.label} />}
                    renderPill={zonePill}
                    onChange={(ingressZones) => patch({ ingressZones })}
                  />
                  <Group align="flex-start" gap="xs" wrap="wrap">
                    <Stack gap={4}>
                      <Text size="sm" fw={500}>Protocol</Text>
                      <SegmentedControl
                        size="xs"
                        aria-label="Protocol"
                        value={draft.protocol}
                        onChange={(value) => patch({ protocol: value as PortForward['protocol'] })}
                        data={[
                          { value: 'tcp', label: 'TCP' },
                          { value: 'udp', label: 'UDP' },
                          { value: 'tcp-udp', label: 'TCP/UDP' },
                        ]}
                      />
                    </Stack>
                    <TextInput
                      className="grow"
                      classNames={mono}
                      label="Destination port(s)"
                      placeholder="443 or 8000-8080"
                      maxLength={11}
                      required
                      value={draft.ports}
                      error={errors.get('ports')}
                      onChange={(e) => patch({ ports: e.currentTarget.value })}
                    />
                  </Group>
                  <MultiSelect
                    label="Source addresses (optional)"
                    placeholder="Any source"
                    searchable
                    nothingFoundMessage={noAlias}
                    data={addresses}
                    value={draft.from}
                    error={errors.get('from')}
                    onChange={(from) => patch({ from, ...(from.length === 0 ? { fromNegated: false } : {}) })}
                  />
                  {draft.from.length > 0 && (
                    <Switch size="xs" label="Invert source match (not these addresses)" checked={draft.fromNegated} onChange={(e) => patch({ fromNegated: e.currentTarget.checked })} />
                  )}
                  <Stack gap={4}>
                    <Text size="sm" fw={500}>Destination address</Text>
                    <SegmentedControl
                      size="xs"
                      aria-label="Destination address mode"
                      value={draft.toKind}
                      onChange={(value) => patch({ toKind: value as 'self' | 'address' })}
                      data={[
                        { value: 'self', label: 'This firewall' },
                        { value: 'address', label: 'Address alias' },
                      ]}
                    />
                  </Stack>
                  {draft.toKind === 'address' && (
                    <Select
                      label="Destination alias"
                      placeholder="Choose an address alias"
                      searchable
                      nothingFoundMessage={noAlias}
                      data={addresses}
                      value={draft.toAddress || null}
                      error={errors.get('to')}
                      onChange={(value) => patch({ toAddress: value ?? '' })}
                    />
                  )}
                  <Switch
                    size="xs"
                    label="Invert destination match (not this destination)"
                    checked={draft.toNegated}
                    onChange={(e) => patch({ toNegated: e.currentTarget.checked, ...(e.currentTarget.checked ? { reflection: false } : {}) })}
                  />
                </Stack>
              </Fieldset>

              <Fieldset legend="Translation">
                <Stack gap="xs">
                  <Switch
                    label="No redirect (exclude matching traffic from later port forwards)"
                    checked={draft.noRedirect}
                    onChange={(e) => patch({ noRedirect: e.currentTarget.checked })}
                  />
                  {!draft.noRedirect && (
                    <>
                      <Stack gap={4}>
                        <Text size="sm" fw={500}>Redirect target</Text>
                        <SegmentedControl
                          size="xs"
                          aria-label="Redirect target mode"
                          value={draft.targetKind}
                          onChange={(value) => patch({ targetKind: value as 'address' | 'self' })}
                          data={[
                            { value: 'address', label: 'IP address' },
                            { value: 'self', label: 'This firewall' },
                          ]}
                        />
                      </Stack>
                      {draft.targetKind === 'address' && (
                        <TextInput
                          classNames={mono}
                          label={draft.family === 'ipv6' ? 'Firewall IPv6 target address' : 'Target IPv4 address'}
                          placeholder={draft.family === 'ipv6' ? 'fd00:1::1' : '192.168.1.50'}
                          required
                          value={draft.targetAddress}
                          error={errors.get('target')}
                          onChange={(e) => patch({ targetAddress: e.currentTarget.value })}
                        />
                      )}
                      {draft.targetKind === 'self' && errors.get('target') && (
                        <Text size="xs" className="error-text">{errors.get('target')}</Text>
                      )}
                      <TextInput
                        w="12rem"
                        classNames={mono}
                        label="Target port (optional)"
                        description="Leave blank to keep the matched port(s)."
                        placeholder="8443"
                        inputMode="numeric"
                        value={draft.targetPort}
                        error={errors.get('targetPort')}
                        onChange={(e) => patch({ targetPort: e.currentTarget.value })}
                      />
                      <Stack gap={4}>
                        <Text size="sm" fw={500}>Firewall rule</Text>
                        <SegmentedControl
                          size="xs"
                          aria-label="Firewall rule"
                          value={draft.filterRule}
                          onChange={(value) => patch({ filterRule: value as 'auto' | 'none' })}
                          data={[
                            { value: 'auto', label: 'Auto-allow matched traffic' },
                            { value: 'none', label: 'None (use Rules page)' },
                          ]}
                        />
                      </Stack>
                      {draft.family !== 'ipv6' && !draft.toNegated && (
                        <Switch
                          label="NAT reflection (allow internal clients via the outside address)"
                          checked={draft.reflection}
                          onChange={(e) => patch({ reflection: e.currentTarget.checked })}
                        />
                      )}
                    </>
                  )}
                  <Switch label="Log matched packets" checked={draft.log} onChange={(e) => patch({ log: e.currentTarget.checked })} />
                </Stack>
              </Fieldset>
            </Stack>
          </Fieldset>

          <Group justify="flex-end" gap="xs" className="drawer-actions">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={busy}>{entry ? 'Save' : 'Add port forward'}</Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}

interface OneToOneEditorProps {
  entry?: OneToOneNat;
  snapshot: Snapshot;
  onClose(): void;
  onSessionLost(): void;
  onAbandoned(message: string): void;
  onSave(value: Omit<OneToOneNat, 'id'>): Promise<void>;
}

export function OneToOneEditor({ entry, snapshot, onClose, onSessionLost, onAbandoned, onSave }: OneToOneEditorProps) {
  const wanZone = snapshot.value.zones.find((z) => z.name.toLowerCase() === 'wan')?.id ?? snapshot.value.zones[0]?.id;
  const [draft, setDraft] = useState<OneToOneDraft>(() => oneToOneDraftOf(entry, wanZone));
  const [errors, setErrors] = useState(new Map<string, string>());
  const [errorFocusTick, setErrorFocusTick] = useState(0);
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);

  const open = useRef(true);
  useEffect(() => { open.current = true; return () => { open.current = false; }; }, []);
  const failureRef = useRef<HTMLDivElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  useEffect(() => { if (failure) failureRef.current?.scrollIntoView?.({ block: 'nearest' }); }, [failure]);
  useEffect(() => {
    if (errorFocusTick === 0) return;
    formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.scrollIntoView?.({ block: 'center' });
    formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]:not(:disabled)')?.focus();
  }, [errorFocusTick]);

  const patch = (next: Partial<OneToOneDraft>) => setDraft((prev) => ({ ...prev, ...next }));
  const zones = snapshot.value.zones.map((z) => ({ value: z.id, label: z.name }));
  const addresses = (snapshot.value.aliases?.addresses ?? []).map((a) => ({ value: a.id, label: a.name }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    const found = checkOneToOne(draft);
    if (found.size > 0) {
      setErrors(found);
      setFailure('');
      setErrorFocusTick((n) => n + 1);
      return;
    }
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      await onSave(oneToOnePayload(draft));
      if (open.current) onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      if (!open.current) return onAbandoned(saveFailure(error, new Map(), '1:1 NAT entry'));
      const fields = oneToOneErrors(error, draft);
      setErrors(fields);
      if (fields.size > 0) setErrorFocusTick((n) => n + 1);
      setFailure(saveFailure(error, fields, '1:1 NAT entry'));
    } finally {
      if (open.current) setBusy(false);
    }
  }

  return (
    <Drawer opened onClose={onClose} position="right" size="lg" title={entry ? `Edit 1:1 NAT: ${entry.name}` : 'Add 1:1 NAT entry'} lockScroll={false} closeButtonProps={{ 'aria-label': 'Close' }}>
      <form ref={formRef} onSubmit={(e) => void submit(e)} noValidate>
        <Stack>
          {failure && <Alert ref={failureRef} color="red" role="alert" title="The 1:1 NAT entry was not saved">{failure}</Alert>}
          <Fieldset variant="unstyled" disabled={busy}>
            <Stack>
              <TextInput data-autofocus label="Name" maxLength={128} required value={draft.name} error={errors.get('name')} onChange={(e) => patch({ name: e.currentTarget.value })} />
              <Textarea label="Description" maxLength={512} autosize minRows={2} value={draft.description} error={errors.get('description')} onChange={(e) => patch({ description: e.currentTarget.value })} />
              <Switch label="Enabled" checked={draft.enabled} onChange={(e) => patch({ enabled: e.currentTarget.checked })} />
              <Select
                label="Zone"
                placeholder="Choose a zone"
                required
                searchable
                data={zones}
                value={draft.zone || null}
                error={errors.get('zone')}
                renderOption={({ option }) => <ZonePill name={option.label} />}
                onChange={(value) => patch({ zone: value ?? '' })}
              />
              <Switch
                label="Exclude from 1:1 NAT (no binat)"
                checked={draft.noBinat}
                onChange={(e) => patch({ noBinat: e.currentTarget.checked })}
              />
              <TextInput
                classNames={mono}
                label="Internal IPv4 address or prefix"
                placeholder="192.168.1.50 or 192.168.1.16/28"
                maxLength={18}
                required
                value={draft.internal}
                error={errors.get('internal')}
                onChange={(e) => patch({ internal: e.currentTarget.value })}
              />
              {!draft.noBinat && (
                <>
                  <TextInput
                    classNames={mono}
                    label="External IPv4 address or prefix"
                    description="Must have the same prefix length as the internal address or prefix."
                    placeholder="203.0.113.50 or 203.0.113.16/28"
                    maxLength={18}
                    required
                    value={draft.external}
                    error={errors.get('external')}
                    onChange={(e) => patch({ external: e.currentTarget.value })}
                  />
                  <MultiSelect
                    label="Destination addresses (optional)"
                    placeholder="Any destination"
                    searchable
                    nothingFoundMessage={noAlias}
                    data={addresses}
                    value={draft.destination}
                    error={errors.get('destination')}
                    onChange={(destination) => patch({ destination, ...(destination.length === 0 ? { destinationNegated: false } : {}) })}
                  />
                  {draft.destination.length > 0 && (
                    <Switch
                      size="xs"
                      label="Invert destination match (not these addresses)"
                      checked={draft.destinationNegated}
                      error={errors.get('destinationNegated')}
                      onChange={(e) => patch({ destinationNegated: e.currentTarget.checked })}
                    />
                  )}
                  <Switch
                    label="NAT reflection"
                    checked={draft.reflection}
                    onChange={(e) => patch({ reflection: e.currentTarget.checked })}
                  />
                </>
              )}
            </Stack>
          </Fieldset>

          <Group justify="flex-end" gap="xs" className="drawer-actions">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={busy}>{entry ? 'Save' : 'Add 1:1 entry'}</Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}
