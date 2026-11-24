// SPDX-License-Identifier: AGPL-3.0-or-later
import {
  Alert,
  Badge,
  Button,
  Card,
  Code,
  Group,
  NumberInput,
  Paper,
  Select,
  Stack,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { useRef, useState } from 'react';
import {
  api,
  type Device,
  type DiagnosticResult,
  type NetplaneEvent,
} from '../api';

interface Props {
  devices: Device[];
  diagRuns: DiagnosticResult[];
  events: NetplaneEvent[];
  sseConnected: boolean;
  canRunDiagnostics?: boolean;
  onDiagnosticCompleted: (res: DiagnosticResult) => void;
}

export function DiagnosticsScreen({
  devices,
  diagRuns,
  events,
  sseConnected,
  canRunDiagnostics = true,
  onDiagnosticCompleted,
}: Props) {
  const [diagKind, setDiagKind] = useState<string>('fdb_lookup');
  const [diagDeviceId, setDiagDeviceId] = useState<string>('');
  const [diagTarget, setDiagTarget] = useState('');
  const [pingCount, setPingCount] = useState<number | string>(3);
  const [diagBusy, setDiagBusy] = useState(false);
  const busyRef = useRef(false);
  const [diagError, setDiagError] = useState<string | null>(null);

  const capableDevices = devices.filter((d) => d.capabilities.includes(`cap:diag_${diagKind}`));

  // Enter in the target field and the Run button both go through runDiag, so it owns the checks.
  const canRun = canRunDiagnostics && diagTarget.trim() !== '' && (diagKind === 'fdb_lookup' || diagDeviceId !== '');

  const runDiag = async () => {
    if (!canRun || busyRef.current) return;
    busyRef.current = true;
    setDiagBusy(true);
    setDiagError(null);
    try {
      const params: Record<string, string> | undefined =
        diagKind === 'ping' && typeof pingCount === 'number' && pingCount >= 1 && pingCount <= 10
          ? { count: String(pingCount) }
          : undefined;
      const res = await api.runDiagnostic({
        kind: diagKind,
        device_id: diagKind === 'fdb_lookup' ? undefined : diagDeviceId || undefined,
        target: diagTarget.trim(),
        params,
      });
      onDiagnosticCompleted(res);
    } catch (e) {
      setDiagError(e instanceof Error ? e.message : String(e));
    } finally {
      busyRef.current = false;
      setDiagBusy(false);
    }
  };

  return (
    <Stack gap="md">
      <div>
        <Title order={2} size="h3">Diagnostics & Events</Title>
        <Text size="sm" c="dimmed">
          Cross-vendor FDB/MAC/IP lookup, device-side ping and traceroute, and live Server-Sent Events stream.
        </Text>
      </div>

      <div className="np-grid-2-md">
        <Card withBorder radius="md" p="md">
          <Title order={4} size="h5" mb="sm">Run Diagnostic</Title>
          <Stack gap="xs">
            {diagError && <Alert color="red" title="Diagnostic failed">{diagError}</Alert>}
            <Select
              label="Type"
              value={diagKind}
              onChange={(v) => {
                const kind = v ?? 'fdb_lookup';
                setDiagKind(kind);
                if (!devices.find((d) => d.id === diagDeviceId)?.capabilities.includes(`cap:diag_${kind}`)) {
                  setDiagDeviceId('');
                }
              }}
              data={[
                { value: 'fdb_lookup', label: 'FDB / MAC / IP Lookup (Cross-Vendor)' },
                { value: 'ping', label: 'Device Ping' },
                { value: 'traceroute', label: 'Device Traceroute' },
              ]}
            />
            {diagKind !== 'fdb_lookup' && (
              <Select
                label="Device"
                placeholder={capableDevices.length > 0 ? 'Select device' : `No devices support ${diagKind}`}
                value={diagDeviceId || null}
                onChange={(v) => setDiagDeviceId(v ?? '')}
                data={capableDevices.map((d) => ({ value: d.id, label: `${d.name} (${d.driver})` }))}
              />
            )}
            <TextInput
              label={diagKind === 'fdb_lookup' ? 'Target (MAC, IP, or hostname)' : 'Target (IP or hostname)'}
              value={diagTarget}
              onChange={(e) => setDiagTarget(e.currentTarget.value)}
              placeholder="e.g. de:ad:be:ef:00:01 or 192.0.2.55"
              onKeyDown={(e) => {
                if (e.key === 'Enter') void runDiag();
              }}
            />
            {diagKind === 'ping' && (
              <NumberInput
                label="Probe count"
                min={1}
                max={10}
                value={pingCount}
                onChange={setPingCount}
              />
            )}
            <Button
              loading={diagBusy}
              disabled={!canRun}
              onClick={() => void runDiag()}
            >
              Run
            </Button>
          </Stack>

          <Title order={5} size="sm" mt="lg" mb="xs">Recent Runs ({diagRuns.length})</Title>
          <Stack gap="xs">
            {diagRuns.map((r) => (
              <Paper key={r.id} withBorder p="xs" radius="sm">
                <Group justify="space-between" wrap="wrap" gap="xs">
                  <Text size="xs" fw={700} ff="monospace">
                    {r.kind} · {r.target}
                    {r.device_id ? ` @ ${r.device_id}` : ''}
                  </Text>
                  <Group gap={6}>
                    <Text size="xs" c="dimmed" ff="monospace">{r.duration_ms} ms</Text>
                    <Badge size="xs" color={r.ok ? 'green' : 'red'}>{r.ok ? 'OK' : 'FAIL'}</Badge>
                  </Group>
                </Group>
                <Text size="xs" mt={4}>{r.summary}</Text>
                {r.error && <Text size="xs" c="red" mt={2}>{r.error}</Text>}
                {r.raw_output && <Code block mt={6} fz="xs">{r.raw_output}</Code>}
              </Paper>
            ))}
            {diagRuns.length === 0 && (
              <Text size="xs" c="dimmed">No diagnostic runs recorded yet.</Text>
            )}
          </Stack>
        </Card>

        <Card withBorder radius="md" p="md">
          <Group justify="space-between" mb="sm">
            <Title order={4} size="h5">Live Event Stream (SSE)</Title>
            <Badge size="xs" variant="dot" color={sseConnected ? 'green' : 'gray'}>
              {sseConnected ? 'Connected' : 'Connecting'}
            </Badge>
          </Group>
          <Stack gap="xs">
            {events.map((ev) => (
              <Paper key={ev.id} withBorder p="xs" radius="sm">
                <Group justify="space-between" wrap="wrap" gap="xs">
                  <Badge
                    size="xs"
                    variant="light"
                    ff="monospace"
                    color={ev.severity === 'crit' ? 'red' : ev.severity === 'warn' ? 'yellow' : 'blue'}
                  >
                    {ev.type}
                  </Badge>
                  <Text size="xs" c="dimmed" ff="monospace">
                    {new Date(ev.timestamp).toLocaleTimeString()}
                  </Text>
                </Group>
                <Text size="xs" mt={4}>{ev.summary}</Text>
              </Paper>
            ))}
            {events.length === 0 && (
              <Text c="dimmed" size="sm">Waiting for live network transitions…</Text>
            )}
          </Stack>
        </Card>
      </div>
    </Stack>
  );
}
