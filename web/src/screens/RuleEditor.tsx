import { Alert, Button, Drawer, Fieldset, Group, MultiSelect, SegmentedControl, Stack, Switch, Text, Textarea, TextInput } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { ApiError, apiErrorMessage } from '../api';
import { firewallZone, type Rule, type RuleSide, type Snapshot } from '../types';

interface RuleEditorProps {
  rule?: Rule;
  snapshot: Snapshot;
  onClose(): void;
  onSessionLost(): void;
  onSave(rule: Omit<Rule, 'id'>): Promise<void>;
}

const anySide: RuleSide = { zones: [], addresses: [] };

// The engine is the authority on what a rule may be. Its refusals name fields by path ("/from/zones/0"); each
// shows beside the field it is about, and one about no field here shows above the form.
function fieldErrors(error: unknown) {
  const fields = new Map<string, string>();
  if (!(error instanceof ApiError)) return fields;
  for (const detail of error.details) {
    const field = /^\/(name|description|action|ports|(?:from|to)\/(?:zones|addresses|not))(?:\/|$)/.exec(detail.path ?? '')?.[1] ?? '';
    fields.set(field, [fields.get(field), detail.message].filter(Boolean).join(' '));
  }
  return fields;
}

// The editor is a panel beside the list, so the order stays in view while a rule is written.
export function RuleEditor({ rule, snapshot, onClose, onSessionLost, onSave }: RuleEditorProps) {
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

  const option = (item: { id: string; name: string }) => ({ value: item.id, label: item.name });
  const zones = snapshot.value.zones.map(option);
  const addresses = (snapshot.value.aliases?.addresses ?? []).map(option);
  const portAliases = (snapshot.value.aliases?.ports ?? []).map(option);
  const toFirewall = to.zones.includes(firewallZone);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim()) {
      setErrors(new Map([['name', 'A rule needs a name.']]));
      return;
    }
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    // "Not" says something only about addresses that are named.
    const side = (value: RuleSide): RuleSide => ({ zones: value.zones, addresses: value.addresses, ...(value.not && value.addresses.length > 0 ? { not: true } : {}) });
    try {
      await onSave({ name: name.trim(), ...(description ? { description } : {}), enabled, action, from: side(from), to: side(to), ports, log });
      onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      const fields = fieldErrors(error);
      setErrors(fields);
      // Someone else's change underneath keeps what was typed here, and says what happened.
      if (error instanceof ApiError && error.status === 412) setFailure('This rule was changed by someone else while it was open here. Close the panel to see what is saved, then make the change again.');
      else if (fields.size === 0 || fields.has('')) setFailure(fields.get('') ?? apiErrorMessage(error));
    } finally {
      setBusy(false);
    }
  }

  // One side of the rule: where, and within that, who.
  const sideFields = (label: string, key: 'from' | 'to', value: RuleSide, set: (next: RuleSide) => void, zoneOptions: Array<{ value: string; label: string }>) => (
    <Fieldset legend={label}>
      <Stack gap="xs">
        <MultiSelect label={`${label}: zones`} description="Empty is any network, never this firewall." data={zoneOptions} value={value.zones} error={errors.get(`${key}/zones`)} searchable clearable
          // The firewall stands alone as a destination: choosing it leaves the other zones, and choosing another leaves it.
          onChange={(next) => set({ ...value, zones: next.includes(firewallZone) && !value.zones.includes(firewallZone) ? [firewallZone] : next.length > 1 ? next.filter((id) => id !== firewallZone) : next })} />
        <MultiSelect label={`${label}: addresses`} description="Address aliases within those zones. Empty is every address." data={addresses} value={value.addresses} error={errors.get(`${key}/addresses`)} searchable clearable
          onChange={(next) => set({ ...value, addresses: next })} />
        <Switch label="Not these addresses: every address but them" checked={Boolean(value.not)} disabled={value.addresses.length === 0} error={errors.get(`${key}/not`)}
          onChange={(event) => set({ ...value, not: event.currentTarget.checked })} />
      </Stack>
    </Fieldset>
  );

  return (
    <Drawer opened onClose={onClose} position="right" size="md" title={rule ? `Edit ${rule.name}` : 'Add rule'}>
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert color="red" role="alert" title="The rule was not saved">{failure}</Alert>}
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
          {sideFields('From', 'from', from, setFrom, zones)}
          {sideFields('To', 'to', to, setTo, [{ value: firewallZone, label: 'This firewall' }, ...zones])}
          {toFirewall && <Text size="sm" c="dimmed">To this firewall: the rule decides what may reach the box itself, and lets nothing through it.</Text>}
          <MultiSelect label="Ports" description="Port aliases. Empty is any protocol and port." data={portAliases} value={ports} error={errors.get('ports')} searchable clearable onChange={setPorts} />
          <Switch label="Log what this rule matches" checked={log} onChange={(event) => setLog(event.currentTarget.checked)} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>Cancel</Button>
            <Button loading={busy} type="submit">Save rule</Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}
