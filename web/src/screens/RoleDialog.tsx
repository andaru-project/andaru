import { Alert, Button, Checkbox, Group, Modal, ScrollArea, Stack, Textarea, TextInput } from '@mantine/core';
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
  const [nameError, setNameError] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim()) {
      setNameError('Role name is required.');
      setError('');
      return;
    }
    setBusy(true);
    setNameError('');
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
    <Modal opened onClose={onClose} title={role ? `Edit ${role.name}` : 'Create role'} centered size="lg" closeButtonProps={{ 'aria-label': 'Close' }}>
      <form onSubmit={submit}>
        <Stack>
          {error && <Alert color="red" role="alert" title="The role was not saved">{error}</Alert>}
          <TextInput data-autofocus error={nameError || undefined} label="Role name" maxLength={128} required value={name} onChange={(event) => { setName(event.currentTarget.value); setNameError(''); }} />
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
