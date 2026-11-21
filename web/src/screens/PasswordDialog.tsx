import { Alert, Button, Group, Modal, PasswordInput, Stack, Text } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { apiErrorMessage } from '../api';
import type { User } from '../types';

interface PasswordDialogProps {
  user: User;
  selfAccountId: string;
  onClose(): void;
  onSessionLost(): void;
  onSave(currentPassword: string, newPassword: string): Promise<void>;
}

export function PasswordDialog({ user, selfAccountId, onClose, onSessionLost, onSave }: PasswordDialogProps) {
  const consoleOnly = user.roleIds.includes('administrator') && user.id !== selfAccountId;
  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmation, setConfirmation] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const length = Array.from(newPassword).length;
    if (length < 8 || length > 128 || new TextEncoder().encode(newPassword).length > 512 || newPassword.includes('\0')) {
      setError('New password must contain 8–128 Unicode characters, at most 512 UTF-8 bytes, and no NUL.');
      return;
    }
    if (newPassword !== confirmation) {
      setError('The new passwords do not match.');
      return;
    }
    setBusy(true);
    setError('');
    try {
      await onSave(currentPassword, newPassword);
      setCurrentPassword('');
      setNewPassword('');
      setConfirmation('');
      onClose();
    } catch (requestError) {
      if (requestError instanceof Error && 'status' in requestError && requestError.status === 401) onSessionLost();
      else setError(apiErrorMessage(requestError));
    } finally {
      setCurrentPassword('');
      setNewPassword('');
      setConfirmation('');
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={`Reset password for ${user.login}`} centered>
      <form onSubmit={submit}>
        <Stack>
          <Text size="sm">Passwords allow spaces and password-manager paste. They must contain 8–128 Unicode characters, use at most 512 UTF-8 bytes, and cannot be a blocked common password.</Text>
          {consoleOnly && <Alert color="yellow" role="status">Built-in administrator passwords can only be reset from the root console.</Alert>}
          {error && <Alert color="red" role="alert">{error}</Alert>}
          <PasswordInput autoComplete="current-password" disabled={consoleOnly} label="Your current password" maxLength={512} required={!consoleOnly} value={currentPassword} onChange={(event) => setCurrentPassword(event.currentTarget.value)} />
          <PasswordInput autoComplete="new-password" disabled={consoleOnly} label="New password" maxLength={512} required={!consoleOnly} value={newPassword} onChange={(event) => setNewPassword(event.currentTarget.value)} />
          <PasswordInput autoComplete="new-password" disabled={consoleOnly} error={error || undefined} label="Confirm new password" maxLength={512} required={!consoleOnly} value={confirmation} onChange={(event) => setConfirmation(event.currentTarget.value)} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>Cancel</Button>
            <Button disabled={consoleOnly} loading={busy} type="submit">Reset password</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
