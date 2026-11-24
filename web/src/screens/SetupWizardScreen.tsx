import {
  Alert,
  Badge,
  Button,
  Group,
  Loader,
  Paper,
  SegmentedControl,
  Select,
  Stack,
  Stepper,
  Switch,
  Table,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { ApiClient, apiErrorMessage, reportError } from '../api';
import {
  buildSetupOperations,
  defaultDhcpRange,
  effectiveMgmtIpv6Mode,
  type SetupNetworkDraft,
} from '../setup';
import { formatTime, formatTimestamp, setTimeZone, timeZoneOptions, useTimeZone } from '../timezone';
import type { Apply, LinkStatus, Session } from '../types';

function Row({ label, children }: { label: string; children: ReactNode }) {
  return <Table.Tr><Table.Th scope="row">{label}</Table.Th><Table.Td>{children}</Table.Td></Table.Tr>;
}

function Facts({ children }: { children: ReactNode }) {
  return <Table className="facts" withRowBorders={false} verticalSpacing={2} horizontalSpacing={0}><Table.Tbody>{children}</Table.Tbody></Table>;
}

const applyConfirmSeconds = 180;
const hostnamePattern = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

interface SetupWizardScreenProps {
  api: ApiClient;
  session: Session;
  onComplete?(): void;
  onSessionLost?(): void;
}

function linkOptionLabel(link: LinkStatus): string {
  const mac = link.linkAddress || 'no MAC';
  const ips = link.addresses.map((a) => `${a.address}/${a.prefixLength}`).join(', ');
  return `${link.name} (${mac}) · ${link.operState}${ips ? ` · ${ips}` : ''}`;
}

export function SetupWizardScreen({ api, onComplete, onSessionLost }: SetupWizardScreenProps) {
  const activeTimeZone = useTimeZone();
  const zoneChoices = useMemo(() => timeZoneOptions(activeTimeZone), [activeTimeZone]);

  const [step, setStep] = useState(0);
  const [loadingHost, setLoadingHost] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [links, setLinks] = useState<LinkStatus[]>([]);
  const [initialHostname, setInitialHostname] = useState('');
  const [hostname, setHostname] = useState('');
  const [selectedZone, setSelectedZone] = useState(activeTimeZone);
  const [previewAt] = useState(() => Date.now());
  const [savedVersion, setSavedVersion] = useState<string | undefined>(undefined);
  const [hasExistingConfig, setHasExistingConfig] = useState(false);

  // If config save succeeds but preview/apply fails, retries reuse committedVersion instead of
  // re-creating duplicate zones/links. Editing any network field clears committedVersion.
  const [committedVersion, setCommittedVersion] = useState<string | null>(null);

  const [draft, setDraft] = useState<SetupNetworkDraft>({
    mgmtMac: '',
    mgmtName: 'lan0',
    mgmtIpv4Mode: 'static',
    mgmtIpv4Address: '192.168.1.1/24',
    mgmtIpv6Mode: 'none',
    mgmtIpv6Address: '',
    mgmtIpv6RaMode: 'unmanaged',
    serveDhcp: true,
    dhcpStart: '192.168.1.100',
    dhcpEnd: '192.168.1.199',
    serveDns: true,
    wanMac: '',
    wanName: 'wan0',
    wanIpv4Mode: 'dhcp',
    wanIpv4Address: '',
    wanIpv4Gateway: '',
    wanIpv6Mode: 'none',
    wanIpv6Address: '',
    wanIpv6Gateway: '',
    wanDhcp6PrefixLen: '56',
    wanDnsServers: '',
  });

  const [applyBusy, setApplyBusy] = useState(false);
  const [applyError, setApplyError] = useState('');
  const [pendingApply, setPendingApply] = useState<Apply | null>(null);
  const [confirmBusy, setConfirmBusy] = useState(false);
  const [completed, setCompleted] = useState(false);

  const updateDraft = useCallback((patch: Partial<SetupNetworkDraft>) => {
    setCommittedVersion(null);
    setDraft((cur) => ({ ...cur, ...patch }));
  }, []);

  const handleSessionError = useCallback((error: unknown, setMsg: (msg: string) => void) => {
    if (onSessionLost) reportError(error, onSessionLost, setMsg);
    else setMsg(apiErrorMessage(error));
  }, [onSessionLost]);

  const loadHostData = useCallback(async () => {
    setLoadingHost(true);
    setLoadError('');
    try {
      const [sys, linkReply, snap] = await Promise.all([
        api.system(),
        api.links(),
        api.config().catch(() => null),
      ]);
      const physical = linkReply.items.filter((l) => l.linkType !== 'loopback' && Boolean(l.linkAddress));
      setLinks(physical);
      setInitialHostname(sys.hostname);
      setHostname((cur) => cur || sys.hostname);
      if (snap) {
        setSavedVersion(snap.savedVersion);
        setHasExistingConfig(snap.value.links.length + snap.value.segments.length > 0);
      }
      setDraft((cur) => {
        if (cur.mgmtMac) return cur;
        const firstUp = physical.find((l) => l.operState === 'up') ?? physical[0];
        const second = physical.find((l) => l !== firstUp);
        return {
          ...cur,
          mgmtMac: firstUp?.linkAddress ?? '',
          mgmtName: firstUp?.name ?? 'lan0',
          wanMac: second?.linkAddress ?? '',
          wanName: second?.name ?? 'wan0',
        };
      });
    } catch (error) {
      handleSessionError(error, setLoadError);
    } finally {
      setLoadingHost(false);
    }
  }, [api, handleSessionError]);

  // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount, not derived state
  useEffect(() => { void loadHostData(); }, [loadHostData]);

  const normalizedHostname = hostname.trim().toLowerCase();
  const hostnameValid = normalizedHostname === '' || hostnamePattern.test(normalizedHostname);
  const mgmtOptions = links.map((l) => ({ value: l.linkAddress ?? '', label: linkOptionLabel(l) }));
  const wanOptions = [
    { value: '', label: 'None yet (configure WAN later)' },
    ...links.filter((l) => (l.linkAddress ?? '') !== draft.mgmtMac).map((l) => ({ value: l.linkAddress ?? '', label: linkOptionLabel(l) })),
  ];
  const wanSupportsDelegation = Boolean(draft.wanMac) && draft.wanIpv6Mode === 'dhcp6';
  const resolvedMgmtIpv6 = effectiveMgmtIpv6Mode(draft);

  const canAdvanceMgmt =
    Boolean(draft.mgmtMac) &&
    (draft.mgmtIpv4Mode === 'dhcp' || draft.mgmtIpv4Address.trim().includes('/')) &&
    (!draft.serveDhcp || draft.mgmtIpv4Mode !== 'static' || (Boolean(draft.dhcpStart.trim()) && Boolean(draft.dhcpEnd.trim()))) &&
    (draft.mgmtIpv6Mode !== 'static' || draft.mgmtIpv6Address.trim().includes('/'));

  const canAdvanceWan =
    !draft.wanMac ||
    ((draft.wanIpv4Mode !== 'none' || draft.wanIpv6Mode !== 'none') &&
      (draft.wanIpv4Mode !== 'static' || (draft.wanIpv4Address.trim().includes('/') && Boolean(draft.wanIpv4Gateway.trim()))) &&
      (draft.wanIpv6Mode !== 'static' || (draft.wanIpv6Address.trim().includes('/') && Boolean(draft.wanIpv6Gateway.trim()))));

  async function saveAndApply() {
    setApplyBusy(true);
    setApplyError('');
    try {
      setTimeZone(selectedZone);
      let versionToApply = committedVersion;
      if (!versionToApply) {
        const ops = buildSetupOperations(draft);
        const saved = await api.applyConfigChanges(ops, savedVersion);
        setSavedVersion(saved.savedVersion);
        setCommittedVersion(saved.savedVersion);
        versionToApply = saved.savedVersion;
      }
      const preview = await api.previewNetwork(versionToApply);
      const errors = preview.findings.filter((f) => f.severity === 'error');
      if (errors.length > 0) {
        setApplyError(errors.map((e) => e.message).join(' '));
        return;
      }
      const applied = await api.apply(versionToApply, applyConfirmSeconds);
      if (normalizedHostname && normalizedHostname !== initialHostname.toLowerCase()) {
        await api.setHostname(normalizedHostname);
        setInitialHostname(normalizedHostname);
      }
      setPendingApply(applied);
    } catch (error) {
      handleSessionError(error, setApplyError);
    } finally {
      setApplyBusy(false);
    }
  }

  async function finishApply(action: 'confirmation' | 'rollback') {
    if (!pendingApply) return;
    setConfirmBusy(true);
    setApplyError('');
    try {
      await api.finishApply(pendingApply.id, action);
      setPendingApply(null);
      if (action === 'confirmation') {
        setCompleted(true);
        onComplete?.();
      }
    } catch (error) {
      handleSessionError(error, setApplyError);
    } finally {
      setConfirmBusy(false);
    }
  }

  return (
    <Stack className="details" gap="md">
      <Group justify="space-between" align="flex-end" wrap="wrap">
        <div>
          <Title order={2}>Setup assistant</Title>
          <Text c="dimmed">Guided initial configuration for hostname, time zone, management LAN, and WAN.</Text>
        </div>
        {onComplete && !pendingApply && (
          <Button variant="subtle" onClick={onComplete}>Skip to dashboard</Button>
        )}
      </Group>

      {hasExistingConfig && !pendingApply && !completed && (
        <Alert color="yellow" role="status" title="Existing configuration detected">
          This appliance already has saved network objects. Saving from the setup assistant adds new zones, links, and segments alongside them; review carefully before applying.
        </Alert>
      )}

      {loadError && (
        <Alert color="red" role="alert" title="Could not read appliance hardware">
          <Group justify="space-between" wrap="wrap">
            <Text size="sm">{loadError}</Text>
            <Button size="xs" variant="default" onClick={() => void loadHostData()}>Retry</Button>
          </Group>
        </Alert>
      )}

      {loadingHost ? (
        <Loader aria-label="Loading appliance hardware" />
      ) : (
        <Paper withBorder p="lg">
          <Stepper active={step} onStepClick={(next) => { if (!pendingApply && next < step) setStep(next); }}>
            <Stepper.Step label="System" description="Hostname & time zone">
              <Stack gap="md" mt="md">
                <TextInput
                  label="Appliance hostname"
                  description="Single DNS label (lowercase letters, digits, hyphens)."
                  placeholder="andaru-gw"
                  value={hostname}
                  error={hostnameValid ? undefined : 'Use 1 to 63 lowercase letters, digits, or hyphens.'}
                  onChange={(e) => setHostname(e.currentTarget.value)}
                />
                <Select
                  label="Display time zone"
                  description="Used for timestamps across the web interface."
                  data={zoneChoices}
                  value={selectedZone}
                  searchable
                  allowDeselect={false}
                  onChange={(v) => { if (v) setSelectedZone(v); }}
                />
                <Text size="sm" c="dimmed" className="tabular">Preview: {formatTimestamp(previewAt, selectedZone)}</Text>
                <Group justify="flex-end">
                  <Button disabled={!hostnameValid} onClick={() => setStep(1)}>Next: Management LAN</Button>
                </Group>
              </Stack>
            </Stepper.Step>

            <Stepper.Step label="Management" description="LAN & services">
              <Stack gap="md" mt="md">
                {links.length === 0 ? (
                  <Alert color="yellow" role="status">No physical network interfaces with a MAC address were reported.</Alert>
                ) : (
                  <Select
                    label="Management interface"
                    data={mgmtOptions}
                    value={draft.mgmtMac}
                    allowDeselect={false}
                    onChange={(mac) => {
                      if (!mac) return;
                      const found = links.find((l) => l.linkAddress === mac);
                      updateDraft({
                        mgmtMac: mac,
                        mgmtName: found?.name ?? draft.mgmtName,
                        wanMac: draft.wanMac === mac ? '' : draft.wanMac,
                      });
                    }}
                  />
                )}

                <div>
                  <Text size="sm" fw={500} mb={4}>IPv4 addressing</Text>
                  <SegmentedControl
                    value={draft.mgmtIpv4Mode}
                    data={[
                      { value: 'static', label: 'Static (recommended)' },
                      { value: 'dhcp', label: 'DHCP client' },
                    ]}
                    onChange={(v) => updateDraft({ mgmtIpv4Mode: v as SetupNetworkDraft['mgmtIpv4Mode'] })}
                  />
                </div>

                {draft.mgmtIpv4Mode === 'dhcp' ? (
                  <Alert color="yellow" role="status" title="DHCP management warning">
                    Using DHCP on management treats it as an uplink; its IP address is not known in advance and factory reset recovery will end at the local console.
                  </Alert>
                ) : (
                  <>
                    <TextInput
                      label="Management IPv4 address (CIDR)"
                      placeholder="192.168.1.1/24"
                      value={draft.mgmtIpv4Address}
                      onChange={(e) => {
                        const val = e.currentTarget.value;
                        const range = defaultDhcpRange(val);
                        updateDraft({
                          mgmtIpv4Address: val,
                          ...(range ? { dhcpStart: range.start, dhcpEnd: range.end } : {}),
                        });
                      }}
                    />
                    <Switch
                      label="Serve DHCP on Management LAN"
                      checked={draft.serveDhcp}
                      onChange={(e) => updateDraft({ serveDhcp: e.currentTarget.checked })}
                    />
                    {draft.serveDhcp && (
                      <div className="setup-grid">
                        <TextInput label="DHCP pool start" value={draft.dhcpStart} onChange={(e) => updateDraft({ dhcpStart: e.currentTarget.value })} />
                        <TextInput label="DHCP pool end" value={draft.dhcpEnd} onChange={(e) => updateDraft({ dhcpEnd: e.currentTarget.value })} />
                      </div>
                    )}
                    <Switch
                      label="Serve DNS on Management LAN"
                      checked={draft.serveDns}
                      onChange={(e) => updateDraft({ serveDns: e.currentTarget.checked })}
                    />
                  </>
                )}

                <div>
                  <Text size="sm" fw={500} mb={4}>IPv6 addressing</Text>
                  <SegmentedControl
                    value={draft.mgmtIpv6Mode}
                    data={[
                      { value: 'none', label: 'None' },
                      { value: 'static', label: 'Static' },
                      { value: 'delegated', label: 'Delegated from WAN', disabled: !wanSupportsDelegation },
                    ]}
                    onChange={(v) => updateDraft({ mgmtIpv6Mode: v as SetupNetworkDraft['mgmtIpv6Mode'] })}
                  />
                </div>

                {draft.mgmtIpv6Mode === 'static' && (
                  <TextInput
                    label="Management IPv6 address (CIDR)"
                    placeholder="fd00:1::1/64"
                    value={draft.mgmtIpv6Address}
                    onChange={(e) => updateDraft({ mgmtIpv6Address: e.currentTarget.value })}
                  />
                )}

                <Group justify="space-between">
                  <Button variant="default" onClick={() => setStep(0)}>Back</Button>
                  <Button disabled={!canAdvanceMgmt} onClick={() => setStep(2)}>Next: WAN</Button>
                </Group>
              </Stack>
            </Stepper.Step>

            <Stepper.Step label="WAN" description="Internet uplink">
              <Stack gap="md" mt="md">
                <Select
                  label="WAN interface"
                  data={wanOptions}
                  value={draft.wanMac}
                  allowDeselect={false}
                  onChange={(mac) => {
                    const value = mac ?? '';
                    const found = links.find((l) => l.linkAddress === value);
                    updateDraft({ wanMac: value, wanName: found?.name ?? 'wan0' });
                  }}
                />

                {draft.wanMac && (
                  <>
                    <div>
                      <Text size="sm" fw={500} mb={4}>WAN IPv4</Text>
                      <SegmentedControl
                        value={draft.wanIpv4Mode}
                        data={[
                          { value: 'dhcp', label: 'DHCP' },
                          { value: 'static', label: 'Static' },
                          { value: 'none', label: 'None' },
                        ]}
                        onChange={(v) => updateDraft({ wanIpv4Mode: v as SetupNetworkDraft['wanIpv4Mode'] })}
                      />
                    </div>

                    {draft.wanIpv4Mode === 'static' && (
                      <div className="setup-grid">
                        <TextInput label="WAN IPv4 address (CIDR)" placeholder="203.0.113.2/30" value={draft.wanIpv4Address} onChange={(e) => updateDraft({ wanIpv4Address: e.currentTarget.value })} />
                        <TextInput label="IPv4 gateway" placeholder="203.0.113.1" value={draft.wanIpv4Gateway} onChange={(e) => updateDraft({ wanIpv4Gateway: e.currentTarget.value })} />
                      </div>
                    )}

                    <div>
                      <Text size="sm" fw={500} mb={4}>WAN IPv6</Text>
                      <SegmentedControl
                        value={draft.wanIpv6Mode}
                        data={[
                          { value: 'none', label: 'None' },
                          { value: 'dhcp6', label: 'DHCPv6' },
                          { value: 'slaac', label: 'SLAAC' },
                          { value: 'static', label: 'Static' },
                        ]}
                        onChange={(v) => updateDraft({ wanIpv6Mode: v as SetupNetworkDraft['wanIpv6Mode'] })}
                      />
                    </div>

                    {draft.wanIpv6Mode === 'dhcp6' && (
                      <TextInput
                        label="Requested prefix delegation length (optional)"
                        placeholder="56"
                        value={draft.wanDhcp6PrefixLen}
                        onChange={(e) => updateDraft({ wanDhcp6PrefixLen: e.currentTarget.value })}
                      />
                    )}

                    {draft.wanIpv6Mode === 'static' && (
                      <div className="setup-grid">
                        <TextInput label="WAN IPv6 address (CIDR)" placeholder="2001:db8::2/64" value={draft.wanIpv6Address} onChange={(e) => updateDraft({ wanIpv6Address: e.currentTarget.value })} />
                        <TextInput label="IPv6 gateway" placeholder="2001:db8::1" value={draft.wanIpv6Gateway} onChange={(e) => updateDraft({ wanIpv6Gateway: e.currentTarget.value })} />
                      </div>
                    )}

                    {(draft.wanIpv4Mode === 'static' || draft.wanIpv6Mode === 'static') && (
                      <TextInput
                        label="Upstream DNS servers (optional)"
                        description="Comma- or space-separated IPv4/IPv6 addresses."
                        placeholder="1.1.1.1, 9.9.9.9"
                        value={draft.wanDnsServers}
                        onChange={(e) => updateDraft({ wanDnsServers: e.currentTarget.value })}
                      />
                    )}
                  </>
                )}

                <Group justify="space-between">
                  <Button variant="default" onClick={() => setStep(1)}>Back</Button>
                  <Button disabled={!canAdvanceWan} onClick={() => setStep(3)}>Next: Review & Apply</Button>
                </Group>
              </Stack>
            </Stepper.Step>

            <Stepper.Step label="Review & Apply" description="Verify & confirm">
              <Stack gap="md" mt="md">
                {applyError && <Alert color="red" role="alert" title="Apply failed">{applyError}</Alert>}

                {pendingApply ? (
                  <Alert color="orange" role="status" title="Configuration applied — confirm connectivity">
                    <Stack gap="sm">
                      <Text size="sm">
                        Apply #{pendingApply.id} is live and awaiting confirmation. If you do not confirm before{' '}
                        <strong>{pendingApply.deadline ? formatTime(pendingApply.deadline, selectedZone) : 'the rollback timer expires'}</strong>,
                        Andaru will automatically roll back to the previous network state.
                      </Text>
                      <Group>
                        <Button color="green" loading={confirmBusy} onClick={() => void finishApply('confirmation')}>Confirm apply</Button>
                        <Button color="red" variant="light" loading={confirmBusy} onClick={() => void finishApply('rollback')}>Roll back now</Button>
                      </Group>
                    </Stack>
                  </Alert>
                ) : (
                  <>
                    <Facts>
                      <Row label="Hostname">{normalizedHostname || initialHostname || 'unchanged'}</Row>
                      <Row label="Display time zone">{selectedZone}</Row>
                      <Row label="Management LAN">
                        {draft.mgmtName} (<Text span ff="monospace" inherit>{draft.mgmtMac}</Text>) ·{' '}
                        {draft.mgmtIpv4Mode === 'static' ? draft.mgmtIpv4Address : 'DHCP'}
                        {resolvedMgmtIpv6 !== 'none' ? ` · IPv6 ${resolvedMgmtIpv6 === 'static' ? draft.mgmtIpv6Address : 'delegated'}` : ''}
                      </Row>
                      <Row label="LAN services">
                        {draft.mgmtIpv4Mode === 'static'
                          ? `${draft.serveDhcp ? `DHCP (${draft.dhcpStart}–${draft.dhcpEnd})` : 'No DHCP'} · ${draft.serveDns ? 'DNS' : 'No DNS'}`
                          : 'None (DHCP client management)'}
                      </Row>
                      <Row label="WAN">
                        {draft.wanMac ? (
                          <>
                            {draft.wanName} (<Text span ff="monospace" inherit>{draft.wanMac}</Text>) · IPv4 {draft.wanIpv4Mode}
                            {draft.wanIpv6Mode !== 'none' ? ` · IPv6 ${draft.wanIpv6Mode}` : ''} ·{' '}
                            <Badge size="sm" variant="light">Automatic outbound NAT</Badge>
                          </>
                        ) : (
                          'None yet'
                        )}
                      </Row>
                    </Facts>
                    <Text size="sm" c="dimmed">
                      Changes are saved atomically, validated against the live hardware, and applied with a {applyConfirmSeconds}-second automatic rollback timer.
                    </Text>
                    <Group justify="space-between">
                      <Button variant="default" disabled={applyBusy} onClick={() => setStep(2)}>Back</Button>
                      <Button loading={applyBusy} disabled={!draft.mgmtMac} onClick={() => void saveAndApply()}>Save and apply</Button>
                    </Group>
                  </>
                )}
              </Stack>
            </Stepper.Step>
          </Stepper>
        </Paper>
      )}
    </Stack>
  );
}
