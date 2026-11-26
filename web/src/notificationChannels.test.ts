import { describe, expect, it } from 'vitest';
import { ApiError } from './api';
import {
  buildChannelWrite,
  channelDraftOf,
  channelFieldErrors,
  existingChannelSecretsOf,
  nextChannelSecretName,
  sharedSecretHolders,
  validateChannelDraft,
  validateWebhookUrl,
} from './notificationChannels';
import type { NotificationChannel } from './types';

describe('notificationChannels', () => {
  it('allocates collision-free secret names matching core secret rules', () => {
    expect(nextChannelSecretName('Ops alerts', 'URL')).toBe('NOTIFY_OPS_ALERTS_URL');
    expect(nextChannelSecretName('Ops alerts', 'AUTH', ['NOTIFY_OPS_ALERTS_AUTH'])).toBe('NOTIFY_OPS_ALERTS_AUTH_2');
    expect(nextChannelSecretName('---', 'URL')).toBe('NOTIFY_URL');
    expect(nextChannelSecretName(`${'a'.repeat(39)} b`, 'URL')).toBe(`NOTIFY_${'A'.repeat(39)}_URL`);
  });

  it('validates webhook URLs, duplicate names, format chars, byte limits, and sources', () => {
    expect(validateWebhookUrl('', true)).toBe('Enter a webhook URL.');
    expect(validateWebhookUrl('', false)).toBeUndefined();
    expect(validateWebhookUrl('ftp://hooks.example/x', true)).toMatch(/http:\/\/ or https:\/\//);
    expect(validateWebhookUrl('https:///path', true)).toMatch(/http:\/\/ or https:\/\//);
    expect(validateWebhookUrl('https://\\hooks.example/x', true)).toMatch(/http:\/\/ or https:\/\//);
    expect(validateWebhookUrl('https://:8080/path', true)).toMatch(/http:\/\/ or https:\/\//);
    expect(validateWebhookUrl('http://user@/path', true)).toMatch(/http:\/\/ or https:\/\//);
    expect(validateWebhookUrl('https://hooks.example/a\nb', true)).toMatch(/whitespace or control/i);
    expect(validateWebhookUrl('https://hooks.example/a\0b', true)).toMatch(/whitespace or control/i);
    expect(validateWebhookUrl(`https://hooks.example/${'a'.repeat(4096)}`, true)).toMatch(/4096 bytes/);
    expect(validateWebhookUrl('https://hooks.example:8443/notify', true)).toBeUndefined();

    const errors = validateChannelDraft(
      { ...channelDraftOf(), name: 'ops', description: 'd'.repeat(4097), sources: ['bad\u200bsrc'], webhookUrl: 'not-a-url', webhookAuth: 'Bearer a\r\nb' },
      undefined,
      ['ops'],
    );
    expect(errors.get('name')).toMatch(/already has this name/i);
    expect(errors.get('description')).toMatch(/4096 characters/);
    expect(errors.get('sources')).toMatch(/control or format characters/i);
    expect(errors.get('webhookUrl')).toMatch(/http:\/\/ or https:\/\//);
    expect(errors.get('webhookAuth')).toMatch(/control characters or line breaks/i);

    const formatNameErr = validateChannelDraft(
      { ...channelDraftOf(), name: 'ops\u202e', webhookUrl: 'https://hooks.example/ok', webhookAuth: 'x'.repeat(4097) },
      undefined,
    );
    expect(formatNameErr.get('name')).toMatch(/control or format characters/i);
    expect(formatNameErr.get('webhookAuth')).toMatch(/4096 bytes/);
  });

  it('builds create, unchanged keep, unresolved echo, secret rotation, and auth removal payloads', () => {
    const createPayload = buildChannelWrite(
      {
        ...channelDraftOf(),
        name: 'Ops Slack',
        description: '  On-call room  ',
        minSeverity: 'warning',
        sources: ['upgrade', 'apply', 'apply'],
        webhookUrl: ' https://hooks.example/token ',
        webhookAuth: ' Bearer secret ',
      },
      undefined,
      ['NOTIFY_OPS_SLACK_URL'],
    );
    expect(createPayload).toEqual({
      name: 'Ops Slack',
      description: 'On-call room',
      enabled: true,
      kind: 'webhook',
      filter: { minSeverity: 'warning', sources: ['apply', 'upgrade'] },
      webhook: {
        url: { secret: 'NOTIFY_OPS_SLACK_URL_2', set: 'https://hooks.example/token', expectedVersion: '0' },
        authorization: { secret: 'NOTIFY_OPS_SLACK_AUTH', set: 'Bearer secret', expectedVersion: '0' },
      },
    });

    const saved: NotificationChannel = {
      id: 'ntf_1',
      name: 'Ops Slack',
      enabled: true,
      kind: 'webhook',
      filter: { minSeverity: 'warning', sources: ['apply'] },
      webhook: {
        url: { secret: 'OPS_URL', isSet: false, version: '0', unresolved: true },
        authorization: { secret: 'OPS_AUTH', isSet: true, version: '8' },
      },
    };

    // Disabling a restored unresolved channel echoes unresolved: true on URL and preserves auth reference.
    const keepUnresolved = buildChannelWrite({ ...channelDraftOf(saved), enabled: false }, saved, existingChannelSecretsOf([saved]));
    expect(keepUnresolved.webhook).toEqual({
      url: { secret: 'OPS_URL', unresolved: true },
      authorization: { secret: 'OPS_AUTH' },
    });

    // Entering a new URL or auth value allocates a fresh secret (expectedVersion: '0') so no other
    // channel referencing the old secret (including one added concurrently) can be silently modified.
    const resolvedAndRemoved = buildChannelWrite(
      { ...channelDraftOf(saved), webhookUrl: 'https://hooks.example/new', removeAuth: true },
      saved,
    );
    expect(resolvedAndRemoved.webhook).toEqual({
      url: { secret: 'NOTIFY_OPS_SLACK_URL', set: 'https://hooks.example/new', expectedVersion: '0' },
    });

    const rotated = buildChannelWrite(
      { ...channelDraftOf(saved), webhookAuth: 'Bearer rotated' },
      saved,
    );
    expect(rotated.webhook).toEqual({
      url: { secret: 'OPS_URL', unresolved: true },
      authorization: { secret: 'NOTIFY_OPS_SLACK_AUTH', set: 'Bearer rotated', expectedVersion: '0' },
    });

    // Even if another channel concurrently starts referencing OPS_URL after open (not in existingSecrets),
    // saving a new URL still writes a fresh secret rather than mutating OPS_URL in place.
    const second: NotificationChannel = {
      id: 'ntf_2',
      name: 'Secondary',
      enabled: true,
      kind: 'webhook',
      webhook: { url: { secret: 'OPS_URL', isSet: true, version: '8' } },
    };
    expect(sharedSecretHolders('OPS_URL', saved.id, [saved, second])).toEqual(['Secondary']);
    const separated = buildChannelWrite(
      { ...channelDraftOf(saved), webhookUrl: 'https://hooks.example/separate' },
      saved,
      ['NOTIFY_OPS_SLACK_URL'],
    );
    expect(separated.webhook).toEqual({
      url: { secret: 'NOTIFY_OPS_SLACK_URL_2', set: 'https://hooks.example/separate', expectedVersion: '0' },
      authorization: { secret: 'OPS_AUTH' },
    });
  });

  it('maps 422 validation pointer paths (direct and collection-prefixed) to form fields', () => {
    const err = new ApiError(422, 'VALIDATION_FAILED', 'Invalid.', [
      { path: '/name', message: 'Name already exists in this collection.' },
      { path: '/notifications/channels/0/filter/sources/0', message: 'Bad source.' },
      { path: '/webhook/url', message: 'This secret belongs to another kind of object.' },
      { path: '/webhook/authorization', message: 'Bad auth secret.' },
    ]);
    expect(channelFieldErrors(err)).toEqual(new Map([
      ['name', 'Name already exists in this collection.'],
      ['sources', 'Bad source.'],
      ['webhookUrl', 'This secret belongs to another kind of object.'],
      ['webhookAuth', 'Bad auth secret.'],
    ]));
  });
});
