import { fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { render, screen, waitFor, within } from '../test/render';
import { AcmePanel } from './AcmePanel';

afterEach(() => {
  vi.restoreAllMocks();
});

const sampleAccount = {
  state: 'saved',
  savedVersion: '5',
  version: '1',
  value: {
    id: 'aca_le_prod',
    name: "Let's Encrypt Prod",
    directoryUrl: 'https://acme-v02.api.letsencrypt.org/directory',
    email: 'admin@example.com',
  },
};

const sampleOrder = {
  state: 'saved',
  savedVersion: '5',
  version: '2',
  value: {
    id: 'aco_edge',
    name: 'Edge Wildcard',
    accountId: 'aca_le_prod',
    certificateId: 'crt_edge',
    domains: ['fw.example.com', '*.example.com'],
    keyAlgorithm: 'ecdsa-p256',
    renewBeforeDays: 30,
    enabled: true,
    challenge: {
      type: 'dns-01',
      dns01: {
        provider: 'cloudflare',
        propagationTimeoutSeconds: 120,
        cloudflare: { apiTokenRef: { secret: 'acme_cf_token', isSet: true, version: '1' } },
      },
    },
  },
};

const sampleSegment = {
  state: 'saved',
  savedVersion: '5',
  version: '1',
  value: {
    id: 'seg_wan',
    name: 'WAN',
    link: 'lnk_wan',
    zone: 'zon_wan',
    addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } },
  },
};

describe('AcmePanel', () => {
  it('creates an ACME account, creates a DNS-01 order, triggers renew/staging test, and deletes with If-Match', async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    let createdAccountBody: unknown;
    let createdOrderBody: unknown;
    let renewCalled = false;
    let stagingCalled = false;
    let deletedIfMatch: string | null = null;

    fetchRoutes({
      '/api/v1/config/acme/accounts': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleAccount] }),
      '/api/v1/config/acme/orders': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleOrder] }),
      '/api/v1/config/segments': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleSegment] }),
      '/api/v1/status/acme': () => jsonResponse(200, {
        items: [{
          orderId: 'aco_edge',
          accountId: 'aca_le_prod',
          certificateId: 'crt_edge',
          state: 'succeeded',
          nextRenewAt: '2027-01-15T00:00:00Z',
        }],
        accounts: [{ accountId: 'aca_le_prod', status: 'valid' }],
      }),
      'POST /api/v1/config/acme/accounts': (init) => {
        createdAccountBody = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(201, sampleAccount);
      },
      'POST /api/v1/config/acme/orders': (init) => {
        createdOrderBody = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(201, sampleOrder);
      },
      'POST /api/v1/acme/orders/aco_edge/renew': () => {
        renewCalled = true;
        return jsonResponse(202, { orderId: 'aco_edge', started: true });
      },
      'POST /api/v1/acme/orders/aco_edge/test-staging': () => {
        stagingCalled = true;
        return jsonResponse(202, { orderId: 'aco_edge', stagingTest: true });
      },
      'DELETE /api/v1/config/acme/orders/aco_edge': (init) => {
        deletedIfMatch = new Headers(init?.headers).get('If-Match');
        return jsonResponse(200, { state: 'saved', savedVersion: '6' });
      },
    });

    render(
      <AcmePanel
        api={new ApiClient('csrf')}
        certificates={[]}
        canReadConfig
        canReadCerts
        canManageCerts
        onSaved={onSaved}
        onSessionLost={vi.fn()}
      />,
    );

    expect(await screen.findByText('Edge Wildcard')).toBeInTheDocument();
    expect(screen.getByText('Succeeded')).toBeInTheDocument();
    expect(screen.getByText('Registered')).toBeInTheDocument();

    // Add ACME account.
    await user.click(screen.getByRole('button', { name: 'Add ACME account' }));
    const accDialog = await screen.findByRole('dialog', { name: 'Add ACME account' });
    fireEvent.change(within(accDialog).getByLabelText(/^Name/), { target: { value: 'LE Production' } });
    fireEvent.change(within(accDialog).getByLabelText(/^Contact email/), { target: { value: 'ops@example.com' } });
    await user.click(within(accDialog).getByRole('button', { name: 'Add account' }));
    await waitFor(() => expect(createdAccountBody).toEqual({
      name: 'LE Production',
      directoryUrl: 'https://acme-v02.api.letsencrypt.org/directory',
      email: 'ops@example.com',
    }));

    // Add DNS-01 order.
    await user.click(screen.getByRole('button', { name: 'Add ACME order' }));
    const ordDialog = await screen.findByRole('dialog', { name: 'Add ACME order' });
    fireEvent.change(within(ordDialog).getByLabelText(/^Name/), { target: { value: 'Wildcard TLS' } });
    fireEvent.change(within(ordDialog).getByLabelText(/^Domains/), { target: { value: 'example.com, *.example.com' } });
    fireEvent.change(within(ordDialog).getByLabelText(/^API Token/), { target: { value: 'cf-secret-token' } });
    await user.click(within(ordDialog).getByRole('button', { name: 'Add order' }));
    await waitFor(() => expect(createdOrderBody).toEqual({
      name: 'Wildcard TLS',
      accountId: 'aca_le_prod',
      domains: ['example.com', '*.example.com'],
      keyAlgorithm: 'ecdsa-p256',
      renewBeforeDays: 30,
      enabled: true,
      challenge: {
        type: 'dns-01',
        dns01: {
          provider: 'cloudflare',
          propagationTimeoutSeconds: 120,
          cloudflare: {
            apiTokenRef: { secret: expect.stringMatching(/^acme_cf_token_[0-9a-f]{12}$/), set: 'cf-secret-token', expectedVersion: '0' },
          },
        },
      },
    }));

    // Trigger Renew now & Test staging.
    await user.click(screen.getByRole('button', { name: 'Renew now' }));
    await waitFor(() => expect(renewCalled).toBe(true));

    await user.click(screen.getByRole('button', { name: 'Test staging' }));
    await waitFor(() => expect(stagingCalled).toBe(true));

    // Delete order with If-Match.
    const deleteButtons = screen.getAllByRole('button', { name: 'Delete' });
    await user.click(deleteButtons[1]!);
    const delDialog = await screen.findByRole('dialog', { name: 'Delete ACME order' });
    await user.click(within(delDialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(deletedIfMatch).toBe('"aco_edge.2"'));
    expect(onSaved).toHaveBeenCalledTimes(3);
  });

  it('rejects wildcard domains on HTTP-01 and hides mutating controls for read-only users', async () => {
    const user = userEvent.setup();
    fetchRoutes({
      '/api/v1/config/acme/accounts': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleAccount] }),
      '/api/v1/config/acme/orders': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleOrder] }),
      '/api/v1/config/segments': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleSegment] }),
      '/api/v1/status/acme': () => jsonResponse(200, {
        items: [{ orderId: 'aco_edge', accountId: 'aca_le_prod', state: 'failed', lastError: 'DNS timeout' }],
        accounts: [],
      }),
    });

    const { rerender } = render(
      <AcmePanel
        api={new ApiClient('csrf')}
        certificates={[]}
        canReadConfig
        canReadCerts
        canManageCerts
        onSaved={vi.fn()}
        onSessionLost={vi.fn()}
      />,
    );

    expect(await screen.findByText('DNS timeout')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Add ACME order' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add ACME order' });
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: 'Bad HTTP Wildcard' } });
    fireEvent.change(within(dialog).getByLabelText(/^Domains/), { target: { value: '*.example.com' } });
    await user.click(within(dialog).getByText('HTTP-01 (port 80)'));
    await user.click(within(dialog).getByRole('button', { name: 'Add order' }));
    expect(await within(dialog).findByText('Wildcard domains require DNS-01 validation.')).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    rerender(
      <AcmePanel
        api={new ApiClient('csrf')}
        certificates={[]}
        canReadConfig
        canReadCerts
        canManageCerts={false}
        onSaved={vi.fn()}
        onSessionLost={vi.fn()}
      />,
    );
    expect(screen.queryByRole('button', { name: 'Add ACME account' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Renew now' })).not.toBeInTheDocument();
  });
});
