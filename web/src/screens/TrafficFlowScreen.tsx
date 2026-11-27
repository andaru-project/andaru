import { Alert, Anchor, Badge, Button, Card, Group, NativeSelect, Paper, SegmentedControl, Select, Stack, Text, TextInput, Title } from '@mantine/core';
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { Link } from 'react-router';
import { ApiClient, ApiError } from '../api';
import { simulateTrafficFlow, validateProbe, type FlowStep, type FlowVerdict, type StepOutcome, type TrafficProbe, type TrafficProtocol } from '../trafficFlow';
import type { Applies, Lease6, LinkStatus, PrefixView, Session, Snapshot } from '../types';
import { useResource } from '../useResource';
import { Loaded } from './Loaded';
import { ZoneBadge, ZonePill } from './ZoneBadge';

interface TrafficFlowScreenProps {
  api: ApiClient;
  session: Session;
  onSessionLost(): void;
}

const mono = { input: 'mono-input' };

const protocols: Array<{ value: TrafficProtocol; label: string }> = [
  { value: 'tcp', label: 'TCP' },
  { value: 'udp', label: 'UDP' },
  { value: 'icmp', label: 'ICMP (IPv4)' },
  { value: 'icmpv6', label: 'ICMPv6 (IPv6)' },
];

const outcomeStyle: Record<StepOutcome, { color: string; label: string }> = {
  pass: { color: 'blue', label: 'Pass' },
  transform: { color: 'violet', label: 'NAT' },
  accept: { color: 'green', label: 'Allow' },
  drop: { color: 'red', label: 'Drop' },
  reject: { color: 'orange', label: 'Reject' },
  info: { color: 'gray', label: 'No match' },
  warn: { color: 'yellow', label: 'Warning' },
};

const verdictColor: Record<FlowVerdict, string> = {
  accept: 'green',
  drop: 'red',
  reject: 'orange',
  no_route: 'red',
};

interface LoadedContext {
  saved: Snapshot;
  liveVersion: string | null;
  hasUnapplied: boolean;
  liveSnapshot: Snapshot['value'] | null;
  liveLinks: LinkStatus[];
  liveLeases6: Lease6[] | null;
  livePrefixes: PrefixView | null;
  liveStatusVersionMismatch: boolean;
}

function optional<T>(promise: Promise<T>, fallback: T): Promise<T> {
  return promise.catch((error) => {
    if (error instanceof ApiError && error.status === 401) throw error;
    return fallback;
  });
}

function normalizeConfigValue(raw: Record<string, unknown>): Snapshot['value'] {
  const value = raw as Partial<Snapshot['value']>;
  return {
    ...value,
    links: Array.isArray(value.links) ? value.links : [],
    segments: Array.isArray(value.segments) ? value.segments : [],
    zones: Array.isArray(value.zones) ? value.zones : [],
  };
}

function parseOptionalPort(raw: string): number | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return undefined;
  if (!/^\d+$/.test(trimmed)) return Number.NaN;
  return Number(trimmed);
}

function FlowStepCard({ step, index }: { step: FlowStep; index: number }) {
  const { color, label } = outcomeStyle[step.outcome];
  return (
    <Paper
      withBorder
      radius="md"
      p="sm"
      className={`traffic-flow-node traffic-flow-node--${step.outcome}`}
    >
      <Stack gap={6}>
        <Group justify="space-between" gap="xs" wrap="wrap">
          <Group gap={6} wrap="nowrap">
            <Badge size="xs" circle color={color} variant="filled">{index + 1}</Badge>
            <Text size="sm" fw={600}>{step.title}</Text>
          </Group>
          <Group gap={4} wrap="nowrap">
            <Badge size="xs" color={color} variant="light">{label}</Badge>
            {step.isFirewall && <Badge size="xs" variant="filled" color="dark" tt="none">This firewall</Badge>}
            {step.zone && <ZoneBadge name={step.zone.name} color={step.zone.color} />}
          </Group>
        </Group>
        <Text size="xs">{step.summary}</Text>
        {step.detail && <Text size="xs" c="dimmed" ff="monospace" className="traffic-flow-detail">{step.detail}</Text>}
        {step.decidedBy && (
          <Text size="xs" c="dimmed">
            Decided by{' '}
            <Anchor component={Link} to={step.decidedBy.to} size="xs">{step.decidedBy.label}</Anchor>
          </Text>
        )}
      </Stack>
    </Paper>
  );
}

// Diagnostics traffic simulator: traces a hypothetical packet through NAT, routing, and firewall rules without sending traffic.
export function TrafficFlowScreen({ api, session, onSessionLost }: TrafficFlowScreenProps) {
  const canReadStatus = session.capabilities.includes('status.read');

  const load = useCallback(async (): Promise<LoadedContext> => {
    let saved!: Snapshot;
    let applies: Applies | null = null;
    let liveLinks: LinkStatus[] = [];
    let liveLeases6: Lease6[] | null = null;
    let livePrefixes: PrefixView | null = null;
    let liveStatusVersionMismatch = false;

    for (let attempt = 0; attempt < 2; attempt++) {
      [saved, applies, liveLinks, liveLeases6, livePrefixes] = await Promise.all([
        api.config(),
        optional(api.listApplies(), null),
        canReadStatus ? optional(api.links().then((res) => res.items), []) : Promise.resolve([]),
        canReadStatus ? optional(api.leases().then((res) => res.v6), null) : Promise.resolve(null),
        canReadStatus ? optional(api.prefixes(), null) : Promise.resolve(null),
      ]);
      if (!canReadStatus || !applies) break;
      const appliesAfter = await optional(api.listApplies(), null);
      if (!appliesAfter || appliesAfter.pointers.live === applies.pointers.live) break;
      if (attempt === 1) liveStatusVersionMismatch = true;
    }

    const liveApplyId = applies?.pointers.live;
    const liveVersion = liveApplyId
      ? (applies?.items.find((item) => item.id === liveApplyId)?.savedVersion ?? null)
      : null;
    const hasUnapplied = applies?.unapplied ?? false;

    const liveSnapshot = liveVersion && liveVersion !== saved.savedVersion && hasUnapplied
      ? await optional(api.version(liveVersion).then((detail) => normalizeConfigValue(detail.value)), null)
      : null;

    return { saved, liveVersion, hasUnapplied, liveSnapshot, liveLinks, liveLeases6, livePrefixes, liveStatusVersionMismatch };
  }, [api, canReadStatus]);

  const resource = useResource(load, onSessionLost);

  const [sourceMode, setSourceMode] = useState<'saved' | 'live'>('saved');
  const [ingressSegmentId, setIngressSegmentId] = useState('');
  const [protocol, setProtocol] = useState<TrafficProtocol>('tcp');
  const [srcIp, setSrcIp] = useState('');
  const [srcPort, setSrcPort] = useState('');
  const [dstIp, setDstIp] = useState('');
  const [dstPort, setDstPort] = useState('443');
  const [icmpType, setIcmpType] = useState('echo-request');
  const [errors, setErrors] = useState(new Map<string, string>());
  const [submittedProbe, setSubmittedProbe] = useState<TrafficProbe | null>(null);
  const [errorFocusTick, setErrorFocusTick] = useState(0);

  const formRef = useRef<HTMLFormElement>(null);
  useEffect(() => {
    if (errorFocusTick === 0) return;
    const invalid = formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]:not(:disabled)');
    if (invalid) {
      invalid.scrollIntoView?.({ block: 'nearest' });
      invalid.focus?.();
    }
  }, [errorFocusTick]);

  const data = resource.data;
  const canCompareLive = Boolean(data?.liveSnapshot);
  const effectiveSource = canCompareLive ? sourceMode : 'saved';
  const activeConfig = useMemo(
    () => (data ? (effectiveSource === 'live' && data.liveSnapshot ? data.liveSnapshot : data.saved.value) : null),
    [data, effectiveSource],
  );
  const activeVersionLabel = data
    ? (effectiveSource === 'live' && data.liveVersion ? `Live (v${data.liveVersion})` : `Saved (v${data.saved.savedVersion})`)
    : '';

  const { simulation, probeStaleInSource } = useMemo(() => {
    if (!submittedProbe || !activeConfig || !data) return { simulation: null, probeStaleInSource: false };
    if (validateProbe(submittedProbe, activeConfig).size > 0) {
      return { simulation: null, probeStaleInSource: true };
    }
    return {
      simulation: simulateTrafficFlow(submittedProbe, activeConfig, data.liveLinks, {
        evaluatingSavedDiffersFromLive: effectiveSource === 'saved' && canCompareLive,
        liveLeases6: data.liveLeases6 ?? undefined,
        livePrefixes: data.livePrefixes ?? undefined,
        liveStatusVersionMismatch: data.liveStatusVersionMismatch,
      }),
      probeStaleInSource: false,
    };
  }, [submittedProbe, activeConfig, data, effectiveSource, canCompareLive]);

  const isPortProtocol = protocol === 'tcp' || protocol === 'udp';

  return (
    <Stack>
      <Group justify="space-between" align="flex-end" wrap="wrap">
        <div>
          <Title order={2}>Traffic simulator</Title>
          <Text c="dimmed">
            Trace what Andaru would do with a hypothetical packet across NAT, routing, and firewall rules — evaluated directly from the configuration without sending traffic.
          </Text>
        </div>
        <Button size="xs" variant="default" loading={resource.loading} onClick={() => void resource.reload()}>Refresh</Button>
      </Group>

      <Loaded resource={resource} what="configuration">
        {({ saved, liveVersion, hasUnapplied }) => {
          if (!activeConfig) return null;
          const zonesById = new Map(activeConfig.zones.map((z) => [z.id, z]));
          const linksById = new Map(activeConfig.links.map((l) => [l.id, l]));

          const segmentOptions = activeConfig.segments.map((seg) => {
            const link = linksById.get(seg.link);
            const zone = zonesById.get(seg.zone);
            const suffix = [zone?.name, link && link.name !== seg.name ? link.name : undefined, link && !link.enabled ? 'disabled' : undefined]
              .filter(Boolean)
              .join(' · ');
            return {
              value: seg.id,
              label: suffix ? `${seg.name} (${suffix})` : seg.name,
            };
          });

          const selectedSegmentId = ingressSegmentId && activeConfig.segments.some((s) => s.id === ingressSegmentId)
            ? ingressSegmentId
            : (activeConfig.segments[0]?.id ?? '');

          function submit(event: FormEvent) {
            event.preventDefault();
            if (!activeConfig) return;
            const probe: TrafficProbe = {
              ingressSegmentId: selectedSegmentId,
              protocol,
              srcIp: srcIp.trim(),
              srcPort: isPortProtocol ? parseOptionalPort(srcPort) : undefined,
              dstIp: dstIp.trim(),
              dstPort: isPortProtocol ? parseOptionalPort(dstPort) : undefined,
              icmpType: !isPortProtocol ? icmpType.trim() : undefined,
            };
            const found = validateProbe(probe, activeConfig);
            setErrors(found);
            if (found.size > 0) {
              setSubmittedProbe(null);
              setErrorFocusTick((t) => t + 1);
              return;
            }
            setSubmittedProbe(probe);
          }

          const sourceSelector = canCompareLive ? (
            <SegmentedControl
              size="xs"
              aria-label="Configuration to evaluate"
              value={effectiveSource}
              onChange={(value) => setSourceMode(value === 'live' ? 'live' : 'saved')}
              data={[
                { value: 'saved', label: `Saved (v${saved.savedVersion})` },
                { value: 'live', label: `Live (v${liveVersion})` },
              ]}
            />
          ) : (
            <Badge size="sm" variant="light" color="blue">
              {liveVersion && !hasUnapplied ? `Saved & live (v${saved.savedVersion})` : activeVersionLabel}
            </Badge>
          );

          if (activeConfig.segments.length === 0) {
            return (
              <Stack>
                <Group justify="flex-end">{sourceSelector}</Group>
                <Alert role="status" title="No segments configured">
                  Configure at least one interface with a segment on <Anchor component={Link} to="/network/interfaces">Interfaces</Anchor> before simulating traffic.
                </Alert>
              </Stack>
            );
          }

          return (
            <Stack>
              <Card withBorder radius="md" p="md">
                <form ref={formRef} onSubmit={submit} noValidate>
                  <Stack gap="sm">
                    <Group justify="space-between" align="center" wrap="wrap">
                      <Title order={3} size="h5">Hypothetical packet</Title>
                      {sourceSelector}
                    </Group>

                    <div className="traffic-flow-grid">
                      <Select
                        label="Ingress interface"
                        required
                        allowDeselect={false}
                        data={segmentOptions}
                        value={selectedSegmentId || null}
                        error={errors.get('ingressSegmentId')}
                        renderOption={({ option }) => {
                          const seg = activeConfig.segments.find((s) => s.id === option.value);
                          const link = seg ? linksById.get(seg.link) : undefined;
                          const zone = seg ? zonesById.get(seg.zone) : undefined;
                          return (
                            <Group gap="xs" wrap="wrap">
                              {zone && <ZonePill name={zone.name} color={zone.color} />}
                              <Text size="sm">{seg?.name ?? option.label}</Text>
                              {link && link.name !== seg?.name && <Text size="xs" c="dimmed" ff="monospace">{link.name}</Text>}
                              {link && !link.enabled && <Badge size="xs" color="gray" variant="light">Disabled</Badge>}
                            </Group>
                          );
                        }}
                        onChange={(value) => setIngressSegmentId(value ?? '')}
                      />
                      <NativeSelect
                        label="Protocol"
                        data={protocols}
                        value={protocol}
                        error={errors.get('protocol')}
                        onChange={(event) => {
                          const next = event.currentTarget.value as TrafficProtocol;
                          setProtocol(next);
                          if ((next === 'icmp' || next === 'icmpv6') && !icmpType.trim()) setIcmpType('echo-request');
                        }}
                      />
                    </div>

                    <div className="traffic-flow-grid">
                      <TextInput
                        classNames={mono}
                        label="Source address"
                        placeholder={protocol === 'icmpv6' ? 'fd00:1::50' : '192.168.1.50'}
                        required
                        value={srcIp}
                        error={errors.get('srcIp')}
                        onChange={(event) => setSrcIp(event.currentTarget.value)}
                      />
                      {isPortProtocol ? (
                        <TextInput
                          classNames={mono}
                          label="Source port"
                          description="Optional (e.g. 68 for DHCPv4, 546 for DHCPv6)."
                          placeholder="54321"
                          inputMode="numeric"
                          value={srcPort}
                          error={errors.get('srcPort')}
                          onChange={(event) => setSrcPort(event.currentTarget.value)}
                        />
                      ) : (
                        <TextInput
                          classNames={mono}
                          label={protocol === 'icmpv6' ? 'ICMPv6 type' : 'ICMP type'}
                          description="Optional (e.g. echo-request, nd-neighbor-solicit)."
                          placeholder="echo-request"
                          value={icmpType}
                          onChange={(event) => setIcmpType(event.currentTarget.value)}
                        />
                      )}
                    </div>

                    <div className="traffic-flow-grid">
                      <TextInput
                        classNames={mono}
                        label="Destination address"
                        placeholder={protocol === 'icmpv6' ? '2606:4700:4700::1111' : '1.1.1.1'}
                        required
                        value={dstIp}
                        error={errors.get('dstIp')}
                        onChange={(event) => setDstIp(event.currentTarget.value)}
                      />
                      {isPortProtocol && (
                        <TextInput
                          classNames={mono}
                          label="Destination port"
                          placeholder="443"
                          inputMode="numeric"
                          required
                          value={dstPort}
                          error={errors.get('dstPort')}
                          onChange={(event) => setDstPort(event.currentTarget.value)}
                        />
                      )}
                    </div>

                    <Group justify="flex-end" gap="xs">
                      {submittedProbe && (
                        <Button
                          type="button"
                          size="sm"
                          variant="default"
                          onClick={() => {
                            setSubmittedProbe(null);
                            setErrors(new Map());
                          }}
                        >
                          Clear result
                        </Button>
                      )}
                      <Button type="submit" size="sm">Simulate traffic</Button>
                    </Group>
                  </Stack>
                </form>
              </Card>

              <div role="status" aria-live="polite">
                {probeStaleInSource && (
                  <Alert color="yellow" title="Ingress interface not present in selected configuration">
                    The simulated ingress interface does not exist in {activeVersionLabel}. Choose an interface above and click Simulate traffic.
                  </Alert>
                )}

                {simulation && (
                  <Stack gap="md">
                    <Paper withBorder radius="md" p="md">
                      <Group justify="space-between" align="flex-start" wrap="wrap" gap="sm">
                        <Stack gap={4}>
                          <Group gap="xs" wrap="wrap">
                            <Badge size="lg" color={verdictColor[simulation.verdict]} variant="filled">
                              {simulation.verdictLabel}
                            </Badge>
                            <Text size="sm" c="dimmed">Evaluated against {activeVersionLabel}</Text>
                          </Group>
                          <Text size="sm" ff="monospace">{simulation.originalTuple}</Text>
                          {simulation.finalTuple !== simulation.originalTuple && (
                            <Text size="sm" ff="monospace" c="dimmed">Final: {simulation.finalTuple}</Text>
                          )}
                        </Stack>
                        {simulation.decidedBy && (
                          <Text size="sm">
                            Decided by{' '}
                            <Anchor component={Link} to={simulation.decidedBy.to} fw={600}>{simulation.decidedBy.label}</Anchor>
                          </Text>
                        )}
                      </Group>
                    </Paper>

                    {simulation.warnings.length > 0 && (
                      <Alert color="yellow" title="Simulation notes">
                        <Stack gap={4}>
                          {simulation.warnings.map((warning) => (
                            <Text key={warning} size="sm">{warning}</Text>
                          ))}
                        </Stack>
                      </Alert>
                    )}

                    <Card withBorder radius="md" p="md" component="section" aria-labelledby="traffic-flow-diagram-heading">
                      <Title id="traffic-flow-diagram-heading" order={3} size="h5" mb="sm">Flow diagram</Title>
                      <ol className="traffic-flow-diagram">
                        {simulation.steps.map((step, index) => (
                          <li key={step.stage} className="traffic-flow-item">
                            <FlowStepCard step={step} index={index} />
                            {index < simulation.steps.length - 1 && (
                              <span className="traffic-flow-arrow" aria-hidden="true">→</span>
                            )}
                          </li>
                        ))}
                      </ol>
                    </Card>

                    <Card withBorder radius="md" p="md" component="section" aria-labelledby="traffic-flow-trace-heading">
                      <Title id="traffic-flow-trace-heading" order={3} size="h5" mb="xs">Step-by-step trace</Title>
                      <ol className="traffic-flow-list">
                        {simulation.steps.map((step) => (
                          <li key={step.stage}>
                            <strong>{step.title}:</strong> {step.summary}
                            {step.detail && <> (<Text span ff="monospace" size="xs" inherit>{step.detail}</Text>)</>}
                            {step.decidedBy && (
                              <> — <Anchor component={Link} to={step.decidedBy.to} size="sm">{step.decidedBy.label}</Anchor></>
                            )}
                          </li>
                        ))}
                      </ol>
                    </Card>
                  </Stack>
                )}
              </div>
            </Stack>
          );
        }}
      </Loaded>
    </Stack>
  );
}
