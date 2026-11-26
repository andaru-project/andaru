import { Alert, Anchor, Button, Drawer, Fieldset, Group, MultiSelect, SegmentedControl, Stack, Switch, Text, Textarea, TextInput } from '@mantine/core';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { ApiClient, ApiError, fieldErrors, saveFailure } from '../api';
import { firewallZone, type Rule, type RuleSide, type Snapshot } from '../types';
import { zoneColorMap } from '../zones';
import { AliasMultiSelect } from './AliasPicker';
import { ZonePill, ZoneSelectionPill } from './ZoneBadge';

interface RuleEditorProps {
  api: ApiClient;
  rule?: Rule;
  snapshot: Snapshot;
  onClose(): void;
  onSessionLost(): void;
  // A save failed after the panel was closed.
  onAbandoned(message: string): void;
  onAliasCreated?(): void;
  onSave(rule: Omit<Rule, 'id'>): Promise<void>;
}

const anySide: RuleSide = { zones: [], addresses: [] };

// The engine is the authority on what a rule may be: its refusals show beside the fields they name.
const field = /^\/(name|description|action|ports|(?:from|to)\/(?:zones|addresses|not))(?:\/|$)/;

// The editor is a panel beside the list, so the order stays in view while a rule is written.
export function RuleEditor({ api, rule, snapshot, onClose, onSessionLost, onAbandoned, onAliasCreated, onSave }: RuleEditorProps) {
  const [name, setName] = useState(rule?.name ?? '');
  const [description, setDescription] = useState(rule?.description ?? '');
  const [enabled, setEnabled] = useState(rule?.enabled ?? true);
  const [action, setAction] = useState<Rule['action']>(rule?.action ?? 'accept');
  const [from, setFrom] = useState<RuleSide>(rule?.from ?? anySide);
  const [to, setTo] = useState<RuleSide>(rule?.to ?? anySide);
  const [ports, setPorts] = useState(rule?.ports ?? []);
  const [log, setLog] = useState(rule?.log ?? false);
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  // A save may answer after this panel was closed and another opened: it then closes nothing.
  const open = useRef(true);
  useEffect(() => { open.current = true; return () => { open.current = false; }; }, []);
  // A refusal shows above the form, which may be scrolled away from it: it comes into view once, when it arrives.
  const failureRef = useRef<HTMLDivElement>(null);
  useEffect(() => { if (failure) failureRef.current?.scrollIntoView?.({ block: 'nearest' }); }, [failure]);

  const option = (item: { id: string; name: string }) => ({ value: item.id, label: item.name });
  const zones = snapshot.value.zones.map(option);
  const zoneColors = zoneColorMap(snapshot.value.zones);
  const addresses = (snapshot.value.aliases?.addresses ?? []).map(option);
  const portAliases = (snapshot.value.aliases?.ports ?? []).map(option);
  const toFirewall = to.zones.includes(firewallZone);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim()) {
      setErrors(new Map([['name', 'A rule needs a name.']]));
      setFailure('');
      return;
    }
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    // "Not" says something only about addresses that are named.
    const side = (value: RuleSide): RuleSide => ({ zones: value.zones, addresses: value.addresses, ...(value.not && value.addresses.length > 0 ? { not: true } : {}) });
    try {
      await onSave({ name: name.trim(), ...(description ? { description } : {}), enabled, action, from: side(from), to: side(to), ports, log });
      if (open.current) onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      // The panel was closed while the save was out: the page says what became of it.
      if (!open.current) return onAbandoned(saveFailure(error, new Map(), 'rule'));
      const fields = fieldErrors(error, field);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'rule'));
    } finally {
      setBusy(false);
    }
  }

  // One side of the rule: where, and within that, who.
  const sideFields = (label: string, key: 'from' | 'to', value: RuleSide, set: (next: RuleSide) => void, zoneOptions: Array<{ value: string; label: string }>, zoneHelp: string) => (
    <Fieldset legend={label}>
      <Stack gap="xs">
        <MultiSelect label={`${label}: zones`} description={zoneHelp} data={zoneOptions} value={value.zones} error={errors.get(`${key}/zones`)} searchable clearable nothingFoundMessage="No zone by that name."
          renderPill={(input) => <ZoneSelectionPill {...input} colors={zoneColors} />} renderOption={({ option }) => option.value === firewallZone ? option.label : <ZonePill name={option.label} color={zoneColors.get(option.value)} />}
          // The firewall stands alone as a destination: choosing it leaves the other zones, and choosing another leaves it.
          onChange={(next) => set({ ...value, zones: next.includes(firewallZone) && !value.zones.includes(firewallZone) ? [firewallZone] : next.length > 1 ? next.filter((id) => id !== firewallZone) : next })} />
        <AliasMultiSelect api={api} aliasType="addresses" label={`${label}: addresses`} description="Aliases or literal IPs, CIDRs, or ranges within those zones. Empty is every address." placeholder="Alias name, 192.168.1.50, or 10.0.0.0/24" initialOptions={addresses} value={value.addresses} error={errors.get(`${key}/addresses`)} clearable onAliasCreated={onAliasCreated} onSessionLost={onSessionLost}
          onChange={(next) => set({ ...value, addresses: next })} />
        <Switch label={`${label}: except these addresses`} description="Matches every other address in the chosen zones. Name an address first." checked={Boolean(value.not)} disabled={value.addresses.length === 0} error={errors.get(`${key}/not`)}
          onChange={(event) => set({ ...value, not: event.currentTarget.checked })} />
      </Stack>
    </Fieldset>
  );

  return (
    <Drawer opened onClose={onClose} position="right" size="md" title={rule ? `Edit rule: ${rule.name}` : 'Add rule'} lockScroll={false} closeButtonProps={{ 'aria-label': 'Close' }}>
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert ref={failureRef} color="red" role="alert" title="The rule was not saved">{failure}</Alert>}
          {!rule && <Text size="sm" c="dimmed">A new rule is added at the bottom of the list. Move it with Edit order.</Text>}
          {/* What is sent is what the form held when Save was pressed: nothing is typed over it meanwhile. */}
          <Fieldset variant="unstyled" disabled={busy}>
            <Stack>
          <TextInput data-autofocus label="Name" maxLength={128} required value={name} error={errors.get('name')} onChange={(event) => setName(event.currentTarget.value)} />
          <Textarea autosize label="Description" maxLength={4096} minRows={1} maxRows={4} value={description} error={errors.get('description')} onChange={(event) => setDescription(event.currentTarget.value)} />
          <Switch label="Enabled" checked={enabled} onChange={(event) => setEnabled(event.currentTarget.checked)} />
          <div>
            <Text size="sm" fw={500} id="rule-action">Action</Text>
            <SegmentedControl aria-labelledby="rule-action" value={action} onChange={(next) => setAction(next as Rule['action'])}
              data={[{ value: 'accept', label: 'Accept' }, { value: 'drop', label: 'Drop' }, { value: 'reject', label: 'Reject' }]} />
            <Text size="xs" c="dimmed">Drop says nothing to the sender. Reject tells it the traffic was refused.</Text>
            {errors.get('action') && <Text size="xs" className="error-text">{errors.get('action')}</Text>}
          </div>
          {sideFields('From', 'from', from, setFrom, zones, 'Empty is any network.')}
          {sideFields('To', 'to', to, setTo, [{ value: firewallZone, label: 'This firewall' }, ...zones], 'Empty is any network, never this firewall: choose This firewall for traffic to the box itself.')}
          {toFirewall && <Text size="sm" c="dimmed">To this firewall: the rule decides what may reach the box itself, and lets nothing through it.</Text>}
          <AliasMultiSelect api={api} aliasType="ports" label="Ports" description="Port aliases or literal ports (e.g. 443, tcp/8000-8080, udp/53). Empty is any protocol and port." placeholder="Alias name, 443, or tcp/8000-8080" initialOptions={portAliases} value={ports} error={errors.get('ports')} clearable onAliasCreated={onAliasCreated} onSessionLost={onSessionLost} onChange={setPorts} />
          <Text size="xs" c="dimmed">Need a group or named alias? <Anchor href="/firewall/aliases" target="_blank" rel="noopener noreferrer" size="xs">Open Firewall ▸ Aliases in a new tab</Anchor> so this draft stays open.</Text>
          <Switch label="Log what this rule matches" checked={log} onChange={(event) => setLog(event.currentTarget.checked)} />
            </Stack>
          </Fieldset>
          <Group justify="flex-end" className="drawer-actions">
            <Button variant="default" onClick={onClose}>Cancel</Button>
            <Button loading={busy} type="submit">Save rule</Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}
