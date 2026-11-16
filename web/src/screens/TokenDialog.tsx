import { Alert, Button, Checkbox, Group, Modal, ScrollArea, Stack, TextInput } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { apiErrorMessage } from '../api';
import type { TokenIssueResponse, User } from '../types';

interface TokenDialogProps {
  account: Pick<User, 'id' | 'login' | 'kind'>;
  capabilities: string[];
  onClose(): void;
  onSessionLost(): void;
  onSave(input: { accountId: string; name: string; scope: string[]; expiresAt?: string; noExpiry: boolean; revokeOnPasswordChange?: boolean }): Promise<TokenIssueResponse>;
  onIssued(response: TokenIssueResponse): void;
}

function expiryLimits(now = Date.now()) {
	const day = 86400000;
	return {
		min: new Date(now).toISOString().slice(0, 10),
		max: new Date(now + 365 * day).toISOString().slice(0, 10),
	};
}

function defaultExpiry() {
	const date = new Date();
	date.setUTCDate(date.getUTCDate() + 90);
	return date.toISOString().slice(0, 10);
}

export function TokenDialog({ account, capabilities, onClose, onSessionLost, onSave, onIssued }: TokenDialogProps) {
  const [name, setName] = useState('');
  const [scope, setScope] = useState<string[]>([]);
  const [limits] = useState(expiryLimits);
  const [expiry, setExpiry] = useState(defaultExpiry);
  const [noExpiry, setNoExpiry] = useState(false);
  const [revokeOnPasswordChange, setRevokeOnPasswordChange] = useState(true);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim()) {
      setError('Token name is required.');
      return;
    }
    if (!noExpiry && !expiry) {
      setError('Choose an expiration date or explicitly select no expiry.');
      return;
    }
    setBusy(true);
    setError('');
    try {
      const response = await onSave({
        accountId: account.id,
        name: name.trim(),
        scope: [...scope].sort(),
        expiresAt: noExpiry ? undefined : new Date(`${expiry}T23:59:59.000Z`).toISOString(),
        noExpiry,
        revokeOnPasswordChange: account.kind === 'human' ? revokeOnPasswordChange : undefined,
      });
      onIssued(response);
      onClose();
    } catch (requestError) {
      if (requestError instanceof Error && 'status' in requestError && requestError.status === 401) onSessionLost();
      else setError(apiErrorMessage(requestError));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={`Create token for ${account.login}`} centered size="lg">
      <form onSubmit={submit}>
        <Stack>
          {error && <Alert color="red" role="alert">{error}</Alert>}
          <TextInput data-autofocus error={error || undefined} label="Token name" maxLength={128} required value={name} onChange={(event) => setName(event.currentTarget.value)} />
          <Checkbox.Group label="Capability scope" value={scope} onChange={setScope}>
            <ScrollArea.Autosize mah={220} mt="xs"><Stack gap="xs">{capabilities.map((capability) => <Checkbox key={capability} label={capability} value={capability} />)}</Stack></ScrollArea.Autosize>
          </Checkbox.Group>
					<TextInput disabled={noExpiry} label="Expiration date" max={limits.max} min={limits.min} required={!noExpiry} type="date" value={expiry} onChange={(event) => setExpiry(event.currentTarget.value)} />
          <Checkbox checked={noExpiry} label="Token never expires" onChange={(event) => setNoExpiry(event.currentTarget.checked)} />
          {noExpiry && <Alert color="yellow" role="alert">A token without an expiration remains valid until it is revoked. Choose this only for automation that cannot rotate credentials.</Alert>}
          {account.kind === 'human' && <Checkbox checked={revokeOnPasswordChange} label="Revoke when this user’s password changes" onChange={(event) => setRevokeOnPasswordChange(event.currentTarget.checked)} />}
          <Group justify="flex-end"><Button variant="default" onClick={onClose}>Cancel</Button><Button loading={busy} type="submit">Create token</Button></Group>
        </Stack>
      </form>
    </Modal>
  );
}
