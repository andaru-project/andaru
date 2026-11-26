import { Alert, Button, Checkbox, Drawer, Fieldset, Group, NativeSelect, Stack, Switch, Text, TextInput } from '@mantine/core';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { ApiError, saveFailure } from '../api';
import {
  checkDhcp4, checkDhcp6, dhcp4Payload, dhcp6Payload, dhcpErrors, dhcpOptionTypes, draft4Of, draft6Of, formatLeaseTime, isDelegatedIpv6, newRowKey, optionPlaceholder, parseDecimalInt, pruneDhcp4Draft, pruneDhcp6Draft, raModeOf, servedIpv4, servedIpv6Static, type Dhcp4Draft, type Dhcp6Draft, type DhcpCommonDraft, type DhcpOptionDraft,
} from '../dhcpEdit';
import type { Dhcp4Entry, Dhcp6Entry, DhcpOptionType, SavedObject, Segment, Snapshot } from '../types';

export type DhcpEditorTarget =
  | { family: 'v4'; segment: Segment; snapshot: Snapshot; saved?: SavedObject<Dhcp4Entry> }
  | { family: 'v6'; segment: Segment; snapshot: Snapshot; saved?: SavedObject<Dhcp6Entry> };

interface DhcpEditorProps {
  target: DhcpEditorTarget;
  onClose(): void;
  onSessionLost(): void;
  onAbandoned(message: string): void;
  onSaveV4(value: Omit<Dhcp4Entry, 'id'>, saved?: SavedObject<Dhcp4Entry>): Promise<void>;
  onDeleteV4(id: string, version: string): Promise<void>;
  onSaveV6(value: Omit<Dhcp6Entry, 'id'>, saved?: SavedObject<Dhcp6Entry>): Promise<void>;
  onDeleteV6(id: string, version: string): Promise<void>;
}

const mono = { input: 'mono-input' };

function hasAdvancedErrors(errors: Map<string, string>): boolean {
  for (const key of errors.keys()) {
    if (key === 'nextServer' || key === 'bootFileName' || key.startsWith('options/')) return true;
  }
  return false;
}

function CustomOptionsSection({
  family,
  options,
  errors,
  onChange,
}: {
  family: 'v4' | 'v6';
  options: DhcpOptionDraft[];
  errors: Map<string, string>;
  onChange(next: DhcpOptionDraft[]): void;
}) {
  const update = (index: number, patch: Partial<DhcpOptionDraft>) => {
    onChange(options.map((item, i) => (i === index ? { ...item, ...patch } : item)));
  };
  const changeType = (index: number, nextType: DhcpOptionType) => {
    const current = options[index];
    const data = nextType === 'boolean' && current.data !== 'true' && current.data !== 'false' ? 'true' : current.data;
    update(index, { type: nextType, data });
  };

  return (
    <Stack gap="xs">
      <Group justify="space-between" align="center">
        <div>
          <Text size="sm" fw={600}>Custom options</Text>
          <Text size="xs" c="dimmed">
            {family === 'v4' ? 'Option codes 1–254 (excluding engine-managed and protocol codes).' : 'Option codes 1–65535 (excluding engine-managed and protocol codes).'}
          </Text>
        </div>
        <Button
          size="compact-xs"
          variant="light"
          onClick={() => onChange([...options, { key: newRowKey(), code: '', type: 'string', data: '', alwaysSend: false }])}
        >
          Add option
        </Button>
      </Group>
      {options.length === 0 && <Text size="xs" c="dimmed">No custom options configured.</Text>}
      {options.map((opt, i) => (
        <Stack key={opt.key} gap="xs" className="dhcp-repeat-row">
          <Group align="flex-start" gap="xs" wrap="wrap">
            <TextInput
              w="5.5rem"
              classNames={mono}
              label="Code"
              inputMode="numeric"
              placeholder={family === 'v4' ? '252' : '200'}
              value={opt.code}
              error={errors.get(`options/${i}/code`)}
              onChange={(e) => update(i, { code: e.currentTarget.value })}
            />
            <NativeSelect
              w="9.5rem"
              label="Type"
              data={dhcpOptionTypes}
              value={opt.type}
              onChange={(e) => changeType(i, e.currentTarget.value as DhcpOptionType)}
            />
            {opt.type === 'boolean' ? (
              <NativeSelect
                className="grow"
                label="Value"
                data={[{ value: 'true', label: 'true' }, { value: 'false', label: 'false' }]}
                value={opt.data === 'false' ? 'false' : 'true'}
                error={errors.get(`options/${i}/data`)}
                onChange={(e) => update(i, { data: e.currentTarget.value })}
              />
            ) : (
              <TextInput
                className="grow"
                classNames={mono}
                label="Value"
                placeholder={optionPlaceholder(opt.type, family)}
                value={opt.data}
                error={errors.get(`options/${i}/data`)}
                onChange={(e) => update(i, { data: e.currentTarget.value })}
              />
            )}
          </Group>
          <Group justify="space-between" align="center">
            <Checkbox
              size="xs"
              label="Always send (even if client does not request it)"
              checked={opt.alwaysSend}
              onChange={(e) => update(i, { alwaysSend: e.currentTarget.checked })}
            />
            <Button
              size="compact-xs"
              variant="subtle"
              color="red"
              aria-label={`Remove custom option ${i + 1}`}
              onClick={() => onChange(options.filter((_, idx) => idx !== i))}
            >
              Remove
            </Button>
          </Group>
        </Stack>
      ))}
    </Stack>
  );
}

export function DhcpEditor({
  target, onClose, onSessionLost, onAbandoned, onSaveV4, onDeleteV4, onSaveV6, onDeleteV6,
}: DhcpEditorProps) {
  const { snapshot } = target;
  const isV4 = target.family === 'v4';
  const delegated = !isV4 && isDelegatedIpv6(target.segment);
  const [draft4, setDraft4] = useState<Dhcp4Draft>(() => draft4Of(isV4 ? target.saved?.value : undefined, target.segment));
  const [draft6, setDraft6] = useState<Dhcp6Draft>(() => draft6Of(!isV4 ? target.saved?.value : undefined, target.segment));
  const set4 = (patch: Partial<Dhcp4Draft>) => setDraft4((d) => ({ ...d, ...patch }));
  const set6 = (patch: Partial<Dhcp6Draft>) => setDraft6((d) => ({ ...d, ...patch }));
  const common: DhcpCommonDraft = isV4 ? draft4 : draft6;
  const setCommon = (patch: Partial<DhcpCommonDraft>) => (isV4 ? set4(patch) : set6(patch));
  const updateCommon = (fn: (d: DhcpCommonDraft) => Partial<DhcpCommonDraft>) => {
    if (isV4) setDraft4((d) => ({ ...d, ...fn(d) }));
    else setDraft6((d) => ({ ...d, ...fn(d) }));
  };

  const [advancedOpen, setAdvancedOpen] = useState(() => (isV4
    ? Boolean(draft4.nextServer || draft4.bootFileName || draft4.options.length > 0)
    : draft6.options.length > 0));
  const [errors, setErrors] = useState(new Map<string, string>());
  const [errorFocusTick, setErrorFocusTick] = useState(0);
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const [disabling, setDisabling] = useState(false);

  const open = useRef(true);
  useEffect(() => { open.current = true; return () => { open.current = false; }; }, []);
  const failureRef = useRef<HTMLDivElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  const keepRef = useRef<HTMLButtonElement>(null);
  useEffect(() => { if (failure) failureRef.current?.scrollIntoView?.({ block: 'nearest' }); }, [failure]);
  useEffect(() => {
    if (errorFocusTick === 0) return;
    formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.scrollIntoView?.({ block: 'center' });
    formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]:not(:disabled)')?.focus();
  }, [errorFocusTick]);
  useEffect(() => { if (disabling) keepRef.current?.focus(); }, [disabling]);

  const dnsServes = Boolean(snapshot.value.dns?.segments.includes(target.segment.id));
  const localDomain = snapshot.value.dns?.localDomain || 'home.arpa';
  const v4Info = isV4 ? servedIpv4(target.segment) : null;
  const v6Static = !isV4 ? servedIpv6Static(target.segment) : null;
  const raMode = !isV4 ? raModeOf(target.segment) : 'disabled';
  const showRaWarning = !isV4 && draft6.ranges.some((r) => r.first.trim() || r.last.trim()) && raMode !== 'managed' && raMode !== 'assisted';
  const showNoDnsWarning = isV4 && !dnsServes && !draft4.dnsServers.trim();

  function showValidationErrors(fields: Map<string, string>) {
    setErrors(fields);
    if (hasAdvancedErrors(fields)) setAdvancedOpen(true);
    if (fields.size > 0) setErrorFocusTick((n) => n + 1);
  }

  async function runSave(action: () => Promise<void>) {
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      await action();
      if (open.current) onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      const noun = isV4 ? 'DHCPv4 server' : 'DHCPv6 server';
      if (!open.current) return onAbandoned(saveFailure(error, new Map(), noun));
      const fields = dhcpErrors(error);
      showValidationErrors(fields);
      setFailure(saveFailure(error, fields, noun));
    } finally {
      if (open.current) setBusy(false);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    if (isV4) {
      const pruned = pruneDhcp4Draft(draft4);
      setDraft4(pruned);
      const found = checkDhcp4(pruned);
      if (found.size > 0) {
        showValidationErrors(found);
        setFailure('');
        return;
      }
      const payload = dhcp4Payload(pruned, target.segment.id, target.saved?.value);
      void runSave(() => onSaveV4(payload, target.saved));
    } else {
      const pruned = pruneDhcp6Draft(draft6);
      setDraft6(pruned);
      const found = checkDhcp6(pruned, delegated);
      if (found.size > 0) {
        showValidationErrors(found);
        setFailure('');
        return;
      }
      const payload = dhcp6Payload(pruned, target.segment.id, delegated, target.saved?.value);
      void runSave(() => onSaveV6(payload, target.saved));
    }
  }

  function confirmDisable() {
    if (!target.saved) return;
    if (!disabling) {
      setDisabling(true);
      return;
    }
    const { value, version } = target.saved;
    void runSave(() => (isV4 ? onDeleteV4(value.id, version) : onDeleteV6(value.id, version)));
  }

  const title = `${isV4 ? 'DHCPv4' : 'DHCPv6'}: ${target.segment.name}`;
  const subtitle = isV4
    ? (v4Info ? `Subnet ${v4Info.subnet} · firewall address ${v4Info.own}` : 'Static IPv4 segment')
    : delegated
      ? `Delegated IPv6 prefix · subnet ID ${target.segment.addressing.ipv6.subnetId ?? '0'}`
      : (v6Static ? `Static IPv6 ${v6Static.prefix}` : 'Static IPv6 segment');

  const leaseNum = parseDecimalInt(common.leaseTime);
  const leasePreview = Number.isInteger(leaseNum) && leaseNum >= 300 && leaseNum <= 604800
    ? `=${formatLeaseTime(leaseNum)}`
    : '300–604800';

  return (
    <Drawer opened onClose={onClose} position="right" size="lg" title={title} lockScroll={false} closeButtonProps={{ 'aria-label': 'Close' }}>
      <form ref={formRef} onSubmit={submit} noValidate>
        <Stack>
          <Text size="sm" c="dimmed" ff="monospace">{subtitle}</Text>
          {failure && <Alert ref={failureRef} color="red" role="alert" title={`The ${isV4 ? 'DHCPv4' : 'DHCPv6'} server was not saved`}>{failure}</Alert>}
          {!target.saved && (
            <Text size="sm" c="dimmed">Saving enables this server in the draft configuration. Nothing changes on the box until applied.</Text>
          )}
          {showNoDnsWarning && (
            <Alert color="yellow" title="No DNS server configured">
              Andaru DNS does not serve this segment and no DNS servers are listed below, so clients will receive no DNS server.
            </Alert>
          )}
          {showRaWarning && (
            <Alert color="yellow" title="Router advertisements do not request DHCPv6">
              This segment&apos;s router advertisement mode is &ldquo;{raMode}&rdquo;. Clients only ask for DHCPv6 addresses automatically when RA mode is managed or assisted.
            </Alert>
          )}

          <Fieldset variant="unstyled" disabled={busy || disabling}>
            <Stack>
              <Fieldset legend="Server & address pool">
                <Stack gap="xs">
                  <Group align="flex-start" wrap="wrap">
                    <TextInput
                      className="grow"
                      data-autofocus
                      label="Name"
                      maxLength={128}
                      required
                      value={common.name}
                      error={errors.get('name')}
                      onChange={(e) => setCommon({ name: e.currentTarget.value })}
                    />
                    <TextInput
                      w="10rem"
                      classNames={mono}
                      label="Lease time (s)"
                      description={leasePreview}
                      inputMode="numeric"
                      required
                      value={common.leaseTime}
                      error={errors.get('leaseTime')}
                      onChange={(e) => setCommon({ leaseTime: e.currentTarget.value })}
                    />
                  </Group>

                  <Group justify="space-between" align="center" mt={4}>
                    <div>
                      <Text size="sm" fw={600}>{delegated ? 'Address suffix ranges' : 'Dynamic address ranges'}</Text>
                      {delegated && <Text size="xs" c="dimmed">Interface suffixes with leading 64 bits zero, e.g. ::1000 to ::1fff.</Text>}
                    </div>
                    <Button
                      size="compact-xs"
                      variant="light"
                      onClick={() => updateCommon((d) => ({ ranges: [...d.ranges, { key: newRowKey(), first: '', last: '' }] }))}
                    >
                      Add range
                    </Button>
                  </Group>
                  {errors.get('ranges') && <Text size="xs" className="error-text">{errors.get('ranges')}</Text>}
                  {common.ranges.length === 0 && !isV4 && (
                    <Text size="xs" c="dimmed">No dynamic ranges. DHCPv6 will serve stateless options or prefix delegation only.</Text>
                  )}
                  {common.ranges.map((rg, i) => (
                    <Stack key={rg.key} gap={4}>
                      <Group align="flex-start" gap="xs" wrap="wrap">
                        <TextInput
                          className="grow"
                          classNames={mono}
                          label={i === 0 ? 'First address' : undefined}
                          aria-label={`Range ${i + 1} first address`}
                          placeholder={isV4 ? '192.168.1.100' : delegated ? '::1000' : 'fd00:1::1000'}
                          value={rg.first}
                          error={errors.get(`ranges/${i}/first`) ?? (errors.get(`ranges/${i}`) ? ' ' : undefined)}
                          onChange={(e) => {
                            const first = e.currentTarget.value;
                            updateCommon((d) => ({ ranges: d.ranges.map((item, idx) => (idx === i ? { ...item, first } : item)) }));
                          }}
                        />
                        <TextInput
                          className="grow"
                          classNames={mono}
                          label={i === 0 ? 'Last address' : undefined}
                          aria-label={`Range ${i + 1} last address`}
                          placeholder={isV4 ? '192.168.1.254' : delegated ? '::1fff' : 'fd00:1::1fff'}
                          value={rg.last}
                          error={errors.get(`ranges/${i}/last`) ?? (errors.get(`ranges/${i}`) ? ' ' : undefined)}
                          onChange={(e) => {
                            const last = e.currentTarget.value;
                            updateCommon((d) => ({ ranges: d.ranges.map((item, idx) => (idx === i ? { ...item, last } : item)) }));
                          }}
                        />
                        {(!isV4 || common.ranges.length > 1) && (
                          <Button
                            size="compact-xs"
                            variant="subtle"
                            color="red"
                            mt={i === 0 ? 28 : 6}
                            aria-label={`Remove range ${i + 1}`}
                            onClick={() => updateCommon((d) => ({ ranges: d.ranges.filter((_, idx) => idx !== i) }))}
                          >
                            Remove
                          </Button>
                        )}
                      </Group>
                      {errors.get(`ranges/${i}`) && <Text size="xs" className="error-text">{errors.get(`ranges/${i}`)}</Text>}
                    </Stack>
                  ))}
                </Stack>
              </Fieldset>

              <Fieldset legend="Network options">
                <Stack gap="xs">
                  {isV4 && (
                    <TextInput
                      classNames={mono}
                      label="Gateway override"
                      description={`Leave blank to hand out this segment's own address${v4Info ? ` (${v4Info.own})` : ''}.`}
                      placeholder={v4Info?.own ?? '192.168.1.1'}
                      value={draft4.gateway}
                      error={errors.get('gateway')}
                      onChange={(e) => set4({ gateway: e.currentTarget.value })}
                    />
                  )}
                  <TextInput
                    classNames={mono}
                    label="DNS servers"
                    description={dnsServes
                      ? 'Leave blank to hand out Andaru DNS on this segment. Separate multiple addresses with commas.'
                      : 'Comma-separated IP addresses.'}
                    placeholder={isV4 ? (dnsServes && v4Info ? v4Info.own : '192.168.1.1, 1.1.1.1') : (dnsServes && v6Static ? v6Static.own : 'fd00:1::1')}
                    value={common.dnsServers}
                    error={errors.get('dnsServers')}
                    onChange={(e) => setCommon({ dnsServers: e.currentTarget.value })}
                  />
                  {isV4 && (
                    <TextInput
                      label="Domain name override"
                      description={snapshot.value.dns ? `Leave blank to use DNS local domain (${localDomain}).` : 'Optional DHCP option 15 domain name.'}
                      placeholder={localDomain}
                      value={draft4.domainName}
                      error={errors.get('domainName')}
                      onChange={(e) => set4({ domainName: e.currentTarget.value })}
                    />
                  )}
                  <TextInput
                    label="Search domains"
                    description={isV4
                      ? 'Optional DHCP option 119 DNS search list, comma-separated.'
                      : `Leave blank to use ${localDomain} and global DNS search domains.`}
                    placeholder="home.arpa, lab.example"
                    value={common.domainSearch}
                    error={errors.get('domainSearch')}
                    onChange={(e) => setCommon({ domainSearch: e.currentTarget.value })}
                  />
                  <TextInput
                    classNames={mono}
                    label={isV4 ? 'NTP servers' : 'SNTP servers'}
                    description="Optional comma-separated time server addresses."
                    placeholder={isV4 ? '192.168.1.1, 192.168.1.2' : 'fd00:1::123'}
                    value={common.ntpServers}
                    error={errors.get('ntpServers')}
                    onChange={(e) => setCommon({ ntpServers: e.currentTarget.value })}
                  />
                </Stack>
              </Fieldset>

              {!isV4 && (
                <Fieldset legend="Prefix delegation">
                  <Stack gap="xs">
                    <Switch
                      label="Delegate IPv6 prefixes to downstream routers"
                      checked={draft6.pdEnabled}
                      onChange={(e) => set6({ pdEnabled: e.currentTarget.checked })}
                    />
                    {draft6.pdEnabled && (
                      delegated ? (
                        <Group align="flex-start" gap="xs" wrap="wrap">
                          <TextInput
                            w="8rem"
                            classNames={mono}
                            label="Subnet ID (hex)"
                            placeholder="8"
                            required
                            value={draft6.pdSubnetId}
                            error={errors.get('prefixDelegation/subnetId')}
                            onChange={(e) => set6({ pdSubnetId: e.currentTarget.value })}
                          />
                          <TextInput
                            w="8rem"
                            classNames={mono}
                            label="Pool length"
                            inputMode="numeric"
                            placeholder="60"
                            required
                            value={draft6.pdLength}
                            error={errors.get('prefixDelegation/length')}
                            onChange={(e) => set6({ pdLength: e.currentTarget.value })}
                          />
                          <TextInput
                            w="9rem"
                            classNames={mono}
                            label="Delegated length"
                            inputMode="numeric"
                            placeholder="64"
                            required
                            value={draft6.pdDelegatedLength}
                            error={errors.get('prefixDelegation/delegatedLength')}
                            onChange={(e) => set6({ pdDelegatedLength: e.currentTarget.value })}
                          />
                        </Group>
                      ) : (
                        <>
                          <Group align="flex-start" gap="xs" wrap="wrap">
                            <TextInput
                              className="grow"
                              classNames={mono}
                              label="Pool prefix"
                              placeholder="fd00:beef:1::/48"
                              required
                              value={draft6.pdPrefix}
                              error={errors.get('prefixDelegation/prefix')}
                              onChange={(e) => set6({ pdPrefix: e.currentTarget.value })}
                            />
                            <TextInput
                              w="9rem"
                              classNames={mono}
                              label="Delegated length"
                              inputMode="numeric"
                              placeholder="56"
                              required
                              value={draft6.pdDelegatedLength}
                              error={errors.get('prefixDelegation/delegatedLength')}
                              onChange={(e) => set6({ pdDelegatedLength: e.currentTarget.value })}
                            />
                          </Group>
                          <TextInput
                            classNames={mono}
                            label="Excluded prefix (optional)"
                            placeholder="fd00:beef:1:ff::/64"
                            value={draft6.pdExcludedPrefix}
                            error={errors.get('prefixDelegation/excludedPrefix')}
                            onChange={(e) => set6({ pdExcludedPrefix: e.currentTarget.value })}
                          />
                        </>
                      )
                    )}
                    {errors.get('prefixDelegation') && <Text size="xs" className="error-text">{errors.get('prefixDelegation')}</Text>}
                  </Stack>
                </Fieldset>
              )}

              <Fieldset legend="Static leases">
                <Stack gap="xs">
                  <Group justify="space-between" align="center">
                    <Text size="xs" c="dimmed">
                      {isV4
                        ? 'Reserve a fixed IPv4 address (and optional DNS hostname) for a MAC address.'
                        : delegated
                          ? 'Reserve an interface suffix (::10) and/or pool prefix ID for a client DUID.'
                          : 'Reserve a fixed IPv6 address (outside dynamic ranges) and/or prefix for a client DUID.'}
                    </Text>
                    <Button
                      size="compact-xs"
                      variant="light"
                      onClick={() => (isV4
                        ? setDraft4((d) => ({ ...d, staticLeases: [...d.staticLeases, { key: newRowKey(), mac: '', address: '', hostname: '' }] }))
                        : setDraft6((d) => ({ ...d, staticLeases: [...d.staticLeases, { key: newRowKey(), duid: '', address: '', prefix: '', prefixId: '', hostname: '' }] })))}
                    >
                      Add static lease
                    </Button>
                  </Group>
                  {(isV4 ? draft4.staticLeases : draft6.staticLeases).length === 0 && (
                    <Text size="xs" c="dimmed">No static leases configured.</Text>
                  )}
                  {isV4
                    ? draft4.staticLeases.map((lease, i) => (
                      <Group key={lease.key} align="flex-start" gap="xs" wrap="wrap" className="dhcp-repeat-row">
                        <TextInput
                          className="grow"
                          classNames={mono}
                          label="MAC address"
                          placeholder="02:00:00:00:00:10"
                          value={lease.mac}
                          error={errors.get(`staticLeases/${i}/mac`)}
                          onChange={(e) => {
                            const mac = e.currentTarget.value;
                            setDraft4((d) => ({ ...d, staticLeases: d.staticLeases.map((item, idx) => (idx === i ? { ...item, mac } : item)) }));
                          }}
                        />
                        <TextInput
                          className="grow"
                          classNames={mono}
                          label="IPv4 address"
                          placeholder="192.168.1.10"
                          value={lease.address}
                          error={errors.get(`staticLeases/${i}/address`)}
                          onChange={(e) => {
                            const address = e.currentTarget.value;
                            setDraft4((d) => ({ ...d, staticLeases: d.staticLeases.map((item, idx) => (idx === i ? { ...item, address } : item)) }));
                          }}
                        />
                        <TextInput
                          w="9rem"
                          label="Hostname"
                          placeholder="nas"
                          value={lease.hostname}
                          error={errors.get(`staticLeases/${i}/hostname`)}
                          onChange={(e) => {
                            const hostname = e.currentTarget.value;
                            setDraft4((d) => ({ ...d, staticLeases: d.staticLeases.map((item, idx) => (idx === i ? { ...item, hostname } : item)) }));
                          }}
                        />
                        <Button
                          size="compact-xs"
                          variant="subtle"
                          color="red"
                          mt={28}
                          aria-label={`Remove static lease ${i + 1}`}
                          onClick={() => setDraft4((d) => ({ ...d, staticLeases: d.staticLeases.filter((_, idx) => idx !== i) }))}
                        >
                          Remove
                        </Button>
                      </Group>
                    ))
                    : draft6.staticLeases.map((lease, i) => (
                      <Stack key={lease.key} gap="xs" className="dhcp-repeat-row">
                        <TextInput
                          classNames={mono}
                          label="Client DUID"
                          placeholder="00:01:00:01:aa:bb:cc:dd:ee:ff"
                          value={lease.duid}
                          error={errors.get(`staticLeases/${i}/duid`)}
                          onChange={(e) => {
                            const duid = e.currentTarget.value;
                            setDraft6((d) => ({ ...d, staticLeases: d.staticLeases.map((item, idx) => (idx === i ? { ...item, duid } : item)) }));
                          }}
                        />
                        <Group align="flex-start" gap="xs" wrap="wrap">
                          <TextInput
                            className="grow"
                            classNames={mono}
                            label={delegated ? 'Address suffix' : 'IPv6 address'}
                            placeholder={delegated ? '::10' : 'fd00:1::10'}
                            value={lease.address}
                            error={errors.get(`staticLeases/${i}/address`)}
                            onChange={(e) => {
                              const address = e.currentTarget.value;
                              setDraft6((d) => ({ ...d, staticLeases: d.staticLeases.map((item, idx) => (idx === i ? { ...item, address } : item)) }));
                            }}
                          />
                          {delegated ? (
                            <TextInput
                              w="7.5rem"
                              classNames={mono}
                              label="Prefix ID"
                              placeholder="1"
                              value={lease.prefixId}
                              error={errors.get(`staticLeases/${i}/prefixId`)}
                              onChange={(e) => {
                                const prefixId = e.currentTarget.value;
                                setDraft6((d) => ({ ...d, staticLeases: d.staticLeases.map((item, idx) => (idx === i ? { ...item, prefixId } : item)) }));
                              }}
                            />
                          ) : (
                            <TextInput
                              className="grow"
                              classNames={mono}
                              label="Delegated prefix"
                              placeholder="fd00:beef:1:100::/56"
                              value={lease.prefix}
                              error={errors.get(`staticLeases/${i}/prefix`)}
                              onChange={(e) => {
                                const prefix = e.currentTarget.value;
                                setDraft6((d) => ({ ...d, staticLeases: d.staticLeases.map((item, idx) => (idx === i ? { ...item, prefix } : item)) }));
                              }}
                            />
                          )}
                          <TextInput
                            w="8.5rem"
                            label="Hostname"
                            placeholder="nas"
                            value={lease.hostname}
                            error={errors.get(`staticLeases/${i}/hostname`)}
                            onChange={(e) => {
                              const hostname = e.currentTarget.value;
                              setDraft6((d) => ({ ...d, staticLeases: d.staticLeases.map((item, idx) => (idx === i ? { ...item, hostname } : item)) }));
                            }}
                          />
                        </Group>
                        {errors.get(`staticLeases/${i}`) && <Text size="xs" className="error-text">{errors.get(`staticLeases/${i}`)}</Text>}
                        <Group justify="flex-end">
                          <Button
                            size="compact-xs"
                            variant="subtle"
                            color="red"
                            aria-label={`Remove static lease ${i + 1}`}
                            onClick={() => setDraft6((d) => ({ ...d, staticLeases: d.staticLeases.filter((_, idx) => idx !== i) }))}
                          >
                            Remove
                          </Button>
                        </Group>
                      </Stack>
                    ))}
                </Stack>
              </Fieldset>

              <details className="dhcp-disclosure" open={advancedOpen}>
                <summary onClick={(e) => { e.preventDefault(); setAdvancedOpen((v) => !v); }}>
                  {isV4 ? 'Boot / PXE & custom options' : 'Custom DHCPv6 options'}
                </summary>
                <Stack gap="sm" mt="xs">
                  {isV4 && (
                    <Group align="flex-start" gap="xs" wrap="wrap">
                      <TextInput
                        className="grow"
                        classNames={mono}
                        label="Next server (TFTP / PXE)"
                        placeholder="192.168.1.10"
                        value={draft4.nextServer}
                        error={errors.get('nextServer')}
                        onChange={(e) => set4({ nextServer: e.currentTarget.value })}
                      />
                      <TextInput
                        className="grow"
                        classNames={mono}
                        label="Boot file name"
                        maxLength={127}
                        placeholder="pxelinux.0"
                        value={draft4.bootFileName}
                        error={errors.get('bootFileName')}
                        onChange={(e) => set4({ bootFileName: e.currentTarget.value })}
                      />
                    </Group>
                  )}
                  <CustomOptionsSection
                    family={target.family}
                    options={common.options}
                    errors={errors}
                    onChange={(options) => setCommon({ options })}
                  />
                </Stack>
              </details>
            </Stack>
          </Fieldset>

          {disabling && (
            <Alert color="red" role="alert" title={`Disable ${isV4 ? 'DHCPv4' : 'DHCPv6'} on ${target.segment.name}?`}>
              Clients on this segment will stop receiving new leases once applied. Its saved ranges, static leases, and custom options will be removed.
            </Alert>
          )}

          <Group justify="space-between" className="drawer-actions">
            <div>
              {target.saved && !disabling && (
                <Button variant="subtle" color="red" disabled={busy} onClick={confirmDisable}>Disable server…</Button>
              )}
              {disabling && (
                <Button color="red" loading={busy} onClick={confirmDisable}>Disable server</Button>
              )}
            </div>
            <Group gap="xs">
              <Button ref={keepRef} variant="default" disabled={busy} onClick={() => (disabling ? setDisabling(false) : onClose())}>
                {disabling ? 'Keep server' : 'Cancel'}
              </Button>
              {!disabling && (
                <Button type="submit" loading={busy}>
                  {target.saved ? 'Save' : `Enable ${isV4 ? 'DHCPv4' : 'DHCPv6'}`}
                </Button>
              )}
            </Group>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}
