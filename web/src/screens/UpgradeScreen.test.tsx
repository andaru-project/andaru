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

it('reads the questions again, from no answers, when the saved settings changed before the answers saved', async () => {
  let head = '7';
  const posted: string[] = [];
  fetchRoutes({
    'POST /api/v1/config/upgrade/preview': () => jsonResponse(200, { savedVersion: head, questions: [{ ...first, answered: true, choice: 'keep' }] }),
    'POST /api/v1/config/upgrade': (init) => {
      const body = JSON.parse(String(init?.body)) as { expectedSavedVersion: string };
      posted.push(body.expectedSavedVersion);
      if (body.expectedSavedVersion !== head) return jsonResponse(412, { error: { code: 'PRECONDITION_FAILED', message: 'Precondition Failed' } });
      return jsonResponse(200, { state: 'saved', savedVersion: '10' });
    },
  });
  render(<UpgradeScreen api={new ApiClient('csrf')} session={session(['configuration.read', 'configuration.write'])} onFinished={vi.fn()} onSessionLost={vi.fn()} />);
  const save = await screen.findByRole('button', { name: 'Save the answers' });
  await vi.waitFor(() => expect(save).toBeEnabled());
  head = '9';
  await userEvent.click(save);
  expect(await screen.findByRole('alert')).toBeInTheDocument();
  await vi.waitFor(() => expect(save).toBeEnabled());
  await userEvent.click(save);
  expect(await screen.findByText('Saved as version 10')).toBeInTheDocument();
  expect(posted).toEqual(['7', '9']);
});

it('names the choices of each question after it', async () => {
  engine();
  render(<UpgradeScreen api={new ApiClient('csrf')} session={session(['configuration.read'])} onFinished={vi.fn()} onSessionLost={vi.fn()} />);
  expect(await screen.findByRole('radiogroup', { name: 'What becomes of the zones?' })).toBeInTheDocument();
});

it('offers to try again when the questions cannot be read', async () => {
  let fail = true;
  fetchRoutes({
    'POST /api/v1/config/upgrade/preview': () => (fail ? jsonResponse(503, { error: { code: 'STORE_UNAVAILABLE', message: 'Unavailable.' } }) : jsonResponse(200, { savedVersion: '7', questions: [first] })),
  });
  render(<UpgradeScreen api={new ApiClient('csrf')} session={session(['configuration.read', 'configuration.write'])} onFinished={vi.fn()} onSessionLost={vi.fn()} />);
  const retry = await screen.findByRole('button', { name: 'Try again' });
  fail = false;
  await userEvent.click(retry);
  expect(await screen.findByText('What becomes of the zones?')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument();
});

it('clears answers given for an earlier head when the saved settings move', async () => {
  let head = '7';
  const previews: Array<Record<string, string>> = [];
  fetchRoutes({
    'POST /api/v1/config/upgrade/preview': (init) => {
      const { choices: given } = JSON.parse(String(init?.body)) as { choices: Record<string, string> };
      previews.push(given);
      const questions = [{ ...first, answered: 'first' in given, choice: given.first ?? 'keep' }, { ...second, answered: 'second' in given, choice: given.second ?? 'keep' }];
      return jsonResponse(200, { savedVersion: head, questions });
    },
  });
  render(<UpgradeScreen api={new ApiClient('csrf')} session={session(['configuration.read', 'configuration.write'])} onFinished={vi.fn()} onSessionLost={vi.fn()} />);
  await userEvent.click((await screen.findAllByRole('radio', { name: /Mark them/ }))[0]);
  await vi.waitFor(() => expect(previews.at(-1)).toEqual({ first: 'mark' }));
  head = '9';
  await userEvent.click(screen.getAllByRole('radio', { name: /Keep the zones/ })[1]);
  expect(await screen.findByText('The saved settings changed')).toBeInTheDocument();
  await vi.waitFor(() => expect(previews.at(-1)).toEqual({}));
  expect(screen.getByRole('button', { name: 'Save the answers' })).toBeDisabled();
});
