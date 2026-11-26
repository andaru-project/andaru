import { describe, expect, it } from 'vitest';
import { ApiError } from './api';
import {
  buildChannelWrite,
  channelDraftOf,
  channelFieldErrors,
  channelSecretsOf,
  existingChannelSecretsOf,
  nextChannelSecretName,
  sharedSecretHolders,
  validateChannelDraft,
  validateNtfyServer,
  validateNtfyTopic,
  validateTelegramChatId,
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
      { ...channelDraftOf(saved), webhookUrl: 'https://hooks.example/new', removeWebhookAuth: true },
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

  it('validates and builds ntfy, pushover, telegram, kind switches, and unknown future kinds', () => {
    // ntfy server & topic validation.
    expect(validateNtfyServer('')).toMatch(/Enter an ntfy server URL/);
    expect(validateNtfyServer('https://ntfy.sh/topic')).toMatch(/host only/);
    expect(validateNtfyServer('https://ntfy.sh/')).toMatch(/host only/);
    expect(validateNtfyServer('https://user:pass@ntfy.sh')).toMatch(/host only/);
    expect(validateNtfyServer('https://ntfy.sh?a=1')).toMatch(/host only/);
    expect(validateNtfyServer('https://ntfy.sh#frag')).toMatch(/host only/);
    expect(validateNtfyServer('ftp://ntfy.sh')).toMatch(/host only/);
    expect(validateNtfyServer('https://ntfy.sh')).toBeUndefined();
    expect(validateNtfyServer('http://ntfy.internal:8080')).toBeUndefined();
    expect(validateNtfyServer('http://[::1]:8080')).toBeUndefined();

    expect(validateNtfyTopic('')).toMatch(/Enter an ntfy topic/);
    expect(validateNtfyTopic('bad/topic')).toMatch(/1 to 64/);
    expect(validateNtfyTopic('a'.repeat(65))).toMatch(/1 to 64/);
    expect(validateNtfyTopic('andaru_alerts-1')).toBeUndefined();

    // Telegram chatId validation.
    expect(validateTelegramChatId('')).toMatch(/Enter a Telegram chat ID/);
    expect(validateTelegramChatId('not_a_chat')).toMatch(/numeric chat ID/i);
    expect(validateTelegramChatId('@bad-name')).toMatch(/numeric chat ID/i);
    expect(validateTelegramChatId(`-${'1'.repeat(64)}`)).toMatch(/64 characters/);
    expect(validateTelegramChatId('-1001234567890')).toBeUndefined();
    expect(validateTelegramChatId('123456789')).toBeUndefined();
    expect(validateTelegramChatId('@ops_alerts')).toBeUndefined();

    // Build ntfy without token, then with token, then removing token.
    const ntfyNoToken = buildChannelWrite(
      { ...channelDraftOf(), name: 'Ntfy Ops', kind: 'ntfy', ntfyServer: 'https://ntfy.sh', ntfyTopic: 'andaru-alerts' },
      undefined,
    );
    expect(ntfyNoToken).toEqual({
      name: 'Ntfy Ops',
      enabled: true,
      kind: 'ntfy',
      filter: { minSeverity: 'info' },
      ntfy: { server: 'https://ntfy.sh', topic: 'andaru-alerts' },
    });
    expect('webhook' in ntfyNoToken).toBe(false);

    const ntfySaved: NotificationChannel = {
      id: 'ntf_ntfy',
      name: 'Ntfy Ops',
      enabled: true,
      kind: 'ntfy',
      ntfy: { server: 'https://ntfy.sh', topic: 'andaru-alerts', token: { secret: 'NTFY_TOK', isSet: true, version: '3' } },
    };
    const ntfyRemoved = buildChannelWrite({ ...channelDraftOf(ntfySaved), removeNtfyToken: true }, ntfySaved);
    expect(ntfyRemoved.ntfy).toEqual({ server: 'https://ntfy.sh', topic: 'andaru-alerts' });

    // Build Pushover (both required secrets).
    const pushoverErrors = validateChannelDraft({ ...channelDraftOf(), name: 'PO', kind: 'pushover' }, undefined);
    expect(pushoverErrors.get('pushoverToken')).toMatch(/application token/i);
    expect(pushoverErrors.get('pushoverUser')).toMatch(/user key/i);

    const pushoverWrite = buildChannelWrite(
      { ...channelDraftOf(), name: 'PO', kind: 'pushover', pushoverToken: 'app-tok', pushoverUser: 'usr-key' },
      undefined,
    );
    expect(pushoverWrite).toEqual({
      name: 'PO',
      enabled: true,
      kind: 'pushover',
      filter: { minSeverity: 'info' },
      pushover: {
        token: { secret: 'NOTIFY_PO_TOKEN', set: 'app-tok', expectedVersion: '0' },
        user: { secret: 'NOTIFY_PO_USER', set: 'usr-key', expectedVersion: '0' },
      },
    });

    // Build Telegram and verify kind switching requires new kind's secrets and emits only its sub-object.
    const switchErrors = validateChannelDraft({ ...channelDraftOf(ntfySaved), kind: 'telegram', telegramChatId: '@ops' }, ntfySaved);
    expect(switchErrors.get('telegramToken')).toMatch(/bot token/i);
    const telegramWrite = buildChannelWrite(
      { ...channelDraftOf(ntfySaved), kind: 'telegram', telegramToken: '123:abc', telegramChatId: ' -1001234567890 ' },
      ntfySaved,
      existingChannelSecretsOf([ntfySaved]),
    );
    expect(telegramWrite).toEqual({
      name: 'Ntfy Ops',
      enabled: true,
      kind: 'telegram',
      filter: { minSeverity: 'info' },
      telegram: {
        token: { secret: 'NOTIFY_NTFY_OPS_TOKEN', set: '123:abc', expectedVersion: '0' },
        chatId: '-1001234567890',
      },
    });
    expect('ntfy' in telegramWrite).toBe(false);

    // Unknown future kind (e.g. email): secrets collected recursively, sub-object echoed unchanged without SecretView metadata.
    const futureEmail: NotificationChannel = {
      id: 'ntf_email',
      name: 'SMTP Ops',
      enabled: true,
      kind: 'email',
      filter: { minSeverity: 'critical' },
      email: {
        host: 'mail.example',
        port: 587,
        from: 'andaru@example.com',
        to: ['ops@example.com'],
        password: { secret: 'SHARED_SMTP_PASS', isSet: true, version: '4', updatedAt: '2026-11-26T00:00:00Z' },
        backupAuth: { secret: 'BACKUP_PASS', isSet: false, version: '0', unresolved: true },
      },
    };
    const otherSharing: NotificationChannel = {
      id: 'ntf_other',
      name: 'Other',
      enabled: true,
      kind: 'ntfy',
      ntfy: { server: 'https://ntfy.sh', topic: 'x', token: { secret: 'SHARED_SMTP_PASS', isSet: true, version: '4' } },
    };
    expect(channelSecretsOf(futureEmail).map((e) => e.view.secret)).toEqual(['SHARED_SMTP_PASS', 'BACKUP_PASS']);
    expect(existingChannelSecretsOf([futureEmail, otherSharing])).toEqual(['SHARED_SMTP_PASS', 'BACKUP_PASS']);
    expect(sharedSecretHolders('SHARED_SMTP_PASS', otherSharing.id, [futureEmail, otherSharing])).toEqual(['SMTP Ops']);

    const disabledEmail = buildChannelWrite({ ...channelDraftOf(futureEmail), enabled: false }, futureEmail);
    expect(disabledEmail).toEqual({
      name: 'SMTP Ops',
      enabled: false,
      kind: 'email',
      filter: { minSeverity: 'critical' },
      email: {
        host: 'mail.example',
        port: 587,
        from: 'andaru@example.com',
        to: ['ops@example.com'],
        password: { secret: 'SHARED_SMTP_PASS' },
        backupAuth: { secret: 'BACKUP_PASS', unresolved: true },
      },
    });
    expect('webhook' in disabledEmail).toBe(false);
  });

  it('maps 422 validation pointer paths (direct and collection-prefixed) to form fields', () => {
    const err = new ApiError(422, 'VALIDATION_FAILED', 'Invalid.', [
      { path: '/name', message: 'Name already exists in this collection.' },
      { path: '/notifications/channels/0/filter/sources/0', message: 'Bad source.' },
      { path: '/webhook/url', message: 'This secret belongs to another kind of object.' },
      { path: '/webhook/authorization', message: 'Bad auth secret.' },
      { path: '/ntfy/server', message: 'Bad server.' },
      { path: '/ntfy/topic', message: 'Bad topic.' },
      { path: '/ntfy/token', message: 'Bad ntfy token.' },
      { path: '/pushover/token', message: 'Bad app token.' },
      { path: '/pushover/user', message: 'Bad user key.' },
      { path: '/telegram/token', message: 'Bad bot token.' },
      { path: '/telegram/chatId', message: 'Bad chat id.' },
    ]);
    expect(channelFieldErrors(err)).toEqual(new Map([
      ['name', 'Name already exists in this collection.'],
      ['sources', 'Bad source.'],
      ['webhookUrl', 'This secret belongs to another kind of object.'],
      ['webhookAuth', 'Bad auth secret.'],
      ['ntfyServer', 'Bad server.'],
      ['ntfyTopic', 'Bad topic.'],
      ['ntfyToken', 'Bad ntfy token.'],
      ['pushoverToken', 'Bad app token.'],
      ['pushoverUser', 'Bad user key.'],
      ['telegramToken', 'Bad bot token.'],
      ['telegramChatId', 'Bad chat id.'],
    ]));
  });
});
