// SPDX-License-Identifier: AGPL-3.0-or-later
import {
  Anchor,
  Badge,
  Card,
  Group,
  SegmentedControl,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { useMemo, useState } from 'react';
import { Link } from 'react-router';
import {
  type Client,
  type Device,
  type VLANFlow,
  type VLANFlowVLAN,
} from '../api';
import { VlanChip } from '../components/Badges';
import { devicePath, vlanFlowPath } from '../routing';
import {
  type ColorPair,
  deviceName,
  getClientVlanIssue,
  vlanLabel,
} from '../vlanUtils';

interface Props {
  clients: Client[];
  flow: VLANFlow | null;
  devicesById: Map<string, Device>;
  vlansById: Map<number, VLANFlowVLAN>;
  vlanColors: Map<number, ColorPair>;
}

export function ClientsScreen({ clients, flow, devicesById, vlansById, vlanColors }: Props) {
  const [query, setQuery] = useState('');
  const [connType, setConnType] = useState<string>('all');
  const [vlanFilter, setVlanFilter] = useState<string>('all');
  const [onlineOnly, setOnlineOnly] = useState(false);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return clients.filter((c) => {
      if (onlineOnly && !c.online) return false;
      if (connType !== 'all' && c.connection_type !== connType) return false;
      if (vlanFilter !== 'all' && String(c.vlan ?? '') !== vlanFilter) return false;
      if (!q) return true;
      const dev = c.attached_device_id ? deviceName(devicesById, c.attached_device_id) : '';
      return [c.hostname ?? '', c.mac, c.ips.join(' '), c.ssid ?? '', c.vendor_oui ?? '', dev]
        .join(' ')
        .toLowerCase()
        .includes(q);
    });
  }, [clients, onlineOnly, connType, vlanFilter, query, devicesById]);

  return (
    <Stack gap="md">
      <Group justify="space-between" align="flex-end" wrap="wrap" gap="sm">
        <div>
          <Title order={2} size="h3">Clients</Title>
          <Text size="sm" c="dimmed">
            Observed wired and wireless endpoints correlated from FDB, ARP/NDP, DHCP leases, and AP associations.
          </Text>
        </div>
        <Group gap="xs" wrap="wrap">
          <TextInput
            size="xs"
            w={{ base: '100%', sm: 220 }}
            placeholder="Search hostname, IP, MAC, SSID..."
            value={query}
            onChange={(e) => setQuery(e.currentTarget.value)}
            aria-label="Search clients"
          />
          <Select
            size="xs"
            w={165}
            aria-label="Filter by VLAN"
            value={vlanFilter}
            onChange={(v) => setVlanFilter(v ?? 'all')}
            data={[
              { value: 'all', label: 'All VLANs' },
              ...(flow?.vlans ?? []).map((v) => ({
                value: String(v.id),
                label: vlanLabel(vlansById, v.id, true),
              })),
            ]}
          />
          <SegmentedControl
            size="xs"
            aria-label="Filter by connection type"
            value={connType}
            onChange={setConnType}
            data={[
              { label: 'All', value: 'all' },
              { label: 'Wired', value: 'wired' },
              { label: 'Wi-Fi', value: 'wireless' },
            ]}
          />
          <Switch
            size="xs"
            label="Online only"
            checked={onlineOnly}
            onChange={(e) => setOnlineOnly(e.currentTarget.checked)}
          />
        </Group>
      </Group>

      <Card withBorder radius="md" p={0}>
        <Table.ScrollContainer minWidth={0} type="native">
          <Table striped highlightOnHover className="data-table">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Hostname</Table.Th>
                <Table.Th>MAC / Vendor</Table.Th>
                <Table.Th>IP Addresses</Table.Th>
                <Table.Th>VLAN</Table.Th>
                <Table.Th>Attachment</Table.Th>
                <Table.Th>Signal / Link</Table.Th>
                <Table.Th>Status</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {filtered.map((c) => {
                const issue = getClientVlanIssue(c, flow);
                const sigParts: string[] = [];
                if (c.signal_dbm != null) sigParts.push(`${c.signal_dbm} dBm`);
                if (c.tx_rate_mbps != null) sigParts.push(`${c.tx_rate_mbps}M`);

                return (
                  <Table.Tr key={c.mac}>
                    <Table.Td fw={600}>{c.hostname || '—'}</Table.Td>
                    <Table.Td>
                      <Text size="xs" ff="monospace">{c.mac}</Text>
                      {c.vendor_oui && <Text size="xs" c="dimmed">{c.vendor_oui}</Text>}
                    </Table.Td>
                    <Table.Td ff="monospace" fz="xs">
                      {c.ips.length > 0 ? (
                        <Stack gap={2}>
                          {c.ips.map((ip) => <span key={ip}>{ip}</span>)}
                        </Stack>
                      ) : (
                        <Text size="xs" c="dimmed">—</Text>
                      )}
                    </Table.Td>
                    <Table.Td>
                      <VlanChip vid={c.vlan} vlansById={vlansById} colorMap={vlanColors} />
                    </Table.Td>
                    <Table.Td>
                      {c.attached_device_id ? (
                        <Group gap={4} wrap="wrap">
                          <Anchor
                            component={Link}
                            to={devicePath(c.attached_device_id, c.attached_link)}
                            size="sm"
                            fw={600}
                          >
                            {deviceName(devicesById, c.attached_device_id)}
                          </Anchor>
                          {(c.ssid || c.attached_link) && (
                            <Text span size="xs" c="dimmed" ff="monospace">
                              ({c.ssid || c.attached_link})
                            </Text>
                          )}
                        </Group>
                      ) : (
                        <Text size="xs" c="dimmed">—</Text>
                      )}
                    </Table.Td>
                    <Table.Td ff="monospace" fz="xs">
                      {c.connection_type === 'wireless'
                        ? sigParts.length > 0
                          ? sigParts.join(' · ')
                          : 'Wi-Fi'
                        : 'Wired'}
                    </Table.Td>
                    <Table.Td>
                      {issue ? (
                        <Badge
                          component={Link}
                          to={vlanFlowPath({ mode: 'single', vlanId: issue.vlanId })}
                          size="xs"
                          variant="light"
                          color="yellow"
                          style={{ cursor: 'pointer' }}
                        >
                          ⊘ VLAN {issue.vlanId} Unreachable
                        </Badge>
                      ) : (
                        <Badge size="xs" variant="dot" color={c.online ? 'green' : 'gray'}>
                          {c.online ? 'Online' : 'Offline'}
                        </Badge>
                      )}
                    </Table.Td>
                  </Table.Tr>
                );
              })}
              {filtered.length === 0 && (
                <Table.Tr>
                  <Table.Td colSpan={7}>
                    <Text size="sm" c="dimmed">
                      {clients.length === 0 ? 'No clients observed yet.' : 'No clients match this filter.'}
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
