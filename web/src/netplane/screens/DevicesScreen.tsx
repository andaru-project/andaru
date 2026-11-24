// SPDX-License-Identifier: AGPL-3.0-or-later
import {
  Anchor,
  Badge,
  Card,
  Group,
  Stack,
  Table,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import {
  formatUptime,
  type NetworkState,
  type VLANFlow,
} from '../api';
import { DriverBadge } from '../components/Badges';
import { devicePath } from '../routing';
import { findingsForDevice, getSfpAlerts } from '../vlanUtils';

interface Props {
  state: NetworkState | null;
  flow: VLANFlow | null;
}

export function DevicesScreen({ state, flow }: Props) {
  const navigate = useNavigate();
  const [query, setQuery] = useState('');

  const devices = useMemo(() => state?.devices ?? [], [state?.devices]);
  const links = state?.links ?? [];
  const findings = flow?.findings ?? [];

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return devices;
    return devices.filter((d) =>
      [d.name, d.driver, d.role, d.model ?? '', d.vendor ?? '', d.mgmt_ip ?? '', d.id]
        .join(' ')
        .toLowerCase()
        .includes(q),
    );
  }, [devices, query]);

  return (
    <Stack gap="md">
      <Group justify="space-between" align="flex-end" wrap="wrap" gap="sm">
        <div>
          <Title order={2} size="h3">Devices</Title>
          <Text size="sm" c="dimmed">
            Managed firewalls, switches, and access points across drivers. Select any device to inspect its port faceplate.
          </Text>
        </div>
        <TextInput
          size="xs"
          className="np-filter-input"
          placeholder="Filter devices, driver, model, IP..."
          value={query}
          onChange={(e) => setQuery(e.currentTarget.value)}
          aria-label="Filter devices"
        />
      </Group>

      <Card withBorder radius="md" p={0}>
        <Table.ScrollContainer minWidth={0} type="native">
          <Table striped highlightOnHover className="np-data-table">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Name</Table.Th>
                <Table.Th>Driver</Table.Th>
                <Table.Th>Role</Table.Th>
                <Table.Th>Model</Table.Th>
                <Table.Th>Mgmt IP</Table.Th>
                <Table.Th>Ports</Table.Th>
                <Table.Th>Firmware</Table.Th>
                <Table.Th>Uptime</Table.Th>
                <Table.Th>Findings</Table.Th>
                <Table.Th>Status</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {filtered.map((d) => {
                const devLinks = links.filter((l) => l.device_id === d.id);
                const up = devLinks.filter((l) => l.link_up).length;
                const devFindings = findingsForDevice(findings, d.id);
                const sfpAlerts = getSfpAlerts(devLinks);
                const hasUpdate = Boolean(d.firmware.available && d.firmware.available !== d.firmware.current);

                return (
                  <Table.Tr
                    key={d.id}
                    style={{ cursor: 'pointer' }}
                    onClick={() => void navigate(devicePath(d.id))}
                  >
                    <Table.Td fw={700}>
                      <Anchor component={Link} to={devicePath(d.id)} inherit c="inherit" onClick={(e) => e.stopPropagation()}>
                        {d.name}
                      </Anchor>
                    </Table.Td>
                    <Table.Td><DriverBadge driver={d.driver} /></Table.Td>
                    <Table.Td>{d.role}</Table.Td>
                    <Table.Td fz="xs">{d.model || '—'}</Table.Td>
                    <Table.Td ff="monospace" fz="xs">{d.mgmt_ip || '—'}</Table.Td>
                    <Table.Td ff="monospace" fz="xs">
                      {devLinks.length > 0 ? `${up} / ${devLinks.length}` : '—'}
                    </Table.Td>
                    <Table.Td ff="monospace" fz="xs">
                      {d.firmware.current || '—'}
                      {hasUpdate && (
                        <Badge size="xs" variant="light" color="blue" ml={6}>Update</Badge>
                      )}
                    </Table.Td>
                    <Table.Td ff="monospace" fz="xs">{formatUptime(d.uptime_sec)}</Table.Td>
                    <Table.Td>
                      <Group gap={4} wrap="wrap">
                        {devFindings.some((f) => f.kind === 'leak') && (
                          <Badge size="xs" variant="light" color="red">⚠ Leak</Badge>
                        )}
                        {devFindings.some((f) => f.kind === 'dead_end') && (
                          <Badge size="xs" variant="light" color="yellow">⊘ Dead-end</Badge>
                        )}
                        {devFindings.some((f) => f.kind === 'tag_mismatch') && (
                          <Badge size="xs" variant="light" color="orange">⇄ Mismatch</Badge>
                        )}
                        {sfpAlerts.length > 0 && (
                          <Badge size="xs" variant="light" color="yellow">DOM</Badge>
                        )}
                        {devFindings.length === 0 && sfpAlerts.length === 0 && (
                          <Text size="xs" c="dimmed">None</Text>
                        )}
                      </Group>
                    </Table.Td>
                    <Table.Td>
                      <Badge size="xs" variant="dot" color={d.online ? 'green' : 'red'}>
                        {d.online ? 'Online' : 'Offline'}
                      </Badge>
                    </Table.Td>
                  </Table.Tr>
                );
              })}
              {filtered.length === 0 && (
                <Table.Tr>
                  <Table.Td colSpan={10}>
                    <Text size="sm" c="dimmed">
                      {devices.length === 0 ? 'No devices polled yet.' : 'No devices match this filter.'}
                    </Text>
                  </Table.Td>
                </Table.Tr>
              )}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Card>
    </Stack>
  );
}
