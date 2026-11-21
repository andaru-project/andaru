import { Group, Progress, Stack, Text } from '@mantine/core';
import { useCallback, useRef } from 'react';
import type { ApiClient } from '../api';
import { bytes } from '../format';
import type { Usage } from '../types';
import { cpuShare } from '../usage';
import { usePolledResource } from '../useResource';
import { Loaded } from './Loaded';

const pollSeconds = 5;

// A measure is a label, its figures, and a bar: the bar is never the only carrier of the value. A share that
// is not known shows no bar value and says so.
function Measure({ label, share, figures }: { label: string; share: number | null; figures: string }) {
  const filled = share === null ? 0 : Math.round(share * 100);
  return (
    <div>
      <Group justify="space-between" gap="xs" wrap="nowrap">
        <Text size="sm" c="dimmed">{label}</Text>
        <Text size="sm" ta="right">{figures}</Text>
      </Group>
      <Progress value={filled} color={filled >= 90 ? 'red' : filled >= 75 ? 'orange' : undefined} aria-label={share === null ? `${label}: not known yet` : label} />
    </div>
  );
}

// part is used over total as a share from 0 to 1, and null when there is no total to speak of.
const part = (used: number, total: number) => (total > 0 ? Math.min(Math.max(used / total, 0), 1) : null);
const percent = (share: number | null) => (share === null ? '—' : `${Math.round(share * 100)}%`);

// Room: what is used of what there is, and the share.
function Room({ label, availableBytes, totalBytes }: { label: string; availableBytes: number; totalBytes: number }) {
  const share = part(totalBytes - availableBytes, totalBytes);
  return <Measure label={label} share={share} figures={`${bytes(Math.max(totalBytes - availableBytes, 0))} / ${bytes(totalBytes)} · ${percent(share)}`} />;
}

export function ResourcesView({ usage, before }: { usage: Usage; before?: Usage }) {
  const { memory, connections } = usage;
  // The processors' share needs two readings a poll or so apart: the first shows none.
  const cpu = before ? cpuShare(before, usage, pollSeconds * 4) : null;
  return (
    <Stack gap="xs">
      <Measure label="CPU" share={cpu} figures={percent(cpu)} />
      {usage.load && <Group justify="space-between" gap="xs"><Text size="sm" c="dimmed">Load 1 · 5 · 15 min</Text><Text size="sm">{usage.load.map((value) => value.toFixed(2)).join(' · ')}</Text></Group>}
      {memory.totalBytes > 0 && <Room label="Memory" availableBytes={memory.availableBytes} totalBytes={memory.totalBytes} />}
      {memory.swapTotalBytes > 0 && <Room label="Swap" availableBytes={memory.swapFreeBytes} totalBytes={memory.swapTotalBytes} />}
      {usage.fileSystems.map((fileSystem, index) => <Room key={`${fileSystem.mountPoint} ${index}`} label={`Disk ${fileSystem.mountPoint}`} availableBytes={fileSystem.availableBytes} totalBytes={fileSystem.totalBytes} />)}
      {connections && <Measure label="Connections" share={part(connections.count, connections.limit)} figures={`${connections.count.toLocaleString('en')} / ${connections.limit.toLocaleString('en')} · ${percent(part(connections.count, connections.limit))}`} />}
      {usage.temperatures.map((temperature, index) => (
        <Group key={`${temperature.sensor} ${index}`} justify="space-between" gap="xs" wrap="nowrap">
          <Text size="sm" c="dimmed">{temperature.sensor || 'Sensor'}</Text>
          <Text size="sm">{temperature.celsius.toFixed(1)} °C</Text>
        </Group>
      ))}
    </Stack>
  );
}

export function ResourcesWidget({ api, onSessionLost }: { api: ApiClient; onSessionLost(): void }) {
  // The reading before the one shown, for the processors' share.
  const last = useRef<Usage>(undefined);
  const resource = usePolledResource(useCallback(async () => {
    const usage = await api.usage();
    const before = last.current;
    last.current = usage;
    return { usage, before };
  }, [api]), onSessionLost, pollSeconds * 1000);
  return <Loaded resource={resource} what="resource use">{({ usage, before }) => <ResourcesView usage={usage} before={before} />}</Loaded>;
}
