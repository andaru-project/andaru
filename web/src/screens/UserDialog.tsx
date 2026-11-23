import { Alert, Button, Checkbox, Group, Modal, MultiSelect, Select, Stack, TextInput } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { apiErrorMessage } from '../api';
import type { Role, User } from '../types';

interface UserDialogProps {
  user?: User;
  revision: number;
  roles: Role[];
  onClose(): void;
  onSessionLost(): void;
  onSave(input: { login: string; displayName: string; kind: User['kind']; enabled: boolean; roleIds: string[]; expectedRevision: number }): Promise<void>;
}

export function UserDialog({ user, revision, roles, onClose, onSessionLost, onSave }: UserDialogProps) {
  const [login, setLogin] = useState(user?.login ?? '');
  const [displayName, setDisplayName] = useState(user?.displayName ?? '');
  const [kind, setKind] = useState<User['kind']>(user?.kind ?? 'human');
  const [enabled, setEnabled] = useState(user?.enabled ?? true);
  const [roleIds, setRoleIds] = useState(user?.roleIds ?? []);
  const [loginError, setLoginError] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!/^[a-z0-9][a-z0-9._-]{2,63}$/.test(login)) {
      setLoginError('Login must be 3–64 lowercase ASCII letters, digits, dots, underscores or hyphens, and start with a letter or digit.');
      setError('');
      return;
    }
    setBusy(true);
    setLoginError('');
    setError('');
    try {
      await onSave({ login, displayName, kind, enabled, roleIds, expectedRevision: revision });
      onClose();
    } catch (requestError) {
      if (requestError instanceof Error && 'status' in requestError && requestError.status === 401) onSessionLost();
      else setError(apiErrorMessage(requestError));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={user ? `Edit ${user.login}` : 'Create user'} centered closeButtonProps={{ 'aria-label': 'Close' }}>
      <form onSubmit={submit}>
        <Stack>
          {error && <Alert color="red" role="alert" title="The user was not saved">{error}</Alert>}
          <TextInput data-autofocus error={loginError || undefined} label="Login" maxLength={64} required value={login} onChange={(event) => { setLogin(event.currentTarget.value.toLowerCase()); setLoginError(''); }} />
          <TextInput label="Display name" maxLength={128} value={displayName} onChange={(event) => setDisplayName(event.currentTarget.value)} />
          <Select allowDeselect={false} data={[{ value: 'human', label: 'Human' }, { value: 'service', label: 'Service account' }]} label="Account kind" value={kind} onChange={(value) => setKind(value as User['kind'])} />
          <MultiSelect data={roles.map((role) => ({ value: role.id, label: role.name }))} label="Roles" searchable value={roleIds} onChange={setRoleIds} />
          <Checkbox checked={enabled} label="Account enabled" onChange={(event) => setEnabled(event.currentTarget.checked)} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>Cancel</Button>
            <Button loading={busy} type="submit">{user ? 'Save user' : 'Create user'}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
