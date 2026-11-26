import { Alert, Button, Group, Modal, Stack, Text, TextInput } from '@mantine/core';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { ApiError, saveFailure, type ApiClient } from '../api';
import { dhcpErrors, ipv6SuffixOf, isDelegatedIpv6 } from '../dhcpEdit';
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
  onAbandoned(message: string): void;
}

const mono = { input: 'mono-input' };

// Extract a clean single DNS label from a DHCP client hostname, if valid.
function cleanLabel(hostname: string): string {
  const first = hostname.split('.')[0]?.trim() ?? '';
  return /^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/.test(first) ? first : '';
}

export function MakeStaticModal({ api, target, onClose, onSaved, onSessionLost, onAbandoned }: MakeStaticModalProps) {
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

  const open = useRef(true);
  useEffect(() => { open.current = true; return () => { open.current = false; }; }, []);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = new Map<string, string>();
    if (!identifier.trim()) found.set('id', isV4 ? 'Enter a MAC address.' : 'Enter a DUID.');
    if (isV4 && !address.trim()) found.set('address', 'Enter an IPv4 address.');
    if (!isV4 && !isPrefix6 && !address.trim()) found.set('address', delegated ? 'Enter an interface suffix.' : 'Enter an IPv6 address.');
    if (!isV4 && isPrefix6 && !delegated && !prefix.trim()) found.set('prefix', 'Enter a delegated prefix.');
    if (!isV4 && isPrefix6 && delegated && !prefixId.trim()) found.set('prefixId', 'Enter a hex prefix ID.');
    if (found.size > 0) {
      setErrors(found);
      return;
    }

    setBusy(true);
    setErrors(new Map());
    setFailure('');
    let targetIdx = 0;
    try {
      if (isV4) {
        const saved = await api.configObject<Dhcp4Entry>('dhcp', target.entryId);
        const { id: _ignored, ...rest } = saved.value;
        void _ignored;
        const existing = rest.staticLeases ?? [];
        targetIdx = existing.length;
        const next: Omit<Dhcp4Entry, 'id'> = {
          ...rest,
          staticLeases: [
            ...existing,
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
        const existing = rest.staticLeases ?? [];
        const duidLower = identifier.trim().toLowerCase();
        const existingIdx = existing.findIndex((s) => s.duid.toLowerCase() === duidLower);
        const mergedHost = hostname.trim() || (existingIdx >= 0 ? existing[existingIdx].hostname : undefined);
        const nextList = [...existing];
        if (existingIdx >= 0) {
          targetIdx = existingIdx;
          const prev = existing[existingIdx];
          nextList[existingIdx] = {
            ...prev,
            duid: identifier.trim(),
            ...(!isPrefix6 && address.trim() ? { address: address.trim() } : {}),
            ...(isPrefix6 && !delegated && prefix.trim() ? { prefix: prefix.trim() } : {}),
            ...(isPrefix6 && delegated && prefixId.trim() ? { prefixId: prefixId.trim() } : {}),
            ...(mergedHost ? { hostname: mergedHost } : {}),
          };
        } else {
          targetIdx = existing.length;
          nextList.push({
            duid: identifier.trim(),
            ...(!isPrefix6 && address.trim() ? { address: address.trim() } : {}),
            ...(isPrefix6 && !delegated && prefix.trim() ? { prefix: prefix.trim() } : {}),
            ...(isPrefix6 && delegated && prefixId.trim() ? { prefixId: prefixId.trim() } : {}),
            ...(hostname.trim() ? { hostname: hostname.trim() } : {}),
          });
        }
        await api.saveDhcp6({ ...rest, staticLeases: nextList }, saved);
      }
      onSaved();
      if (open.current) onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      if (!open.current) return onAbandoned(saveFailure(error, new Map(), 'static lease'));
      const rawFields = dhcpErrors(error);
      const mapped = new Map<string, string>();
      const rowPrefix = `staticLeases/${targetIdx}`;
      const entryFallback = isPrefix6 ? (delegated ? 'prefixId' : 'prefix') : 'address';
      for (const [key, msg] of rawFields.entries()) {
        if (key === `${rowPrefix}/mac` || key === `${rowPrefix}/duid`) mapped.set('id', msg);
        else if (key === `${rowPrefix}/address`) mapped.set('address', msg);
        else if (key === `${rowPrefix}/prefix`) mapped.set('prefix', msg);
        else if (key === `${rowPrefix}/prefixId`) mapped.set('prefixId', msg);
        else if (key === `${rowPrefix}/hostname`) mapped.set('hostname', msg);
        else if (key === rowPrefix) mapped.set(entryFallback, msg);
      }
      setErrors(mapped);
      setFailure(saveFailure(error, mapped, 'static lease'));
    } finally {
      if (open.current) setBusy(false);
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
              required
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
