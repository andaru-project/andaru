import { expect, it, vi } from 'vitest';
import userEvent from '@testing-library/user-event';
import { ApiClient } from '../api';
import type { Session, UpgradeQuestion } from '../types';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { render, screen } from '../test/render';
import { UpgradeScreen } from './UpgradeScreen';

const session = (capabilities: string[]): Session => ({
  account: { id: '0657cce4-d4f9-4d1e-9cae-53a99855bb10', login: 'admin', displayName: 'Administrator' },
  capabilities, csrfToken: 'csrf', expiresAt: '2026-11-16T12:00:00Z', idleTimeoutSeconds: 1800,
});
const choices = [{ id: 'keep', label: 'Keep the zones', description: 'As they were.' }, { id: 'mark', label: 'Mark them', description: 'Asks one more question.' }];
const first: UpgradeQuestion = { id: 'first', question: 'What becomes of the zones?', choices, provisional: 'keep', objects: ['zon_lan'], choice: 'keep', answered: false };
const second: UpgradeQuestion = { id: 'second', question: 'And the marked ones?', choices, provisional: 'keep', objects: ['zon_lan'], choice: 'keep', answered: false };

// The engine raises the second question once the first is answered with "mark".
function engine() {
  const bodies: Array<Record<string, unknown>> = [];
  const mock = fetchRoutes({
    'POST /api/v1/config/upgrade/preview': (init) => {
      const { choices: given } = JSON.parse(String(init?.body)) as { choices: Record<string, string> };
      bodies.push(given);
      const questions = [{ ...first, answered: 'first' in given, choice: given.first ?? 'keep' }];
      if (given.first === 'mark') questions.push({ ...second, answered: 'second' in given, choice: given.second ?? 'keep' });
      return jsonResponse(200, { savedVersion: '7', questions });
    },
    'POST /api/v1/config/upgrade': (init) => {
      bodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
      return jsonResponse(200, { state: 'saved', savedVersion: '8' });
    },
  });
  return { mock, bodies };
}

it('asks each question, the ones a choice raises too, and saves the answers against the head they came from', async () => {
  const { bodies } = engine();
  const onFinished = vi.fn();
  render(<UpgradeScreen api={new ApiClient('csrf')} session={session(['configuration.read', 'configuration.write'])} onFinished={onFinished} onSessionLost={vi.fn()} />);

  expect(await screen.findByText('What becomes of the zones?')).toBeInTheDocument();
  expect(screen.getAllByText('Current reading')).toHaveLength(1);
  const save = screen.getByRole('button', { name: 'Save the answers' });
  expect(save).toBeDisabled();

  await userEvent.click(screen.getByRole('radio', { name: /Mark them/ }));
  expect(await screen.findByText('And the marked ones?')).toBeInTheDocument();
  expect(screen.getByText('One question is still open.')).toBeInTheDocument();
  expect(save).toBeDisabled();

  await userEvent.click(screen.getAllByRole('radio', { name: /Keep the zones/ })[1]);
  await vi.waitFor(() => expect(save).toBeEnabled());
  await userEvent.click(save);
  expect(await screen.findByText('Saved as version 8')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Save the answers' })).not.toBeInTheDocument();
  expect(bodies.at(-1)).toEqual({ choices: { first: 'mark', second: 'keep' }, expectedSavedVersion: '7' });
  expect(onFinished).toHaveBeenCalled();
});

it('shows the questions read-only to an account that cannot change the configuration', async () => {
  engine();
  render(<UpgradeScreen api={new ApiClient('csrf')} session={session(['configuration.read'])} onFinished={vi.fn()} onSessionLost={vi.fn()} />);

  expect(await screen.findByText('What becomes of the zones?')).toBeInTheDocument();
  expect(screen.getByRole('radio', { name: /Mark them/ })).toBeDisabled();
  expect(screen.queryByRole('button', { name: 'Save the answers' })).not.toBeInTheDocument();
  expect(screen.getByText(/needs permission to change the configuration/)).toBeInTheDocument();
});
