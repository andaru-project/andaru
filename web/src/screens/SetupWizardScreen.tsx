import {
  Alert,
  Badge,
  Button,
  Code,
  Group,
  Image,
  Loader,
  Paper,
  PasswordInput,
  SegmentedControl,
  Select,
  Stack,
  Stepper,
  Switch,
  Table,
  Text,
  Textarea,
  TextInput,
  Title,
} from '@mantine/core';
import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react';
import mark from '../andaru-icon.svg';
import { ApiClient, apiErrorMessage, reportError } from '../api';
import {
  buildSetupOperations,
  defaultDhcpRange,
  type MgmtIPv4Mode,
  type MgmtIPv6Mode,
  type SetupNetworkDraft,
  type WanIPv4Mode,
  type WanIPv6Mode,
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
  session?: Session | null;
  onBootstrapped?(session: Session): void;
  onComplete?(): void;
  onSessionLost?(): void;
}

function linkOptionLabel(link: LinkStatus): string {
  const mac = link.linkAddress || 'no MAC';
  const ips = link.addresses.map((a) => `${a.address}/${a.prefixLength}`).join(', ');
  return `${link.name} (${mac}) · ${link.operState}${ips ? ` · ${ips}` : ''}`;
}

export function SetupWizardScreen({ api, session, onBootstrapped, onComplete, onSessionLost }: SetupWizardScreenProps) {
  const preAuth = !session;
  const activeTimeZone = useTimeZone();
  const zoneChoices = useMemo(() => timeZoneOptions(activeTimeZone), [activeTimeZone]);

  // Pre-auth bootstrap fields (Step 0 when no admin exists).
  const [setupCode, setSetupCode] = useState('');
  const [login, setLogin] = useState('admin');
  const [displayName, setDisplayName] = useState('Administrator');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [sshPublicKey, setSshPublicKey] = useState('');
  const [bootstrapBusy, setBootstrapBusy] = useState(false);
  const [bootstrapError, setBootstrapError] = useState('');

  // Authenticated wizard state (Steps 0..3 when signed in).
  const [step, setStep] = useState(0);
  const [loadingHost, setLoadingHost] = useState(!preAuth);
  const [loadError, setLoadError] = useState('');
  const [links, setLinks] = useState<LinkStatus[]>([]);
  const [initialHostname, setInitialHostname] = useState('');
  const [hostname, setHostname] = useState('');
  const [selectedZone, setSelectedZone] = useState(activeTimeZone);
  const [previewAt] = useState(() => Date.now());
  const [savedVersion, setSavedVersion] = useState<string | undefined>(undefined);
  const [existingConfigCount, setExistingConfigCount] = useState(0);

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

  const handleSessionError = useCallback((error: unknown, setMsg: (msg: string) => void) => {
    if (onSessionLost) reportError(error, onSessionLost, setMsg);
    else setMsg(apiErrorMessage(error));
  }, [onSessionLost]);

  const loadHostData = useCallback(async () => {
    if (!session) return;
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
        setExistingConfigCount(snap.value.links.length + snap.value.segments.length);
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
  }, [api, session, handleSessionError]);

  // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount, not derived state
  useEffect(() => { if (session) void loadHostData(); }, [session, loadHostData]);

  async function submitBootstrap(event: FormEvent) {
    event.preventDefault();
    setBootstrapError('');
    if (password !== confirmPassword) {
      setBootstrapError('Passwords do not match.');
      return;
    }
    if (password.length < 8) {
      setBootstrapError('Password must be at least 8 characters.');
      return;
    }
    setBootstrapBusy(true);
    try {
      const created = await api.bootstrapSetup({
        setupCode: setupCode.trim(),
        login: login.trim(),
        displayName: displayName.trim() || login.trim(),
        password,
        ...(sshPublicKey.trim() ? { sshPublicKey: sshPublicKey.trim() } : {}),
      });
      setPassword('');
      setConfirmPassword('');
      onBootstrapped?.(created);
    } catch (error) {
      setBootstrapError(apiErrorMessage(error));
    } finally {
      setBootstrapBusy(false);
    }
  }

  // Pre-auth full-page wizard (Step 1: create administrator with console setup code).
  if (preAuth) {
    return (
      <div className="login-page">
        <Paper className="setup-card" component="main" withBorder p="xl">
          <form onSubmit={submitBootstrap}>
            <Stack gap="md">
              <Group gap="sm" wrap="nowrap">
                <Image src={mark} alt="" w={40} h={40} />
                <div>
                  <Title order={1} size="h2">Andaru setup assistant</Title>
                  <Text c="dimmed" size="sm">Create the first administrator account, then configure management and WAN networking.</Text>
                </div>
              </Group>

              <Alert color="blue" role="status" title="One-time setup code required">
                <Stack gap="xs">
                  <Text size="sm">On the local appliance console (screen or serial), run this command to display the one-time setup code:</Text>
                  <Code block>sudo andaru ctl setup-code</Code>
                </Stack>
              </Alert>

              {bootstrapError && <Alert color="red" role="alert" title="Setup failed">{bootstrapError}</Alert>}

              <TextInput
                label="One-time setup code"
                description="16-character code shown on the appliance console (dashes optional)."
                placeholder="XXXX-XXXX-XXXX-XXXX"
                required
                autoFocus
                value={setupCode}
                onChange={(e) => setSetupCode(e.currentTarget.value)}
              />

              <div className="setup-grid">
                <TextInput
                  label="Administrator login"
                  autoComplete="username"
                  required
                  maxLength={64}
                  value={login}
                  onChange={(e) => setLogin(e.currentTarget.value)}
                />
                <TextInput
                  label="Display name"
                  maxLength={128}
                  value={displayName}
                  onChange={(e) => setDisplayName(e.currentTarget.value)}
                />
              </div>

              <div className="setup-grid">
                <PasswordInput
                  label="Password"
                  autoComplete="new-password"
                  required
                  minLength={8}
                  maxLength={512}
                  value={password}
                  onChange={(e) => setPassword(e.currentTarget.value)}
                />
                <PasswordInput
                  label="Confirm password"
                  autoComplete="new-password"
                  required
                  minLength={8}
                  maxLength={512}
                  value={confirmPassword}
                  onChange={(e) => setConfirmPassword(e.currentTarget.value)}
                />
              </div>

              <Textarea
                label="SSH public key (optional)"
                description="OpenSSH public key (e.g. ssh-ed25519 AAAA...) for root console access."
                placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... admin@workstation"
                minRows={2}
                styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
                value={sshPublicKey}
                onChange={(e) => setSshPublicKey(e.currentTarget.value)}
              />

              <Group justify="flex-end">
                <Button type="submit" loading={bootstrapBusy}>Create administrator and continue</Button>
              </Group>
            </Stack>
          </form>
        </Paper>
      </div>
    );
  }

  const normalizedHostname = hostname.trim().toLowerCase();
  const hostnameValid = normalizedHostname === '' || hostnamePattern.test(normalizedHostname);
  const mgmtOptions = links.map((l) => ({ value: l.linkAddress ?? '', label: linkOptionLabel(l) }));
  const wanOptions = [
    { value: '', label: 'None yet (configure WAN later)' },
    ...links.filter((l) => (l.linkAddress ?? '') !== draft.mgmtMac).map((l) => ({ value: l.linkAddress ?? '', label: linkOptionLabel(l) })),
  ];

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
      if (normalizedHostname && normalizedHostname !== initialHostname.toLowerCase()) {
        await api.setHostname(normalizedHostname);
        setInitialHostname(normalizedHostname);
      }
      const ops = buildSetupOperations(draft);
      const saved = await api.applyConfigChanges(ops, savedVersion);
      setSavedVersion(saved.savedVersion);
      const preview = await api.previewNetwork(saved.savedVersion);
      const errors = preview.findings.filter((f) => f.severity === 'error');
      if (errors.length > 0) {
        setApplyError(errors.map((e) => e.message).join(' '));
        return;
      }
      const applied = await api.apply(saved.savedVersion, applyConfirmSeconds);
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

      {existingConfigCount > 0 && !pendingApply && !completed && (
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
                  <Button disabled={!hostnameValid} onClick={() => { setTimeZone(selectedZone); setStep(1); }}>Next: Management LAN</Button>
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
                      setDraft((cur) => ({
                        ...cur,
                        mgmtMac: mac,
                        mgmtName: found?.name ?? cur.mgmtName,
                        wanMac: cur.wanMac === mac ? '' : cur.wanMac,
                      }));
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
                    onChange={(v) => setDraft((cur) => ({ ...cur, mgmtIpv4Mode: v as MgmtIPv4Mode }))}
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
                        setDraft((cur) => ({
                          ...cur,
                          mgmtIpv4Address: val,
                          ...(range ? { dhcpStart: range.start, dhcpEnd: range.end } : {}),
                        }));
                      }}
                    />
                    <Switch
                      label="Serve DHCP on Management LAN"
                      checked={draft.serveDhcp}
                      onChange={(e) => setDraft((cur) => ({ ...cur, serveDhcp: e.currentTarget.checked }))}
                    />
                    {draft.serveDhcp && (
                      <div className="setup-grid">
                        <TextInput label="DHCP pool start" value={draft.dhcpStart} onChange={(e) => setDraft((cur) => ({ ...cur, dhcpStart: e.currentTarget.value }))} />
                        <TextInput label="DHCP pool end" value={draft.dhcpEnd} onChange={(e) => setDraft((cur) => ({ ...cur, dhcpEnd: e.currentTarget.value }))} />
                      </div>
                    )}
                    <Switch
                      label="Serve recursive DNS on Management LAN"
                      checked={draft.serveDns}
                      onChange={(e) => setDraft((cur) => ({ ...cur, serveDns: e.currentTarget.checked }))}
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
                      { value: 'delegated', label: 'Delegated from WAN' },
                    ]}
                    onChange={(v) => setDraft((cur) => ({ ...cur, mgmtIpv6Mode: v as MgmtIPv6Mode }))}
                  />
                </div>

                {draft.mgmtIpv6Mode === 'static' && (
                  <TextInput
                    label="Management IPv6 address (CIDR)"
                    placeholder="fd00:1::1/64"
                    value={draft.mgmtIpv6Address}
                    onChange={(e) => setDraft((cur) => ({ ...cur, mgmtIpv6Address: e.currentTarget.value }))}
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
                    setDraft((cur) => ({ ...cur, wanMac: value, wanName: found?.name ?? 'wan0' }));
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
                        onChange={(v) => setDraft((cur) => ({ ...cur, wanIpv4Mode: v as WanIPv4Mode }))}
                      />
                    </div>

                    {draft.wanIpv4Mode === 'static' && (
                      <div className="setup-grid">
                        <TextInput label="WAN IPv4 address (CIDR)" placeholder="203.0.113.2/30" value={draft.wanIpv4Address} onChange={(e) => setDraft((cur) => ({ ...cur, wanIpv4Address: e.currentTarget.value }))} />
                        <TextInput label="IPv4 gateway" placeholder="203.0.113.1" value={draft.wanIpv4Gateway} onChange={(e) => setDraft((cur) => ({ ...cur, wanIpv4Gateway: e.currentTarget.value }))} />
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
                        onChange={(v) => setDraft((cur) => ({ ...cur, wanIpv6Mode: v as WanIPv6Mode }))}
                      />
                    </div>

                    {draft.wanIpv6Mode === 'dhcp6' && (
                      <TextInput
                        label="Requested prefix delegation length (optional)"
                        placeholder="56"
                        value={draft.wanDhcp6PrefixLen}
                        onChange={(e) => setDraft((cur) => ({ ...cur, wanDhcp6PrefixLen: e.currentTarget.value }))}
                      />
                    )}

                    {draft.wanIpv6Mode === 'static' && (
                      <div className="setup-grid">
                        <TextInput label="WAN IPv6 address (CIDR)" placeholder="2001:db8::2/64" value={draft.wanIpv6Address} onChange={(e) => setDraft((cur) => ({ ...cur, wanIpv6Address: e.currentTarget.value }))} />
                        <TextInput label="IPv6 gateway" placeholder="2001:db8::1" value={draft.wanIpv6Gateway} onChange={(e) => setDraft((cur) => ({ ...cur, wanIpv6Gateway: e.currentTarget.value }))} />
                      </div>
                    )}

                    {(draft.wanIpv4Mode === 'static' || draft.wanIpv6Mode === 'static') && (
                      <TextInput
                        label="Upstream DNS servers (optional)"
                        description="Comma- or space-separated IPv4/IPv6 addresses."
                        placeholder="1.1.1.1, 9.9.9.9"
                        value={draft.wanDnsServers}
                        onChange={(e) => setDraft((cur) => ({ ...cur, wanDnsServers: e.currentTarget.value }))}
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
                        {draft.mgmtIpv6Mode !== 'none' ? ` · IPv6 ${draft.mgmtIpv6Mode === 'static' ? draft.mgmtIpv6Address : 'delegated'}` : ''}
                      </Row>
                      <Row label="LAN services">
                        {draft.mgmtIpv4Mode === 'static'
                          ? `${draft.serveDhcp ? `DHCP (${draft.dhcpStart}–${draft.dhcpEnd})` : 'No DHCP'} · ${draft.serveDns ? 'Recursive DNS' : 'No DNS'}`
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
