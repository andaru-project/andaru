import { Alert, Anchor, Badge, Button, Checkbox, Fieldset, Group, Loader, Paper, SegmentedControl, Stack, Switch, Text, TextInput, Title } from '@mantine/core';
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { Link } from 'react-router';
import { ApiClient, ApiError, saveFailure } from '../api';
import {
  checkDns, dnsDraftOf, dnsErrors, dnsListenAddresses, dnsPayload, isDnsEligibleSegment, newDnsRowKey, pruneDnsDraft, segmentsWithHostDns, type DnsDraft,
} from '../dnsEdit';
import type { DnsMode, DnsService, DnsUpstreamProtocol, SavedObject, Session, Snapshot } from '../types';
import { useResource } from '../useResource';
import { ZoneBadge } from './ZoneBadge';

interface DnsScreenProps {
  api: ApiClient;
  session: Session;
  onSaved(): void;
  onSessionLost(): void;
}

const mono = { input: 'mono-input' };

export function DnsScreen({ api, session, onSaved, onSessionLost }: DnsScreenProps) {
  const canWrite = session.capabilities.includes('configuration.write');

  const load = useCallback(async () => {
    const [snapshot, dns] = await Promise.all([api.config(), api.dns()]);
    return { snapshot, dns };
  }, [api]);

  const resource = useResource(load, onSessionLost);

  const [enabling, setEnabling] = useState(false);
  const [draft, setDraft] = useState<DnsDraft | null>(null);
  const [dirty, setDirty] = useState(false);
  const [seenData, setSeenData] = useState<{ snapshot: Snapshot; dns: SavedObject<DnsService> | null } | undefined>();
  const [errors, setErrors] = useState(new Map<string, string>());
  const [errorFocusTick, setErrorFocusTick] = useState(0);
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const [disabling, setDisabling] = useState(false);

  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
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

  if (resource.data !== seenData) {
    setSeenData(resource.data);
    if (resource.data && (!dirty || draft === null)) {
      setDraft(dnsDraftOf(resource.data.dns?.value, resource.data.snapshot));
      if (resource.data.dns) setEnabling(false);
    }
  }

  if (!resource.data) {
    return resource.error
      ? <Alert color="red" role="alert" title="DNS request failed">{resource.error}</Alert>
      : <Loader aria-label="Loading DNS" />;
  }

  const { snapshot, dns } = resource.data;
  const activeDraft = draft ?? dnsDraftOf(dns?.value, snapshot);
  const segments = snapshot.value.segments;
  const zones = new Map(snapshot.value.zones.map((z) => [z.id, z.name]));
  const dhcp4Served = new Set((snapshot.value.dhcp ?? []).filter((d) => !d.dnsServers?.length).map((d) => d.segment));
  const dhcp6Served = new Set((snapshot.value.dhcp6 ?? []).filter((d) => !d.dnsServers?.length).map((d) => d.segment));
  const hostDnsSegments = segmentsWithHostDns(segments);
  const selectedSegmentSet = new Set(activeDraft.segments);
  const selectableSegments = segments.filter((s) => isDnsEligibleSegment(s) || selectedSegmentSet.has(s.id));
  const virtualAddresses = snapshot.value.virtualAddresses ?? [];
  const showVirtualAddresses = virtualAddresses.length > 0 || activeDraft.virtualAddresses.length > 0;
  const needsManagementForVips = activeDraft.virtualAddresses.length > 0 && !snapshot.value.management?.segment;
  const showForm = Boolean(dns) || enabling;

  const patchDraft = (patch: Partial<DnsDraft>) => {
    setDraft((prev) => ({ ...(prev ?? activeDraft), ...patch }));
    setDirty(true);
  };

  function startEnable() {
    setDraft(dnsDraftOf(null, snapshot));
    setEnabling(true);
    setDirty(false);
    setErrors(new Map());
    setFailure('');
    setDisabling(false);
  }

  function resetForm() {
    setDraft(dnsDraftOf(dns?.value, snapshot));
    setDirty(false);
    setErrors(new Map());
    setFailure('');
    setDisabling(false);
    if (!dns) setEnabling(false);
  }

  function changeMode(mode: DnsMode) {
    setDraft((prev) => {
      const base = prev ?? activeDraft;
      const upstreams = mode === 'forward' && base.upstreams.length === 0
        ? [{ key: newDnsRowKey(), address: '', tlsName: '' }]
        : base.upstreams;
      return { ...base, mode, upstreams };
    });
    setDirty(true);
  }

  function toggleSegment(id: string, checked: boolean) {
    setDraft((prev) => {
      const base = prev ?? activeDraft;
      const next = checked
        ? Array.from(new Set([...base.segments, id]))
        : base.segments.filter((item) => item !== id);
      return { ...base, segments: next };
    });
    setDirty(true);
  }

  function toggleVirtualAddress(id: string, checked: boolean) {
    setDraft((prev) => {
      const base = prev ?? activeDraft;
      const next = checked
        ? Array.from(new Set([...base.virtualAddresses, id]))
        : base.virtualAddresses.filter((item) => item !== id);
      return { ...base, virtualAddresses: next };
    });
    setDirty(true);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!canWrite || busy) return;
    const pruned = pruneDnsDraft(activeDraft);
    setDraft(pruned);
    const found = checkDns(pruned);
    if (found.size > 0) {
      setErrors(found);
      setFailure('');
      setErrorFocusTick((n) => n + 1);
      return;
    }

    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      await api.saveDns(dnsPayload(pruned), dns?.version ?? '0');
      if (!mounted.current) return;
      setDirty(false);
      setEnabling(false);
      setDisabling(false);
      onSaved();
      void resource.reload();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      if (!mounted.current) return;
      const fields = dnsErrors(error);
      setErrors(fields);
      if (fields.size > 0) setErrorFocusTick((n) => n + 1);
      setFailure(saveFailure(error, fields, 'DNS service'));
    } finally {
      if (mounted.current) setBusy(false);
    }
  }

  async function confirmDisable() {
    if (!canWrite || !dns || busy) return;
    if (!disabling) {
      setDisabling(true);
      return;
    }
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      await api.deleteDns(dns.version);
      if (!mounted.current) return;
      setDirty(false);
      setEnabling(false);
      setDisabling(false);
      onSaved();
      void resource.reload();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return onSessionLost();
      if (!mounted.current) return;
      const fields = dnsErrors(error);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'DNS service'));
    } finally {
      if (mounted.current) setBusy(false);
    }
  }

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div>
          <Group gap="xs" align="center">
            <Title order={2}>DNS</Title>
            {dns ? (
              <Badge variant="dot" color="green">Serving</Badge>
            ) : enabling ? (
              <Badge variant="dot" color="blue">Configuring</Badge>
            ) : (
              <Badge variant="dot" color="gray">Off</Badge>
            )}
          </Group>
          <Text c="dimmed">Local DNS resolver, forwarding and DNS over TLS upstreams, served segments, and DHCP lease registration.</Text>
        </div>
        <Button size="xs" variant="default" loading={resource.loading} onClick={() => void resource.reload()}>Refresh</Button>
      </Group>

      {resource.error && <Alert color="red" role="alert" title="DNS request failed">{resource.error}</Alert>}

      {hostDnsSegments.length > 0 && (
        <Alert color="yellow" title="Per-segment DNS servers must be moved first">
          {hostDnsSegments.map((s) => s.name).join(', ')}{' '}
          {hostDnsSegments.length === 1 ? 'declares' : 'declare'} static DNS servers under interface addressing. Remove them on{' '}
          <Anchor component={Link} to="/network/interfaces">Interfaces</Anchor> before saving the DNS service, and place any upstream servers here instead.
        </Alert>
      )}

      {!showForm ? (
        <Paper withBorder p="md" radius="sm">
          <Stack gap="sm" align="flex-start">
            <Text fw={600}>DNS service is off</Text>
            <Text size="sm" c="dimmed">
              Enable the DNS service to run a recursive or forwarding resolver on selected segments, serve local hostnames, and automatically register DHCP leases.
            </Text>
            {canWrite && (
              <Button onClick={startEnable}>Enable DNS service</Button>
            )}
          </Stack>
        </Paper>
      ) : (
        <form ref={formRef} onSubmit={(e) => void submit(e)} noValidate>
          <Stack>
            {!dns && (
              <Text size="sm" c="dimmed">
                Saving enables the DNS service in the draft configuration. Nothing changes on the box until applied.
              </Text>
            )}
            {failure && <Alert ref={failureRef} color="red" role="alert" title="The DNS service was not saved">{failure}</Alert>}
            {needsManagementForVips && (
              <Alert color="yellow" title="Virtual addresses require a management segment">
                Applying DNS on virtual addresses requires the packet filter, which needs a management segment configured first.
              </Alert>
            )}

            <Fieldset variant="unstyled" disabled={!canWrite || busy || disabling}>
              <Stack>
                <Fieldset legend="Resolver mode">
                  <Stack gap="sm">
                    <SegmentedControl
                      aria-label="Resolver mode"
                      value={activeDraft.mode}
                      onChange={(value) => changeMode(value as DnsMode)}
                      data={[
                        { value: 'recursive', label: 'Recursive' },
                        { value: 'forward', label: 'Forward to upstreams' },
                      ]}
                    />
                    <Text size="xs" c="dimmed">
                      {activeDraft.mode === 'recursive'
                        ? 'Resolves queries directly from the root DNS servers with DNSSEC validation. No external upstream resolver is used.'
                        : 'Forwards all non-local queries to the upstream servers listed below.'}
                    </Text>

                    {activeDraft.mode === 'forward' && (
                      <Stack gap="xs">
                        <Group justify="space-between" align="flex-end" wrap="wrap">
                          <Stack gap={4}>
                            <Text size="sm" fw={600}>Upstream protocol</Text>
                            <SegmentedControl
                              size="xs"
                              aria-label="Upstream protocol"
                              value={activeDraft.protocol}
                              onChange={(value) => patchDraft({ protocol: value as DnsUpstreamProtocol })}
                              data={[
                                { value: 'do53', label: 'Plain DNS (port 53)' },
                                { value: 'dot', label: 'DNS over TLS (port 853)' },
                              ]}
                            />
                          </Stack>
                          {canWrite && (
                            <Button
                              size="compact-xs"
                              variant="light"
                              disabled={activeDraft.upstreams.length >= 8}
                              onClick={() => patchDraft({
                                upstreams: [...activeDraft.upstreams, { key: newDnsRowKey(), address: '', tlsName: '' }],
                              })}
                            >
                              Add upstream
                            </Button>
                          )}
                        </Group>
                        {errors.get('upstreams') && <Text size="xs" className="error-text">{errors.get('upstreams')}</Text>}
                        {activeDraft.upstreams.length === 0 && (
                          <Text size="xs" c="dimmed">No upstreams configured. Add at least one upstream IP address.</Text>
                        )}
                        {activeDraft.upstreams.map((upstream, i) => (
                          <Group key={upstream.key} align="flex-start" gap="xs" wrap="wrap" className="dhcp-repeat-row">
                            <TextInput
                              className="grow"
                              classNames={mono}
                              label={i === 0 ? 'Server address' : undefined}
                              aria-label={`Upstream ${i + 1} address`}
                              placeholder={activeDraft.protocol === 'dot' ? '1.1.1.1 or 1.1.1.1:853' : '1.1.1.1 or [2606:4700:4700::1111]:53'}
                              required
                              value={upstream.address}
                              error={errors.get(`upstreams/${i}/address`)}
                              onChange={(e) => {
                                const address = e.currentTarget.value;
                                patchDraft({
                                  upstreams: activeDraft.upstreams.map((item, idx) => (idx === i ? { ...item, address } : item)),
                                });
                              }}
                            />
                            {activeDraft.protocol === 'dot' && (
                              <TextInput
                                className="grow"
                                classNames={mono}
                                label={i === 0 ? 'TLS server name' : undefined}
                                aria-label={`Upstream ${i + 1} TLS server name`}
                                placeholder="cloudflare-dns.com"
                                required
                                value={upstream.tlsName}
                                error={errors.get(`upstreams/${i}/tlsName`)}
                                onChange={(e) => {
                                  const tlsName = e.currentTarget.value;
                                  patchDraft({
                                    upstreams: activeDraft.upstreams.map((item, idx) => (idx === i ? { ...item, tlsName } : item)),
                                  });
                                }}
                              />
                            )}
                            {canWrite && (
                              <Button
                                size="compact-xs"
                                variant="subtle"
                                color="red"
                                mt={i === 0 ? 28 : 6}
                                aria-label={`Remove upstream ${i + 1}`}
                                onClick={() => patchDraft({
                                  upstreams: activeDraft.upstreams.filter((_, idx) => idx !== i),
                                })}
                              >
                                Remove
                              </Button>
                            )}
                          </Group>
                        ))}
                      </Stack>
                    )}
                  </Stack>
                </Fieldset>

                <Fieldset legend="Served segments">
                  <Stack gap="xs">
                    <Text size="xs" c="dimmed">
                      Segments where the DNS service listens on port 53. Requires a static IPv4 or static IPv6 address on the segment.
                    </Text>
                    {errors.get('segments') && <Text size="xs" className="error-text">{errors.get('segments')}</Text>}
                    {selectableSegments.length === 0 ? (
                      <Text size="sm" c="dimmed">
                        No segments have a static IPv4 or IPv6 address. Configure addressing on{' '}
                        <Anchor component={Link} to="/network/interfaces">Interfaces</Anchor>.
                      </Text>
                    ) : (
                      <Stack gap="xs">
                        {selectableSegments.map((segment) => {
                          const addrs = dnsListenAddresses(segment);
                          const checked = selectedSegmentSet.has(segment.id);
                          const hasDefaultDhcp = dhcp4Served.has(segment.id) || dhcp6Served.has(segment.id);
                          return (
                            <Group key={segment.id} justify="space-between" align="center" wrap="wrap" className="dhcp-repeat-row">
                              <Checkbox
                                checked={checked}
                                onChange={(e) => toggleSegment(segment.id, e.currentTarget.checked)}
                                label={
                                  <Group gap="xs" wrap="wrap">
                                    <Text fw={600} size="sm">{segment.name}</Text>
                                    <ZoneBadge name={zones.get(segment.zone) ?? segment.zone} />
                                  </Group>
                                }
                              />
                              <Group gap="xs" wrap="wrap">
                                {addrs.length > 0
                                  ? <Text size="xs" c="dimmed" ff="monospace">{addrs.join(', ')}</Text>
                                  : <Text size="xs" className="warning-text">No static listen address</Text>}
                                {hasDefaultDhcp && (
                                  <Badge size="xs" variant="light" color="gray" className="plain-badge">DHCP default</Badge>
                                )}
                              </Group>
                            </Group>
                          );
                        })}
                      </Stack>
                    )}

                    {showVirtualAddresses && (
                      <Stack gap="xs" mt="xs">
                        <Text size="sm" fw={600}>Virtual addresses</Text>
                        <Text size="xs" c="dimmed">Optional firewall virtual addresses that the DNS service also listens on.</Text>
                        {errors.get('virtualAddresses') && <Text size="xs" className="error-text">{errors.get('virtualAddresses')}</Text>}
                        {virtualAddresses.map((vip) => (
                          <Group key={vip.id} justify="space-between" align="center" wrap="wrap" className="dhcp-repeat-row">
                            <Checkbox
                              checked={activeDraft.virtualAddresses.includes(vip.id)}
                              onChange={(e) => toggleVirtualAddress(vip.id, e.currentTarget.checked)}
                              label={<Text fw={600} size="sm">{vip.name}</Text>}
                            />
                            <Text size="xs" c="dimmed" ff="monospace">{vip.address}</Text>
                          </Group>
                        ))}
                      </Stack>
                    )}
                  </Stack>
                </Fieldset>

                <Fieldset legend="Local names & DHCP leases">
                  <Stack gap="xs">
                    <TextInput
                      label="Local domain"
                      description="Domain suffix for local hostnames and registered DHCP leases (defaults to home.arpa)."
                      placeholder="home.arpa"
                      maxLength={189}
                      value={activeDraft.localDomain}
                      error={errors.get('localDomain')}
                      onChange={(e) => patchDraft({ localDomain: e.currentTarget.value })}
                    />
                    <TextInput
                      label="Search domains"
                      description="Optional comma-separated DNS search list (up to 8 domains)."
                      placeholder="home.arpa, lab.example"
                      value={activeDraft.searchDomains}
                      error={errors.get('searchDomains')}
                      onChange={(e) => patchDraft({ searchDomains: e.currentTarget.value })}
                    />
                    <Switch
                      label="Register DHCP leases in local DNS"
                      description="Automatically publish active DHCPv4/DHCPv6 hostnames and static leases under the local domain."
                      checked={activeDraft.registerLeases}
                      onChange={(e) => patchDraft({ registerLeases: e.currentTarget.checked })}
                    />
                  </Stack>
                </Fieldset>
              </Stack>
            </Fieldset>

            {disabling && (
              <Alert color="red" role="alert" title="Disable the DNS service?">
                The firewall will stop running its local DNS resolver once applied. Clients that relied on Andaru DNS will need another DNS server configured.
              </Alert>
            )}

            {canWrite && (
              <Group justify="space-between">
                <div>
                  {dns && !disabling && (
                    <Button variant="subtle" color="red" disabled={busy} onClick={() => void confirmDisable()}>Disable DNS…</Button>
                  )}
                  {disabling && (
                    <Button color="red" loading={busy} onClick={() => void confirmDisable()}>Disable DNS</Button>
                  )}
                </div>
                <Group gap="xs">
                  {(dirty || !dns || disabling) && (
                    <Button
                      ref={keepRef}
                      variant="default"
                      disabled={busy}
                      onClick={() => (disabling ? setDisabling(false) : resetForm())}
                    >
                      {disabling ? 'Keep DNS' : dns ? 'Reset' : 'Cancel'}
                    </Button>
                  )}
                  {!disabling && (
                    <Button type="submit" loading={busy} disabled={Boolean(dns) && !dirty}>
                      {dns ? 'Save' : 'Enable DNS'}
                    </Button>
                  )}
                </Group>
              </Group>
            )}
          </Stack>
        </form>
      )}
    </Stack>
  );
}
