import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { ApiError } from '../api';
import { render, screen, waitFor } from '../test/render';
import { LoginScreen } from './LoginScreen';

it('keeps login failures generic, associated and focused for another attempt', async () => {
  const login = vi.fn().mockRejectedValue(new ApiError(401, 'AUTHENTICATION_FAILED', 'Login or password is incorrect.'));
  render(<LoginScreen setupRequired={false} onLogin={login} />);

  expect(screen.getByLabelText(/^Login/)).toHaveFocus();
  await userEvent.type(screen.getByLabelText(/^Login/), 'missing');
  await userEvent.type(screen.getByLabelText(/^Password/), 'not-a-password');
  await userEvent.click(screen.getByRole('button', { name: 'Sign in' }));

  expect(await screen.findByRole('alert')).toHaveTextContent('Login or password is incorrect.');
  await waitFor(() => expect(screen.getByLabelText(/^Password/)).toHaveValue(''));
  expect(screen.queryByText(/missing account/i)).not.toBeInTheDocument();
});

it('directs an unbootstrapped appliance to the root CLI without accepting a password', () => {
  render(<LoginScreen setupRequired onLogin={vi.fn()} />);

  expect(screen.getByRole('heading', { name: 'Setup required' })).toBeInTheDocument();
  expect(screen.getByText(/andaru ctl users bootstrap-admin/)).toBeInTheDocument();
  expect(screen.queryByLabelText('Password')).not.toBeInTheDocument();
});
