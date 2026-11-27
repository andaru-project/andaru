import { Alert, Badge, Button, Group, Modal, Paper, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useCallback, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { Link } from 'react-router';
import { ApiClient, apiErrorMessage, reportError } from '../api';
import { bytes, duration, firmwareDate, plural } from '../format';
import type { SafeModeReason, SafeModeStatus, SystemInfo, UpdateStatus } from '../types';
import { useVisiblePoll, type Resource } from '../useResource';
import { Loaded } from './Loaded';

const hostnamePattern = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
const hostnameError = 'Use 1 to 63 lowercase letters, digits, or hyphens; cannot start or end with a hyphen.';

type System = Resource<{ info: SystemInfo; loadedAt: number } | undefined>;

function Row({ label, children }: { label: string; children: ReactNode }) {
  return <Table.Tr><Table.Th scope="row">{label}</Table.Th><Table.Td>{children}</Table.Td></Table.Tr>;
}

function Facts({ children }: { children: ReactNode }) {
  return <Table className="facts" withRowBorders={false} verticalSpacing={2} horizontalSpacing={0}><Table.Tbody>{children}</Table.Tbody></Table>;
}

const list = (names: string[]) => (names.length > 0 ? names.join(', ') : 'none');
const words = (...parts: string[]) => parts.filter(Boolean).join(' ');

// loadedAt is the browser's clock when the description arrived. The box's clock may differ from the browser's, so
// the box's time and uptime are its own at that moment plus the browser's time since: they move on a minute at a
// time, and when the tab is looked at again, without asking the box.
function useBoxClock(info: SystemInfo, loadedAt: number) {
  const [now, setNow] = useState(Date.now);
  useVisiblePoll(useCallback(() => setNow(Date.now()), []), 60_000);
  const clock = Date.parse(info.time) + Math.max(now - loadedAt, 0);
  const uptime = info.bootedAt ? (clock - Date.parse(info.bootedAt)) / 1000 : NaN;
  return (
    <>
      {/* The box's own clock, in UTC so it reads the same from anywhere: a firewall with a wrong clock misjudges certificates and logs. */}
      {Number.isFinite(clock) && <Row label="Clock">{new Date(clock).toISOString().slice(0, 16).replace('T', ' ')} UTC</Row>}
      {Number.isFinite(uptime) && uptime >= 0 && <Row label="Uptime">{duration(uptime)}</Row>}
    </>
  );
}

export interface UpdateNotice {
  color: string;
  variant: 'filled' | 'light';
  pillLabel: string;
  navLabel?: string;
}

// eslint-disable-next-line react-refresh/only-export-components -- shared helper for SystemWidget and Shell
export function updateNotice(update?: UpdateStatus): UpdateNotice | null {
  if (!update || !update.supported) return null;
  if (update.rebootNeeded) {
    const version = update.staged?.version || (update.rollbackQueued ? update.rollback?.version : undefined) || 'update';
    return { color: 'teal', variant: 'filled', pillLabel: `Reboot to apply ${version}`, navLabel: 'Reboot' };
  }
  if (update.state === 'available') {
    return {
      color: 'orange',
      variant: 'filled',
      pillLabel: `Update available: ${update.available?.version || 'new version'}`,
      navLabel: 'Update',
    };
  }
  if (update.state === 'up_to_date') {
    return { color: 'green', variant: 'light', pillLabel: 'Up to date' };
  }
  if (update.state === 'error') {
    return { color: 'red', variant: 'light', pillLabel: 'Update check failed' };
  }
  return null;
}

const safeModeReasonLabel: Record<SafeModeReason, string> = {
  boot: 'one-boot safe mode',
  kernelArg: 'kernel argument (andaru.safe=1)',
  settingsHold: 'pending settings upgrade decisions',
};

// eslint-disable-next-line react-refresh/only-export-components -- shared helper for SystemWidget and Shell
export function formatSafeModeReasons(safeMode: SafeModeStatus): string {
  return safeMode.reasons.map((r) => safeModeReasonLabel[r] ?? r).join(', ');
}

function UpdatePill({ update }: { update?: UpdateStatus }) {
  const notice = updateNotice(update);
  if (!notice) return null;
  return (
    <Badge component={Link} to="/system/updates" size="xs" variant={notice.variant} color={notice.color} className="update-pill plain-badge">
      {notice.pillLabel}
    </Badge>
  );
}

// The dashboard's widget: what changes or says something is wrong, and a line each for the machine and its
// processor. The header names the box; the rest of what it is made of is looked up rarely, and is on the
// System details page.
export function SystemWidget({ info, loadedAt, update }: { info: SystemInfo; loadedAt: number; update?: UpdateStatus }) {
  const running = useBoxClock(info, loadedAt);
  // A guest that names no machine is still said to be one.
  const machine = words(info.hardware.vendor, info.hardware.product) || (info.guest ? 'virtual machine' : '');
  return (
    <Facts>
      <Row label="Version">
        <Group gap="xs" wrap="wrap">
          <span>{info.version}</span>
          {info.safeMode?.active && <Badge size="xs" variant="filled" color="orange">Safe mode</Badge>}
          <UpdatePill update={update} />
        </Group>
      </Row>
      {running}
      {machine && <Row label="Machine">{machine}</Row>}
      <Row label="CPU">{info.cpu.model || info.architecture}, {plural(info.cpu.cores, 'core')}</Row>
    </Facts>
  );
}

function Card({ title, children }: { title: string; children: ReactNode }) {
  const id = `system-${title.toLowerCase()}`;
  return <Paper component="section" withBorder p="md" aria-labelledby={id}><Title order={3} size="h5" mb="sm" id={id}>{title}</Title><Facts>{children}</Facts></Paper>;
}

// Everything the box says of itself, in three cards.
export function SystemFacts({ info, loadedAt }: { info: SystemInfo; loadedAt: number }) {
  const running = useBoxClock(info, loadedAt);
  const { cpu, hardware } = info;
  // Good news is only what the box calls so: a state this version does not know counts as unknown.
  const named = (states: string[]) => cpu.vulnerabilities.filter((item) => states.includes(item.state)).map((item) => item.name).join(', ');
  const vulnerable = named(['vulnerable']);
  const unknown = cpu.vulnerabilities.filter((item) => !['vulnerable', 'mitigated', 'not_affected'].includes(item.state)).map((item) => item.name).join(', ');
  const machine = words(hardware.vendor, hardware.product);
  const board = words(hardware.boardVendor, hardware.board);
  const firmware = words(hardware.biosVendor, hardware.biosVersion, firmwareDate(hardware.biosDate));
  return (
    <Stack className="details">
      <Card title="Software">
        <Row label="Hostname">{info.hostname || '—'}</Row>
        <Row label="Version">{info.version}</Row>
        {info.safeMode?.active && (() => {
          const reasons = formatSafeModeReasons(info.safeMode);
          return <Row label="Safe mode">Active{reasons ? ` (${reasons})` : ''}</Row>;
        })()}
        <Row label="Kernel">{info.kernel}</Row>
        {running}
      </Card>
      <Card title="Hardware">
        <Row label="Machine">{[machine, info.guest && `virtual machine${info.guest !== 'unknown' ? ` (${info.guest})` : ''}`].filter(Boolean).join(' · ') || '—'}</Row>
        {board && board !== machine && <Row label="Board">{board}</Row>}
        {hardware.serial && <Row label="Serial">{hardware.serial}</Row>}
        {firmware && <Row label="BIOS">{firmware}</Row>}
        {info.memoryBytes > 0 && <Row label="Memory">{bytes(info.memoryBytes)}</Row>}
      </Card>
      <Card title="Processor">
        <Row label="CPU">{cpu.model || info.architecture}, {plural(cpu.cores, 'core')}{cpu.threads !== cpu.cores && `, ${plural(cpu.threads, 'thread')}`}</Row>
        {cpu.level && <Row label="Level">{cpu.level}</Row>}
        {cpu.microcode && <Row label="Microcode">{cpu.microcode}</Row>}
        <Row label="Hardware crypto">{list(cpu.features.cryptography)}</Row>
        <Row label="CPU security">{list(cpu.features.security)}</Row>
        <Row label="Virtualisation">{list([...cpu.features.virtualization, ...(info.iommu ? ['IOMMU active'] : [])])}</Row>
        <Row label="CPU vulnerabilities">
          {cpu.vulnerabilities.length === 0 ? 'no report' : !vulnerable && !unknown ? 'all mitigated' : (
            <>
              {vulnerable && <Text span inherit className="error-text">not mitigated: {vulnerable}</Text>}
              {vulnerable && unknown && '; '}
              {unknown && <>unknown: {unknown}</>}
            </>
          )}
        </Row>
      </Card>
    </Stack>
  );
}

export function SystemDetailsScreen({
  system,
  api,
  canRename = false,
  onSessionLost,
}: {
  system: System;
  api?: ApiClient;
  canRename?: boolean;
  onSessionLost?: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState('');
  const [fieldError, setFieldError] = useState('');
  const [submitError, setSubmitError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const submittingRef = useRef(false);

  const currentHostname = system.data?.info.hostname ?? '';
  const normalizedDraft = draft.trim().toLowerCase();
  const liveInvalid = normalizedDraft !== '' && !hostnamePattern.test(normalizedDraft);
  const unchanged = normalizedDraft !== '' && normalizedDraft === currentHostname;

  function openRename() {
    setDraft(currentHostname);
    setFieldError('');
    setSubmitError('');
    setNotice('');
    setEditing(true);
  }

  function closeRename() {
    if (submittingRef.current) return;
    setEditing(false);
    setFieldError('');
    setSubmitError('');
  }

  async function saveHostname(event: FormEvent) {
    event.preventDefault();
    if (!api || submittingRef.current) return;
    if (!normalizedDraft || !hostnamePattern.test(normalizedDraft)) {
      setFieldError(hostnameError);
      return;
    }
    if (normalizedDraft === currentHostname) {
      setFieldError('Enter a different hostname.');
      return;
    }
    submittingRef.current = true;
    setBusy(true);
    setFieldError('');
    setSubmitError('');
    try {
      const updated = await api.setHostname(normalizedDraft);
      void system.reload();
      setEditing(false);
      setNotice(`Hostname updated to ${updated.hostname}.`);
    } catch (error) {
      if (onSessionLost) reportError(error, onSessionLost, setSubmitError);
      else setSubmitError(apiErrorMessage(error));
    } finally {
      submittingRef.current = false;
      setBusy(false);
    }
  }

  return (
    <Stack className="details">
      <Group justify="space-between" align="flex-end" wrap="wrap">
        <div>
          <Title order={2}>System details</Title>
          <Text c="dimmed">What the box is made of and what it runs.</Text>
        </div>
        {api && canRename && system.data && (
          <Button size="xs" variant="default" onClick={openRename}>Change hostname</Button>
        )}
      </Group>
      {notice && <Alert color="green" role="status">{notice}</Alert>}
      <Loaded resource={system} what="the system description">{(loaded) => loaded && <SystemFacts info={loaded.info} loadedAt={loaded.loadedAt} />}</Loaded>
      <Modal
        opened={editing}
        onClose={closeRename}
        closeOnClickOutside={!busy}
        closeOnEscape={!busy}
        closeButtonProps={{ 'aria-label': 'Close', disabled: busy }}
        lockScroll={false}
        title="Change hostname"
      >
        <form onSubmit={saveHostname} noValidate>
          <Stack>
            {submitError && <Alert color="red" role="alert" title="Hostname was not changed">{submitError}</Alert>}
            <TextInput
              data-autofocus
              label="Hostname"
              description="1 to 63 lowercase letters, digits, or hyphens. Applied immediately to the appliance."
              value={draft}
              disabled={busy}
              required
              error={fieldError || (liveInvalid ? hostnameError : undefined)}
              onChange={(event) => {
                setDraft(event.currentTarget.value);
                setFieldError('');
                setSubmitError('');
              }}
            />
            <Group justify="flex-end" gap="xs">
              <Button variant="default" disabled={busy} onClick={closeRename}>Cancel</Button>
              <Button type="submit" loading={busy} disabled={unchanged}>Save hostname</Button>
            </Group>
          </Stack>
        </form>
      </Modal>
    </Stack>
  );
}
