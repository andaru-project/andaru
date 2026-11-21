import { Progress, Stack, Text } from '@mantine/core';
import { useCallback, useRef } from 'react';
import type { ApiClient } from '../api';
import { bytes } from '../format';
import { cpuShare } from '../traffic';
import type { Usage } from '../types';
import { usePolledResource } from '../useResource';
import { Loaded } from './Loaded';

const pollMilliseconds = 5_000;

// A measure is a bar with its number beside it in words: the bar is never the only carrier of the value.
function Measure({ label, share, children }: { label: string; share: number | null; children: string }) {
  const percent = share === null ? 0 : Math.round(share * 100);
  return (
    <div>
      <Text size="sm"><Text span c="dimmed" inherit>{label}</Text> {children}</Text>
      <Progress value={percent} color={percent >= 90 ? 'red' : percent >= 75 ? 'yellow' : undefined} aria-label={`${label}: ${share === null ? 'not known yet' : `${percent}% used`}`} />
    </div>
  );
}

const part = (used: number, total: number) => (total > 0 ? Math.min(used / total, 1) : null);
const percent = (share: number | null) => (share === null ? '—' : `${Math.round(share * 100)}%`);

export function ResourcesView({ usage, before }: { usage: Usage; before?: Usage }) {
  const { memory, connections } = usage;
  // The processors' share needs two readings: the first poll shows none.
  const cpu = before ? cpuShare(before.cpu, usage.cpu) : null;
  const memoryShare = part(memory.totalBytes - memory.availableBytes, memory.totalBytes);
  const swapShare = part(memory.swapTotalBytes - memory.swapFreeBytes, memory.swapTotalBytes);
  return (
    <Stack gap="xs">
      <Measure label="CPU" share={cpu}>{`${percent(cpu)}${usage.load ? `, load ${usage.load.map((value) => value.toFixed(2)).join(' ')}` : ''}`}</Measure>
      {memory.totalBytes > 0 && <Measure label="Memory" share={memoryShare}>{`${percent(memoryShare)}: ${bytes(memory.availableBytes)} free of ${bytes(memory.totalBytes)}`}</Measure>}
      {memory.swapTotalBytes > 0 && <Measure label="Swap" share={swapShare}>{`${percent(swapShare)}: ${bytes(memory.swapFreeBytes)} free of ${bytes(memory.swapTotalBytes)}`}</Measure>}
      {usage.fileSystems.map((fileSystem) => {
        const share = part(fileSystem.totalBytes - fileSystem.availableBytes, fileSystem.totalBytes);
        return <Measure key={fileSystem.mountPoint} label={`Disk ${fileSystem.mountPoint}`} share={share}>{`${percent(share)}: ${bytes(fileSystem.availableBytes)} free of ${bytes(fileSystem.totalBytes)}`}</Measure>;
      })}
      {connections && <Measure label="Connections" share={part(connections.count, connections.limit)}>{`${connections.count.toLocaleString('en')} of ${connections.limit.toLocaleString('en')} tracked`}</Measure>}
      {usage.temperatures.length > 0 && (
        <ul className="plain-list tight">
          {usage.temperatures.map((temperature, index) => <li key={`${temperature.sensor} ${index}`}><Text size="sm"><Text span c="dimmed" inherit>{temperature.sensor || 'Sensor'}</Text> {temperature.celsius.toFixed(1)} °C</Text></li>)}
        </ul>
      )}
    </Stack>
  );
}

export function ResourcesWidget({ api, onSessionLost }: { api: ApiClient; onSessionLost(): void }) {
  // The reading before the one shown, for the processors' share.
  const readings = useRef<{ before?: Usage; latest?: Usage }>({});
  const resource = usePolledResource(useCallback(async () => {
    const usage = await api.usage();
    readings.current = { before: readings.current.latest, latest: usage };
    return readings.current;
  }, [api]), onSessionLost, pollMilliseconds);
  return <Loaded resource={resource} what="resource use">{({ latest, before }) => latest && <ResourcesView usage={latest} before={before} />}</Loaded>;
}
