import { Alert, Button, Group, Modal, Stack, Text, TextInput } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { ApiError, saveFailure, type ApiClient } from '../api';
import { dhcpErrors, isDelegatedIpv6 } from '../dhcpEdit';
import type { Dhcp4Entry, Dhcp6Entry, Lease4, Lease6, Segment } from '../types';

export type MakeStaticTarget =
  | { family: 'v4'; lease: Lease4; segment: Segment; entryId: string }
  | { family: 'v6'; lease: Lease6; segment: Segment; entryId: string };

interface MakeStaticModalProps {
  api: ApiClient;
  target: MakeStaticTarget;
  onClose(): void;
  onSaved(): void;
  onSessionLost(): void;
}

const mono = { input: 'mono-input' };

// Extract a clean single DNS label from a DHCP client hostname, if valid.
function cleanLabel(hostname: string): string {
  const first = hostname.split('.')[0]?.trim() ?? '';
  return /^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/.test(first) ? first : '';
}

// Extract the low 64-bit interface identifier suffix (e.g. "::1234:5678") from a full IPv6 address.
function ipv6SuffixOf(address: string): string {
  const host = address.split('/')[0];
  if (!host.includes(':')) return '';
  const halves = host.split('::');
  if (halves.length > 2) return '';
  const left = halves[0] ? halves[0].split(':') : [];
  const right = halves.length === 2 && halves[1] ? halves[1].split(':') : [];
  const missing = 8 - left.length - right.length;
  if (missing < 0) return '';
  const full = [...left, ...Array<string>(missing).fill('0'), ...right].map((g) => parseInt(g || '0', 16).toString(16));
  if (full.length !== 8 || full.some((g) => g === 'NaN')) return '';
  const tail = full.slice(4);
  let i = 0;
  while (i < tail.length - 1 && tail[i] === '0') i++;
  return `::${tail.slice(i).join(':')}`;
}

export function MakeStaticModal({ api, target, onClose, onSaved, onSessionLost }: MakeStaticModalProps) {
  const isV4 = target.family === 'v4';
  const delegated = !isV4 && isDelegatedIpv6(target.segment);
  const isPrefix6 = !isV4 && target.lease.type === 'prefix';

  const [identifier, setIdentifier] = useState(isV4 ? target.lease.mac : target.lease.duid);
  const [address, setAddress] = useState(() => {
    if (isV4) return target.lease.address;
    if (isPrefix6) return '';
    return delegated ? ipv6SuffixOf(target.lease.address) : target.lease.address;
  });
  const [prefix, setPrefix] = useState(!isV4 && isPrefix6 && !delegated ? target.lease.address : '');
  const [prefixId, setPrefixId] = useState('');
  const [hostname, setHostname] = useState(() => cleanLabel(target.lease.hostname));
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = new Map<string, string>();
    if (!identifier.trim()) found.set('id', isV4 ? 'Enter a MAC address.' : 'Enter a DUID.');
    if (isV4 && !address.trim()) found.set('address', 'Enter an IPv4 address.');
    if (!isV4 && !delegated && !address.trim() && !prefix.trim()) found.set('address', 'Enter an IPv6 address or prefix.');
    if (!isV4 && delegated && !address.trim() && !prefixId.trim()) found.set('address', 'Enter a suffix address or prefix ID.');
    if (found.size > 0) {
      setErrors(found);
      return;
    }

    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      if (isV4) {
        const saved = await api.configObject<Dhcp4Entry>('dhcp', target.entryId);
        const { id: _ignored, ...rest } = saved.value;
        void _ignored;
        const next: Omit<Dhcp4Entry, 'id'> = {
          ...rest,
          staticLeases: [
            ...(rest.staticLeases ?? []),
            {
              mac: identifier.trim(),
              address: address.trim(),
              ...(hostname.trim() ? { hostname: hostname.trim() } : {}),
            },
          ],
        };
        await api.saveDhcp4(next, saved);
      } else {
        const saved = await api.configObject<Dhcp6Entry>('dhcp6', target.entryId);
        const { id: _ignored, ...rest } = saved.value;
        void _ignored;
        const next: Omit<Dhcp6Entry, 'id'> = {
          ...rest,
          staticLeases: [
            ...(rest.staticLeases ?? []),
            {
              duid: identifier.trim(),
              ...(address.trim() ? { address: address.trim() } : {}),
              ...(!delegated && prefix.trim() ? { prefix: prefix.trim() } : {}),
              ...(delegated && prefixId.trim() ? { prefixId: prefixId.trim() } : {}),
              ...(hostname.trim() ? { hostname: hostname.trim() } : {}),
            },
          ],
        };
        await api.saveDhcp6(next, saved);
      }
      onSaved();
      onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      const rawFields = dhcpErrors(error);
      const mapped = new Map<string, string>();
      for (const [key, msg] of rawFields.entries()) {
        if (key.endsWith('/mac') || key.endsWith('/duid')) mapped.set('id', msg);
        else if (key.endsWith('/address')) mapped.set('address', msg);
        else if (key.endsWith('/prefix')) mapped.set('prefix', msg);
        else if (key.endsWith('/prefixId')) mapped.set('prefixId', msg);
        else if (key.endsWith('/hostname')) mapped.set('hostname', msg);
      }
      setErrors(mapped);
      setFailure(saveFailure(error, mapped, 'static lease'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={`Make static lease on ${target.segment.name}`}>
      <form onSubmit={(e) => void submit(e)} noValidate>
        <Stack>
          <Text size="sm" c="dimmed">
            Adds a static reservation to the saved {isV4 ? 'DHCPv4' : 'DHCPv6'} server. Apply changes to activate it.
          </Text>
          {failure && <Alert color="red" role="alert" title="Static lease was not saved">{failure}</Alert>}
          <TextInput
            classNames={mono}
            label={isV4 ? 'MAC address' : 'Client DUID'}
            required
            value={identifier}
            error={errors.get('id')}
            onChange={(e) => setIdentifier(e.currentTarget.value)}
          />
          {!isPrefix6 && (
            <TextInput
              classNames={mono}
              label={isV4 ? 'IPv4 address' : delegated ? 'IPv6 interface suffix' : 'IPv6 address'}
              description={!isV4 ? 'Static DHCPv6 addresses must lie outside dynamic ranges.' : undefined}
              required={isV4}
              value={address}
              error={errors.get('address')}
              onChange={(e) => setAddress(e.currentTarget.value)}
            />
          )}
          {!isV4 && isPrefix6 && !delegated && (
            <TextInput
              classNames={mono}
              label="Delegated prefix"
              required
              value={prefix}
              error={errors.get('prefix')}
              onChange={(e) => setPrefix(e.currentTarget.value)}
            />
          )}
          {!isV4 && isPrefix6 && delegated && (
            <TextInput
              classNames={mono}
              label="Pool prefix ID (hex)"
              description={`Current delegated prefix: ${target.lease.address}`}
              placeholder="1"
              required
              value={prefixId}
              error={errors.get('prefixId')}
              onChange={(e) => setPrefixId(e.currentTarget.value)}
            />
          )}
          <TextInput
            label="Hostname (optional)"
            placeholder="nas"
            value={hostname}
            error={errors.get('hostname')}
            onChange={(e) => setHostname(e.currentTarget.value)}
          />
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={busy}>Save static lease</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
