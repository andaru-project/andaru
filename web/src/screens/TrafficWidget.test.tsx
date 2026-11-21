import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes } from '../test/fetch';
import { render, screen } from '../test/render';
import type { Traffic, TrafficSample } from '../types';
import { TrafficView, TrafficWidget } from './TrafficWidget';

const sample = (second: number, rxBytes: number, txBytes: number): TrafficSample => ({
  at: new Date(Date.UTC(2026, 10, 21, 12, 0, second)).toISOString(), rxBytes, txBytes, rxPackets: 0, txPackets: 0, rxErrors: 0, txErrors: 0, rxDropped: 0, txDropped: 0,
});

// ens18 received 1, 2, then 4 Mbit/s and sent a steady 1 Mbit/s; ens19 hardly moved; veth0 has one reading.
const megabit = 3_750_000;
const traffic: Traffic = {
  intervalSeconds: 30,
  interfaces: [
    { name: 'ens18', samples: [sample(0, 0, 0), sample(30, megabit, megabit), sample(60, 3 * megabit, 2 * megabit), sample(90, 7 * megabit, 3 * megabit)] },
    { name: 'ens19', samples: [sample(0, 0, 0), sample(30, 375, 0)] },
    { name: 'veth0', samples: [sample(90, 5, 5)] },
  ],
};

const zones = new Map([['ens18', 'wan'], ['ens19', 'lan']]);
const lines = (container: HTMLElement, name: string) => [...container.querySelectorAll(`polyline.${name}`)].map((line) => line.getAttribute('points')!.split(' ').length);

it('shows the busiest interface first, as a graph and as the same in numbers', () => {
  const { container } = render(<TrafficView traffic={traffic} zones={zones} chosen={undefined} onChoose={vi.fn()} />);
  expect(screen.getByRole('combobox', { name: 'Interface' })).toHaveValue('ens18');
  // The zone is on each choice and, as a badge, beside the one shown; an interface in no zone has neither.
  expect(screen.getAllByRole('option').map((option) => option.textContent)).toEqual(['ens18 (wan)', 'ens19 (lan)', 'veth0']);
  expect(screen.getByLabelText('zone wan')).toHaveTextContent('wan');
  expect(screen.getByRole('img', { name: 'Traffic on ens18 over the last hour, up to 4.0 Mbit/s' })).toBeInTheDocument();
  expect(lines(container, 'traffic-received')).toEqual([3]);
  expect(lines(container, 'traffic-sent')).toEqual([3]);
  expect(screen.getByText('Received: 4.0 Mbit/s now, 2.3 Mbit/s on average, 4.0 Mbit/s at most.')).toBeInTheDocument();
  expect(screen.getByText('Sent: 1.0 Mbit/s now, 1.0 Mbit/s on average, 1.0 Mbit/s at most.')).toBeInTheDocument();
  // The lines are told apart in words, not by colour.
  expect(screen.getByText(/Solid line: received\. Dashed line: sent\./)).toBeInTheDocument();
});

it('draws a gap as a gap: the line breaks where a counter started again', () => {
  const restarted: Traffic = { intervalSeconds: 30, interfaces: [{ name: 'ens18', samples: [sample(0, 0, 0), sample(30, megabit, 0), sample(60, 2 * megabit, 0), sample(90, 100, 0), sample(120, megabit, 0), sample(150, 2 * megabit, 0)] }] };
  const { container } = render(<TrafficView traffic={restarted} zones={zones} chosen="ens18" onChoose={vi.fn()} />);
  expect(lines(container, 'traffic-received')).toEqual([2, 2]);
});

it('says so when an interface has no rate yet, and when the engine has read nothing', () => {
  const view = render(<TrafficView traffic={traffic} zones={zones} chosen="veth0" onChoose={vi.fn()} />);
  expect(screen.getByText('Received: no readings yet.')).toBeInTheDocument();
  view.rerender(<TrafficView traffic={{ intervalSeconds: 30, interfaces: [] }} zones={zones} chosen={undefined} onChoose={vi.fn()} />);
  expect(screen.getByText(/No readings yet/)).toBeInTheDocument();
});

it('falls back to the busiest when the chosen interface has gone', () => {
  render(<TrafficView traffic={traffic} zones={zones} chosen="ppp0" onChoose={vi.fn()} />);
  expect(screen.getByRole('combobox', { name: 'Interface' })).toHaveValue('ens18');
});

it('reads the history and lets another interface be chosen', async () => {
  fetchRoutes({ '/api/v1/status/traffic': traffic });
  render(<TrafficWidget api={new ApiClient('csrf')} zones={zones} onSessionLost={vi.fn()} />);
  const select = await screen.findByRole('combobox', { name: 'Interface' });
  await userEvent.selectOptions(select, 'ens19');
  expect(select).toHaveValue('ens19');
  expect(screen.getByText('Received: 100 bit/s now, 100 bit/s on average, 100 bit/s at most.')).toBeInTheDocument();
});
