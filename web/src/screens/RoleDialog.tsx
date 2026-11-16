import { Button, Checkbox, Group, Modal, ScrollArea, Stack, Textarea, TextInput } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { apiErrorMessage } from '../api';
import type { Role } from '../types';

interface RoleDialogProps {
  role?: Role;
  capabilities: string[];
  onClose(): void;
  onSessionLost(): void;
  onSave(role: Omit<Role, 'id' | 'builtIn'>): Promise<void>;
}

export function RoleDialog({ role, capabilities, onClose, onSessionLost, onSave }: RoleDialogProps) {
  const [name, setName] = useState(role?.name ?? '');
  const [description, setDescription] = useState(role?.description ?? '');
  const [selected, setSelected] = useState(role?.capabilities ?? []);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim()) {
      setError('Role name is required.');
      return;
    }
    setBusy(true);
    setError('');
    try {
      await onSave({ name: name.trim(), description, capabilities: [...selected].sort() });
      onClose();
    } catch (requestError) {
      if (requestError instanceof Error && 'status' in requestError && requestError.status === 401) onSessionLost();
      else setError(apiErrorMessage(requestError));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={role ? `Edit ${role.name}` : 'Create role'} centered size="lg">
      <form onSubmit={submit}>
        <Stack>
          <TextInput data-autofocus error={error || undefined} label="Role name" maxLength={128} required value={name} onChange={(event) => setName(event.currentTarget.value)} />
          <Textarea autosize label="Description" maxLength={4096} maxRows={4} minRows={2} value={description} onChange={(event) => setDescription(event.currentTarget.value)} />
          <Checkbox.Group label="Capabilities" value={selected} onChange={setSelected}>
            <ScrollArea.Autosize mah={260} mt="xs">
              <Stack gap="xs">{capabilities.map((capability) => <Checkbox key={capability} label={capability} value={capability} />)}</Stack>
            </ScrollArea.Autosize>
          </Checkbox.Group>
          <Group justify="flex-end"><Button variant="default" onClick={onClose}>Cancel</Button><Button loading={busy} type="submit">Save role draft</Button></Group>
        </Stack>
      </form>
    </Modal>
  );
}
