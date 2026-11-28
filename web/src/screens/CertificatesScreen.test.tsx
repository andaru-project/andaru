import { fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen, waitFor, within } from '../test/render';
import { CertificatesScreen } from './CertificatesScreen';

afterEach(() => {
  vi.restoreAllMocks();
});

const futureDate = new Date(Date.now() + 90 * 86_400_000).toISOString();
const soonDate = new Date(Date.now() + 10 * 86_400_000).toISOString();

const sampleCA = {
  state: 'saved',
  savedVersion: '5',
  version: '1',
  value: {
    id: 'ca_root',
    name: 'Internal Root CA',
    subject: 'CN=Andaru Internal Root CA',
    serial: '01',
    fingerprintSha256: '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef',
    notBefore: '2026-01-01T00:00:00Z',
    notAfter: futureDate,
    certPem: '-----BEGIN CERTIFICATE-----\nROOT\n-----END CERTIFICATE-----',
    keyRef: { secret: 'ca_root', isSet: true, version: '1' },
  },
};

const sampleCert = {
  state: 'saved',
  savedVersion: '5',
  version: '2',
  value: {
    id: 'crt_web',
    name: 'Gateway HTTPS',
    caId: 'ca_root',
    subject: 'CN=fw.example.lan',
    sans: ['fw.example.lan', '192.168.1.1'],
    serial: '02',
    fingerprintSha256: 'fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210',
    notBefore: '2026-01-01T00:00:00Z',
    notAfter: soonDate,
    certPem: '-----BEGIN CERTIFICATE-----\nLEAF\n-----END CERTIFICATE-----',
    keyRef: { secret: 'crt_web', isSet: true, version: '1' },
    keyAlgorithm: 'ecdsa-p256',
  },
};

const sampleTlsCrypt = {
  state: 'saved',
  savedVersion: '5',
  version: '1',
  value: {
    id: 'tck_office',
    name: 'Office OpenVPN Key',
    keyRef: { secret: 'tck_office', isSet: true, version: '1' },
  },
};

describe('CertificatesScreen', () => {
  it('generates and imports certificates and deletes with If-Match', async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    let generatedCertBody: unknown;
    let importedCertBody: unknown;
    let deletedCertHeader: string | null = null;

    fetchRoutes({
      '/api/v1/config/pki/cas': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleCA] }),
      '/api/v1/config/pki/certificates': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleCert] }),
      '/api/v1/config/pki/tls-crypt-keys': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleTlsCrypt] }),
      'POST /api/v1/pki/certificates/generate': (init) => {
        generatedCertBody = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(201, sampleCert);
      },
      'POST /api/v1/pki/certificates/import': (init) => {
        importedCertBody = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(201, { ...sampleCert, value: { ...sampleCert.value, id: 'crt_imp', name: 'Imported Edge' } });
      },
      'DELETE /api/v1/config/pki/certificates/crt_web': (init) => {
        deletedCertHeader = new Headers(init?.headers).get('If-Match');
        return new Response(null, { status: 204 });
      },
    });

    render(
      <CertificatesScreen
        api={new ApiClient('csrf')}
        session={session(['configuration.read', 'configuration.write', 'certificates.read', 'certificates.manage'])}
        onSaved={onSaved}
        onSessionLost={vi.fn()}
      />,
    );

    expect(await screen.findByText('Gateway HTTPS')).toBeInTheDocument();
    const certPanel = screen.getByRole('tabpanel');
    expect(within(certPanel).getByText(/Expires in \d+d/)).toBeInTheDocument();
    expect(within(certPanel).getByText('Internal Root CA')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Generate certificate' }));
    const genDialog = await screen.findByRole('dialog', { name: 'Generate certificate' });
    fireEvent.change(within(genDialog).getByLabelText(/^Name/), { target: { value: 'VPN Server' } });
    fireEvent.change(within(genDialog).getByLabelText(/^Common Name/), { target: { value: 'vpn.example.lan' } });
    fireEvent.change(within(genDialog).getByLabelText(/Subject Alternative Names/), { target: { value: 'vpn.example.lan, 10.0.0.1' } });
    await user.click(within(genDialog).getByRole('button', { name: 'Generate' }));
    await waitFor(() => expect(generatedCertBody).toEqual({
      name: 'VPN Server',
      caId: 'ca_root',
      commonName: 'vpn.example.lan',
      sans: ['vpn.example.lan', '10.0.0.1'],
      usage: 'server',
      keyAlgorithm: 'ecdsa-p256',
      validDays: 365,
    }));

    await user.click(screen.getByRole('button', { name: 'Import PEM' }));
    const impDialog = await screen.findByRole('dialog', { name: 'Import PEM certificate' });
    fireEvent.change(within(impDialog).getByLabelText(/^Name/), { target: { value: 'Imported Edge' } });
    fireEvent.change(within(impDialog).getByLabelText(/^Certificate PEM/), { target: { value: '-----BEGIN CERTIFICATE-----\nABC\n-----END CERTIFICATE-----' } });
    fireEvent.change(within(impDialog).getByLabelText(/^Private key PEM/), { target: { value: '-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----' } });
    await user.click(within(impDialog).getByRole('button', { name: 'Import certificate' }));
    await waitFor(() => expect(importedCertBody).toEqual({
      name: 'Imported Edge',
      certPem: '-----BEGIN CERTIFICATE-----\nABC\n-----END CERTIFICATE-----',
      privateKeyPem: '-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----',
    }));

    await user.click(within(certPanel).getByRole('button', { name: 'Delete' }));
    const delDialog = await screen.findByRole('dialog', { name: 'Delete PKI object' });
    await user.click(within(delDialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(deletedCertHeader).toBe('"crt_web.2"'));
    expect(onSaved).toHaveBeenCalledTimes(3);
  });

  it('creates certificate authorities and generates TLS-Crypt keys', async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    let generatedCaBody: unknown;
    let generatedTlsCryptBody: unknown;

    fetchRoutes({
      '/api/v1/config/pki/cas': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleCA] }),
      '/api/v1/config/pki/certificates': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleCert] }),
      '/api/v1/config/pki/tls-crypt-keys': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleTlsCrypt] }),
      'POST /api/v1/pki/cas/generate': (init) => {
        generatedCaBody = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(201, sampleCA);
      },
      'POST /api/v1/pki/tls-crypt-keys/generate': (init) => {
        generatedTlsCryptBody = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(201, sampleTlsCrypt);
      },
    });

    render(
      <CertificatesScreen
        api={new ApiClient('csrf')}
        session={session(['configuration.read', 'configuration.write', 'certificates.read', 'certificates.manage'])}
        onSaved={onSaved}
        onSessionLost={vi.fn()}
      />,
    );

    expect(await screen.findByText('Gateway HTTPS')).toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: /Certificate Authorities/ }));
    expect(await screen.findByText('Can sign')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Create CA' }));
    const caDialog = await screen.findByRole('dialog', { name: 'Create certificate authority' });
    fireEvent.change(within(caDialog).getByLabelText(/^Name/), { target: { value: 'VPN CA' } });
    fireEvent.change(within(caDialog).getByLabelText(/^Common Name/), { target: { value: 'Andaru VPN CA' } });
    await user.click(within(caDialog).getByRole('button', { name: 'Create CA' }));
    await waitFor(() => expect(generatedCaBody).toEqual({
      name: 'VPN CA',
      commonName: 'Andaru VPN CA',
      keyAlgorithm: 'ecdsa-p384',
      validDays: 3650,
    }));

    await user.click(screen.getByRole('tab', { name: /TLS-Crypt Keys/ }));
    expect(await screen.findByText('Office OpenVPN Key')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Generate TLS-Crypt key' }));
    const tckDialog = await screen.findByRole('dialog', { name: 'Generate OpenVPN TLS-Crypt key' });
    fireEvent.change(within(tckDialog).getByLabelText(/^Name/), { target: { value: 'Branch Key' } });
    await user.click(within(tckDialog).getByRole('button', { name: 'Generate key' }));
    await waitFor(() => expect(generatedTlsCryptBody).toEqual({ name: 'Branch Key' }));
    expect(onSaved).toHaveBeenCalledTimes(2);
  });

  it('hides mutating actions for read-only users while keeping PEM download', async () => {
    fetchRoutes({
      '/api/v1/config/pki/cas': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleCA] }),
      '/api/v1/config/pki/certificates': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleCert] }),
      '/api/v1/config/pki/tls-crypt-keys': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleTlsCrypt] }),
    });

    render(
      <CertificatesScreen
        api={new ApiClient('csrf')}
        session={session(['configuration.read', 'certificates.read'])}
        onSaved={vi.fn()}
        onSessionLost={vi.fn()}
      />,
    );

    expect(await screen.findByText('Gateway HTTPS')).toBeInTheDocument();
    const certPanel = screen.getByRole('tabpanel');
    expect(within(certPanel).getByRole('button', { name: 'Download PEM' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Generate certificate' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Import PEM' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
  });
});
