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
import { AddonMarkdown } from '../addonMarkdown';
import { addonText, type AddonPropertyDraft } from '../addons';
import type { AddonProperty, Zone } from '../types';

interface AddonPropertyFormProps {
  properties: AddonProperty[];
  draft: AddonPropertyDraft;
  zones: Zone[];
  readOnly: boolean;
  errors: Map<string, string>;
  onChange(next: AddonPropertyDraft): void;
}

function stringPlaceholder(prop: AddonProperty): string | undefined {
  switch (prop.format) {
    case 'hostname':
      return 'service.example.lan';
    case 'cidr':
      return '192.168.10.0/24';
    case 'ip':
      return '192.0.2.10';
    case 'uri':
      return 'https://example.com';
    case 'duration':
      return '30s';
    default:
      return undefined;
  }
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

  return (
    <Stack gap="md">
      {properties.map((prop) => {
        const title = addonText(prop.title) || prop.name;
        const descriptionText = addonText(prop.description);
        const descriptionId = descriptionText ? `addon-prop-desc-${prop.name}` : undefined;
        const descriptionNode = descriptionText ? (
          <AddonMarkdown source={prop.description} className="addon-property-description" />
        ) : undefined;
        const fieldError = errors.get(prop.name);

        return (
          <div key={prop.name} className="addon-property-field">
            {prop.type === 'boolean' && (
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
            )}

            {prop.type === 'integer' && (
              <NumberInput
                label={title}
                description={descriptionNode}
                descriptionProps={descriptionId ? { component: 'div', id: descriptionId } : undefined}
                required={prop.required}
                disabled={readOnly}
                allowDecimal={false}
                min={prop.minimum}
                max={prop.maximum}
                value={typeof draft.values[prop.name] === 'number' ? (draft.values[prop.name] as number) : ''}
                error={fieldError}
                onChange={(val) => setValue(prop.name, val === '' ? '' : Number(val))}
              />
            )}

            {prop.type === 'string' && (
              <TextInput
                label={title}
                description={descriptionNode}
                descriptionProps={descriptionId ? { component: 'div', id: descriptionId } : undefined}
                required={prop.required}
                disabled={readOnly}
                placeholder={stringPlaceholder(prop)}
                value={typeof draft.values[prop.name] === 'string' ? (draft.values[prop.name] as string) : ''}
                error={fieldError}
                onChange={(event) => setValue(prop.name, event.currentTarget.value)}
              />
            )}

            {prop.type === 'string-list' && (
              <Textarea
                label={title}
                description={descriptionNode}
                descriptionProps={descriptionId ? { component: 'div', id: descriptionId } : undefined}
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
            )}

            {prop.type === 'enum' && (() => {
              const options = prop.options ?? [];
              const selectData = options.map((opt) => ({
                value: opt.value,
                label: addonText(opt.title) || opt.value,
              }));
              const current = Array.isArray(draft.values[prop.name])
                ? (draft.values[prop.name] as string[])
                : [];
              const single = prop.maxItems === 1;
              if (single) {
                const selectedVal = current[0] ?? null;
                const selectedOpt = options.find((opt) => opt.value === selectedVal);
                return (
                  <Stack gap={4}>
                    <Select
                      label={title}
                      description={descriptionNode}
                      descriptionProps={descriptionId ? { component: 'div', id: descriptionId } : undefined}
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
                    descriptionProps={descriptionId ? { component: 'div', id: descriptionId } : undefined}
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
            })()}

            {prop.type === 'zone-reference' && (
              <Select
                label={title}
                description={descriptionNode}
                descriptionProps={descriptionId ? { component: 'div', id: descriptionId } : undefined}
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
            )}

            {prop.type === 'secret-reference' && (() => {
              const existing = draft.existingSecrets[prop.name];
              const removed = Boolean(draft.secretRemovals[prop.name]);
              const typed = draft.secretInputs[prop.name] ?? '';
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
            })()}
          </div>
        );
      })}
    </Stack>
  );
}
