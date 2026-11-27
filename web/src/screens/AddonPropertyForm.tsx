import {
  Alert,
  Checkbox,
  MultiSelect,
  NumberInput,
  PasswordInput,
  Select,
  Stack,
  Switch,
  Text,
  Textarea,
  TextInput,
} from '@mantine/core';
import type { ReactNode } from 'react';
import { AddonMarkdown } from '../addonMarkdown';
import { addonText, type AddonPropertyDraft } from '../addons';
import type { AddonProperty, Zone } from '../types';

const formatPlaceholders: Record<string, string> = {
  hostname: 'service.example.lan',
  cidr: '192.168.10.0/24',
  ipv4: '192.0.2.10',
  ipv6: '2001:db8::10',
  uri: 'https://example.com',
  date: '2026-11-27',
  'date-time': '2026-11-27T12:00:00Z',
};

interface AddonPropertyFormProps {
  properties: AddonProperty[];
  draft: AddonPropertyDraft;
  zones: Zone[];
  readOnly: boolean;
  errors: Map<string, string>;
  onChange(next: AddonPropertyDraft): void;
}

export function AddonPropertyForm({
  properties,
  draft,
  zones,
  readOnly,
  errors,
  onChange,
}: AddonPropertyFormProps) {
  const zoneOptions = zones.map((z) => ({ value: z.id, label: z.name }));

  function setValue(name: string, value: unknown) {
    onChange({
      ...draft,
      values: { ...draft.values, [name]: value },
    });
  }

  function setSecretInput(name: string, value: string) {
    onChange({
      ...draft,
      secretInputs: { ...draft.secretInputs, [name]: value },
      secretRemovals: { ...draft.secretRemovals, [name]: false },
    });
  }

  function setSecretRemoved(name: string, removed: boolean) {
    onChange({
      ...draft,
      secretRemovals: { ...draft.secretRemovals, [name]: removed },
      secretInputs: removed ? { ...draft.secretInputs, [name]: '' } : draft.secretInputs,
    });
  }

  function renderControl(prop: AddonProperty): ReactNode {
    const title = addonText(prop.title) || prop.name;
    const descriptionText = addonText(prop.description);
    const descriptionId = descriptionText ? `addon-prop-desc-${prop.name}` : undefined;
    const descriptionNode = descriptionText ? (
      <AddonMarkdown source={prop.description} className="addon-property-description" />
    ) : undefined;
    const descriptionProps = descriptionId ? { component: 'div' as const, id: descriptionId } : undefined;
    const fieldError = errors.get(prop.name);

    switch (prop.type) {
      case 'boolean':
        return (
          <>
            <Switch
              label={title}
              checked={Boolean(draft.values[prop.name])}
              disabled={readOnly}
              aria-describedby={descriptionId}
              error={fieldError}
              onChange={(event) => setValue(prop.name, event.currentTarget.checked)}
            />
            {descriptionText && (
              <AddonMarkdown
                id={descriptionId}
                source={prop.description}
                className="addon-property-description"
              />
            )}
          </>
        );

      case 'integer': {
        const raw = draft.values[prop.name];
        const inputVal = typeof raw === 'number' && Number.isFinite(raw)
          ? raw
          : typeof raw === 'string'
            ? raw
            : '';
        return (
          <NumberInput
            label={title}
            description={descriptionNode}
            descriptionProps={descriptionProps}
            required={prop.required}
            disabled={readOnly}
            allowDecimal={false}
            min={prop.minimum}
            max={prop.maximum}
            value={inputVal}
            error={fieldError}
            onChange={(val) => setValue(prop.name, val)}
          />
        );
      }

      case 'string-list':
        return (
          <Textarea
            label={title}
            description={descriptionNode}
            descriptionProps={descriptionProps}
            required={prop.required}
            disabled={readOnly}
            autosize
            minRows={2}
            maxRows={8}
            placeholder="One entry per line"
            value={typeof draft.values[prop.name] === 'string' ? (draft.values[prop.name] as string) : ''}
            error={fieldError}
            onChange={(event) => setValue(prop.name, event.currentTarget.value)}
          />
        );

      case 'enum': {
        const options = prop.options ?? [];
        const selectData = options.map((opt) => ({
          value: opt.value,
          label: addonText(opt.title) || opt.value,
        }));
        const current = Array.isArray(draft.values[prop.name])
          ? (draft.values[prop.name] as string[])
          : [];
        if (prop.maxItems === 1) {
          const selectedVal = current[0] ?? null;
          const selectedOpt = options.find((opt) => opt.value === selectedVal);
          return (
            <Stack gap={4}>
              <Select
                label={title}
                description={descriptionNode}
                descriptionProps={descriptionProps}
                required={prop.required}
                disabled={readOnly}
                data={selectData}
                value={selectedVal}
                clearable={!prop.required}
                allowDeselect={!prop.required}
                error={fieldError}
                onChange={(val) => setValue(prop.name, val ? [val] : [])}
              />
              {selectedOpt?.description && (
                <div className="addon-option-note">
                  <AddonMarkdown source={selectedOpt.description} />
                </div>
              )}
            </Stack>
          );
        }
        return (
          <Stack gap={4}>
            <MultiSelect
              label={title}
              description={descriptionNode}
              descriptionProps={descriptionProps}
              required={prop.required}
              disabled={readOnly}
              data={selectData}
              value={current}
              maxValues={prop.maxItems}
              error={fieldError}
              onChange={(vals) => setValue(prop.name, vals)}
            />
            {options.filter((opt) => current.includes(opt.value) && opt.description).map((opt) => (
              <div key={opt.value} className="addon-option-note">
                <Text size="xs" fw={600}>{addonText(opt.title) || opt.value}</Text>
                <AddonMarkdown source={opt.description} />
              </div>
            ))}
          </Stack>
        );
      }

      case 'zone-reference':
        return (
          <Select
            label={title}
            description={descriptionNode}
            descriptionProps={descriptionProps}
            required={prop.required}
            disabled={readOnly}
            data={zoneOptions}
            value={typeof draft.values[prop.name] === 'string' && draft.values[prop.name] !== '' ? (draft.values[prop.name] as string) : null}
            clearable={!prop.required}
            allowDeselect={!prop.required}
            placeholder={zoneOptions.length > 0 ? 'Select a firewall zone' : 'No zones configured'}
            error={fieldError}
            onChange={(val) => setValue(prop.name, val ?? '')}
          />
        );

      case 'secret-reference': {
        const existing = Object.hasOwn(draft.existingSecrets, prop.name)
          ? draft.existingSecrets[prop.name]
          : undefined;
        const removed = Object.hasOwn(draft.secretRemovals, prop.name)
          ? Boolean(draft.secretRemovals[prop.name])
          : false;
        const typed = Object.hasOwn(draft.secretInputs, prop.name)
          ? draft.secretInputs[prop.name]
          : '';
        const statusHint = existing
          ? existing.unresolved
            ? `Unresolved secret (${existing.secret}). Enter a new value to restore it.`
            : `Stored securely (${existing.secret}). Leave blank to keep the current value.`
          : 'Write-only secret. Values are never displayed after saving.';
        return (
          <Stack gap={6}>
            {existing?.unresolved && !removed && (
              <Alert color="orange" role="status" title="Unresolved secret">
                This secret must be entered again before applying configuration changes.
              </Alert>
            )}
            <PasswordInput
              label={title}
              description={
                <Stack gap={4}>
                  <Text size="xs" c="dimmed">{statusHint}</Text>
                  {descriptionNode}
                </Stack>
              }
              descriptionProps={{ component: 'div', ...(descriptionId ? { id: descriptionId } : {}) }}
              required={prop.required && !existing}
              disabled={readOnly || removed}
              placeholder={existing && !existing.unresolved ? 'Leave blank to keep stored secret' : 'Enter secret value'}
              autoComplete="new-password"
              visibilityToggleFocusable
              visibilityToggleButtonProps={{ 'aria-label': `Toggle ${title} visibility` }}
              value={typed}
              error={fieldError}
              onChange={(event) => setSecretInput(prop.name, event.currentTarget.value)}
            />
            {existing && !prop.required && !readOnly && (
              <Checkbox
                label={`Remove stored ${title.toLowerCase()}`}
                checked={removed}
                onChange={(event) => setSecretRemoved(prop.name, event.currentTarget.checked)}
              />
            )}
          </Stack>
        );
      }

      case 'string':
      default:
        return (
          <TextInput
            label={title}
            description={descriptionNode}
            descriptionProps={descriptionProps}
            required={prop.required}
            disabled={readOnly}
            placeholder={prop.format ? formatPlaceholders[prop.format] : undefined}
            value={typeof draft.values[prop.name] === 'string' ? (draft.values[prop.name] as string) : ''}
            error={fieldError}
            onChange={(event) => setValue(prop.name, event.currentTarget.value)}
          />
        );
    }
  }

  return (
    <Stack gap="md">
      {properties.map((prop) => (
        <div key={prop.name} className="addon-property-field">
          {renderControl(prop)}
        </div>
      ))}
    </Stack>
  );
}
