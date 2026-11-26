import { describe, expect, it } from 'vitest';
import { ApiError } from './api';
import {
  buildChannelWrite,
  channelDraftOf,
  channelFieldErrors,
  channelSecretsOf,
  existingChannelSecretsOf,
  nextChannelSecretName,
  parseEmailAddress,
  sharedSecretHolders,
  validateChannelDraft,
  validateEmailHost,
  validateEmailPort,
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
    // ntfy server & topic validation (proxy paths allowed; credentials, query, fragment, bad hosts/ports rejected).
    expect(validateNtfyServer('')).toMatch(/Enter an ntfy server URL/);
    expect(validateNtfyServer('https://ntfy.sh')).toBeUndefined();
    expect(validateNtfyServer('https://example.org/ntfy')).toBeUndefined();
    expect(validateNtfyServer('https://example.org/ntfy/')).toBeUndefined();
    expect(validateNtfyServer('https://example.org/proxy/%20')).toBeUndefined();
    expect(validateNtfyServer('http://ntfy.internal:8080/sub/path')).toBeUndefined();
    expect(validateNtfyServer('http://[::1]:8080')).toBeUndefined();
    expect(validateNtfyServer('https://ntfy.example/proxy/%zz')).toMatch(/no credentials, query, or fragment/);
    expect(validateNtfyServer('https://ntfy.example/proxy/%2')).toMatch(/no credentials, query, or fragment/);
    expect(validateNtfyServer('https://user:pass@ntfy.sh')).toMatch(/no credentials, query, or fragment/);
    expect(validateNtfyServer('https://ntfy.sh?a=1')).toMatch(/no credentials, query, or fragment/);
    expect(validateNtfyServer('https://ntfy.sh#frag')).toMatch(/no credentials, query, or fragment/);
    expect(validateNtfyServer('https://ntfy.sh:')).toMatch(/no credentials, query, or fragment/);
    expect(validateNtfyServer('https://ntfy.sh:/proxy')).toMatch(/no credentials, query, or fragment/);
    expect(validateNtfyServer('https://ntfy.sh:0')).toMatch(/no credentials, query, or fragment/);
    expect(validateNtfyServer('https://.')).toMatch(/no credentials, query, or fragment/);
    expect(validateNtfyServer('https://..')).toMatch(/no credentials, query, or fragment/);
    expect(validateNtfyServer('https://a..b')).toMatch(/no credentials, query, or fragment/);
    expect(validateNtfyServer('ftp://ntfy.sh')).toMatch(/http:\/\/ or https:\/\//);
    expect(validateNtfyServer('https://ntfy.sh/a\nb')).toMatch(/whitespace or control/i);
    expect(validateNtfyServer(`https://ntfy.sh/${'a'.repeat(2040)}`)).toMatch(/2048 characters/);

    expect(validateNtfyTopic('')).toMatch(/Enter an ntfy topic/);
    expect(validateNtfyTopic('bad/topic')).toMatch(/1 to 64/);
    expect(validateNtfyTopic('a'.repeat(65))).toMatch(/1 to 64/);
    expect(validateNtfyTopic('andaru_alerts-1')).toBeUndefined();

    // Telegram chatId validation.
    expect(validateTelegramChatId('')).toMatch(/Enter a Telegram chat ID/);
    expect(validateTelegramChatId('-')).toMatch(/numeric chat ID/i);
    expect(validateTelegramChatId('@')).toMatch(/numeric chat ID/i);
    expect(validateTelegramChatId('not_a_chat')).toMatch(/numeric chat ID/i);
    expect(validateTelegramChatId('@bad-name')).toMatch(/numeric chat ID/i);
    expect(validateTelegramChatId('1'.repeat(64))).toMatch(/64 characters/);
    expect(validateTelegramChatId(`-${'1'.repeat(64)}`)).toMatch(/64 characters/);
    expect(validateTelegramChatId('-1001234567890')).toBeUndefined();
    expect(validateTelegramChatId('123456789')).toBeUndefined();
    expect(validateTelegramChatId('@ops_alerts')).toBeUndefined();

    // Build ntfy without token, with unresolved token kept, rotated, and removed.
    const ntfyNoToken = buildChannelWrite(
      { ...channelDraftOf(), name: 'Ntfy Ops', kind: 'ntfy', ntfyServer: 'https://example.org/ntfy', ntfyTopic: 'andaru-alerts' },
      undefined,
    );
    expect(ntfyNoToken).toEqual({
      name: 'Ntfy Ops',
      enabled: true,
      kind: 'ntfy',
      filter: { minSeverity: 'info' },
      ntfy: { server: 'https://example.org/ntfy', topic: 'andaru-alerts' },
    });
    expect('webhook' in ntfyNoToken).toBe(false);

    const ntfySaved: NotificationChannel = {
      id: 'ntf_ntfy',
      name: 'Ntfy Ops',
      enabled: true,
      kind: 'ntfy',
      ntfy: { server: 'https://ntfy.sh', topic: 'andaru-alerts', token: { secret: 'NTFY_TOK', isSet: false, version: '0', unresolved: true } },
    };
    const ntfyKeptUnresolved = buildChannelWrite({ ...channelDraftOf(ntfySaved), enabled: false }, ntfySaved);
    expect(ntfyKeptUnresolved.ntfy).toEqual({
      server: 'https://ntfy.sh',
      topic: 'andaru-alerts',
      token: { secret: 'NTFY_TOK', unresolved: true },
    });
    const ntfyRotated = buildChannelWrite({ ...channelDraftOf(ntfySaved), ntfyToken: 'tk_fresh' }, ntfySaved);
    expect(ntfyRotated.ntfy).toEqual({
      server: 'https://ntfy.sh',
      topic: 'andaru-alerts',
      token: { secret: 'NOTIFY_NTFY_OPS_TOKEN', set: 'tk_fresh', expectedVersion: '0' },
    });
    const ntfyRemoved = buildChannelWrite({ ...channelDraftOf(ntfySaved), removeNtfyToken: true }, ntfySaved);
    expect(ntfyRemoved.ntfy).toEqual({ server: 'https://ntfy.sh', topic: 'andaru-alerts' });

    // Build Pushover (both required secrets), keep unchanged, and rotate one secret.
    const pushoverErrors = validateChannelDraft({ ...channelDraftOf(), name: 'PO', kind: 'pushover' }, undefined);
    expect(pushoverErrors.get('pushoverToken')).toBe('Enter an application token.');
    expect(pushoverErrors.get('pushoverUser')).toBe('Enter a user key.');

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
    const pushoverSaved: NotificationChannel = {
      id: 'ntf_po',
      name: 'PO',
      enabled: true,
      kind: 'pushover',
      pushover: {
        token: { secret: 'PO_TOK', isSet: true, version: '2' },
        user: { secret: 'PO_USR', isSet: true, version: '2' },
      },
    };
    const pushoverKept = buildChannelWrite({ ...channelDraftOf(pushoverSaved), enabled: false }, pushoverSaved);
    expect(pushoverKept.pushover).toEqual({
      token: { secret: 'PO_TOK' },
      user: { secret: 'PO_USR' },
    });

    // Build Telegram, verify keep unchanged and kind switching requires new kind's secrets and emits only its sub-object.
    const switchErrors = validateChannelDraft({ ...channelDraftOf(ntfySaved), kind: 'telegram', telegramChatId: '@ops' }, ntfySaved);
    expect(switchErrors.get('telegramToken')).toBe('Enter a bot token.');
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
    const telegramSaved: NotificationChannel = {
      id: 'ntf_tg',
      name: 'TG',
      enabled: true,
      kind: 'telegram',
      telegram: { token: { secret: 'TG_TOK', isSet: true, version: '1' }, chatId: '@ops' },
    };
    expect(buildChannelWrite({ ...channelDraftOf(telegramSaved), telegramChatId: '-1001' }, telegramSaved).telegram).toEqual({
      token: { secret: 'TG_TOK' },
      chatId: '-1001',
    });

    // Unknown future kind (e.g. pagerduty): secrets collected recursively, non-SecretView objects preserved intact,
    // and prototype-property kind names never read Object.prototype.
    const futurePagerDuty: NotificationChannel = {
      id: 'ntf_pd',
      name: 'PD Ops',
      enabled: true,
      kind: 'pagerduty',
      filter: { minSeverity: 'critical' },
      pagerduty: {
        region: 'us',
        routingKey: { secret: 'SHARED_PD_KEY', isSet: true, version: '4', updatedAt: '2026-11-26T00:00:00Z' },
        backupAuth: { secret: 'BACKUP_PASS', isSet: false, version: '0', unresolved: true },
        metadata: { secret: 'not-a-secret-view', version: '1', extra: 'keep-me' },
      },
    };
    const otherSharing: NotificationChannel = {
      id: 'ntf_other',
      name: 'Other',
      enabled: true,
      kind: 'ntfy',
      ntfy: { server: 'https://ntfy.sh', topic: 'x', token: { secret: 'SHARED_PD_KEY', isSet: true, version: '4' } },
    };
    expect(channelSecretsOf(futurePagerDuty).map((e) => e.view.secret)).toEqual(['SHARED_PD_KEY', 'BACKUP_PASS']);
    expect(existingChannelSecretsOf([futurePagerDuty, otherSharing])).toEqual(['SHARED_PD_KEY', 'BACKUP_PASS']);
    expect(sharedSecretHolders('SHARED_PD_KEY', otherSharing.id, [futurePagerDuty, otherSharing])).toEqual(['PD Ops']);

    const disabledFuture = buildChannelWrite({ ...channelDraftOf(futurePagerDuty), enabled: false }, futurePagerDuty);
    expect(disabledFuture).toEqual({
      name: 'PD Ops',
      enabled: false,
      kind: 'pagerduty',
      filter: { minSeverity: 'critical' },
      pagerduty: {
        region: 'us',
        routingKey: { secret: 'SHARED_PD_KEY' },
        backupAuth: { secret: 'BACKUP_PASS', unresolved: true },
        metadata: { secret: 'not-a-secret-view', version: '1', extra: 'keep-me' },
      },
    });
    expect('webhook' in disabledFuture).toBe(false);

    // Hostile prototype-key kind name must not read Object.prototype properties.
    const protoChannel: NotificationChannel = { id: 'ntf_proto', name: 'Proto', enabled: true, kind: 'toString' };
    expect(channelSecretsOf(protoChannel)).toEqual([]);
    const protoWrite = buildChannelWrite(channelDraftOf(protoChannel), protoChannel);
    expect(Object.hasOwn(protoWrite, 'toString')).toBe(false);
  });

  it('validates and builds email (SMTP) channels including auth pairing, hosts, ports, and recipients', () => {
    expect(validateEmailHost('smtp.example.com')).toBeUndefined();
    expect(validateEmailHost('localhost')).toBeUndefined();
    expect(validateEmailHost('192.0.2.10')).toBeUndefined();
    expect(validateEmailHost('2001:db8::1')).toBeUndefined();
    expect(validateEmailHost('::ffff:192.0.2.1')).toBeUndefined();
    expect(validateEmailHost('::1')).toBeUndefined();
    expect(validateEmailHost('')).toMatch(/Enter an SMTP host/);
    expect(validateEmailHost('smtp.example.com.')).toMatch(/host name or IP/i);
    expect(validateEmailHost('12345')).toMatch(/host name or IP/i);
    expect(validateEmailHost('192.168.1.300')).toMatch(/host name or IP/i);
    expect(validateEmailHost('01.2.3.4')).toMatch(/host name or IP/i);
    expect(validateEmailHost('https://smtp.example.com')).toMatch(/no scheme, port, or path/i);
    expect(validateEmailHost('smtp.example.com:465')).toMatch(/no scheme, port, or path/i);
    expect(validateEmailHost('smtp.example.com/path')).toMatch(/no scheme, port, or path/i);
    expect(validateEmailHost('fe80::1%eth0')).toMatch(/host name or IP/i);
    expect(validateEmailHost('[::1]')).toMatch(/host name or IP/i);
    expect(validateEmailHost(`${'a.'.repeat(127)}a`)).toMatch(/253 characters/);

    expect(validateEmailPort('465')).toBeUndefined();
    expect(validateEmailPort('587')).toBeUndefined();
    expect(validateEmailPort('0')).toMatch(/1 to 65535/);
    expect(validateEmailPort('65536')).toMatch(/1 to 65535/);
    expect(validateEmailPort('100000')).toMatch(/1 to 65535/);
    expect(validateEmailPort('465.5')).toMatch(/1 to 65535/);

    expect(parseEmailAddress('ops@example.com')).toEqual({ address: 'ops@example.com' });
    expect(parseEmailAddress('Andaru <Alerts@Example.com>')).toEqual({ address: 'alerts@example.com' });
    expect(parseEmailAddress('"Doe, Jane" <jane@example.com>')).toEqual({ address: 'jane@example.com' });
    expect(parseEmailAddress('"Ops <1>" <ops@example.com>')).toEqual({ address: 'ops@example.com' });
    expect(parseEmailAddress('Ops (On call) <ops@example.com>')).toEqual({ address: 'ops@example.com' });
    expect(parseEmailAddress('ops@example.com (Ops)')).toEqual({ address: 'ops@example.com' });
    expect(parseEmailAddress('Andaru <alerts@example.com> (Primary)')).toEqual({ address: 'alerts@example.com' });
    expect('error' in parseEmailAddress('Ops (unclosed <ops@example.com>')).toBe(true);
    expect('error' in parseEmailAddress('"a b"@example.com')).toBe(true);
    expect('error' in parseEmailAddress('a@b@example.com')).toBe(true);
    expect('error' in parseEmailAddress('a@b.com, Ops <c@d.com>')).toBe(true);
    expect('error' in parseEmailAddress('Bad, Name <a@b.com>')).toBe(true);
    expect('error' in parseEmailAddress('Foo@Bar <a@b.com>')).toBe(true);
    expect('error' in parseEmailAddress('"Unclosed <a@b.com>')).toBe(true);
    expect('error' in parseEmailAddress('Andaru < a@b.com >')).toBe(true);
    expect('error' in parseEmailAddress('.@.')).toBe(true);
    expect('error' in parseEmailAddress('a..b@example.com')).toBe(true);
    expect('error' in parseEmailAddress('a@.example.com')).toBe(true);
    expect('error' in parseEmailAddress('a@example.com.')).toBe(true);
    expect('error' in parseEmailAddress('ops@example.com\r\nBcc: evil@example.com')).toBe(true);

    // Username without password, duplicate recipients (case-insensitive), empty/overflow recipients.
    const badAuth = validateChannelDraft(
      {
        ...channelDraftOf(),
        name: 'SMTP',
        kind: 'email',
        emailHost: 'smtp.example.com',
        emailPort: '587',
        emailSecurity: 'starttls',
        emailUsername: 'alerts@example.com',
        emailFrom: 'Andaru <alerts@example.com>',
        emailTo: ['Ops <OPS@example.com>', 'ops@example.com'],
      },
      undefined,
    );
    expect(badAuth.get('emailPassword')).toMatch(/username requires a password/i);
    expect(badAuth.get('emailTo')).toMatch(/unique/i);

    const emptyAndOverflow = validateChannelDraft(
      { ...channelDraftOf(), name: 'SMTP', kind: 'email', emailHost: 'smtp.example.com', emailPort: '465', emailFrom: 'a@b.com', emailTo: [] },
      undefined,
    );
    expect(emptyAndOverflow.get('emailTo')).toMatch(/at least one recipient/i);
    const tooMany = validateChannelDraft(
      {
        ...channelDraftOf(),
        name: 'SMTP',
        kind: 'email',
        emailHost: 'smtp.example.com',
        emailPort: '465',
        emailFrom: 'a@b.com',
        emailTo: Array.from({ length: 21 }, (_, i) => `u${i}@example.com`),
      },
      undefined,
    );
    expect(tooMany.get('emailTo')).toMatch(/At most 20/);

    const passwordOnly = validateChannelDraft(
      {
        ...channelDraftOf(),
        name: 'SMTP',
        kind: 'email',
        emailHost: 'smtp.example.com',
        emailPort: '465',
        emailPassword: 'secret',
        emailFrom: 'alerts@example.com',
        emailTo: ['ops@example.com'],
      },
      undefined,
    );
    expect(passwordOnly.get('emailUsername')).toMatch(/password requires a username/i);

    // Unauthenticated build omits username and password.
    const unauthWrite = buildChannelWrite(
      {
        ...channelDraftOf(),
        name: 'SMTP Relay',
        kind: 'email',
        emailHost: 'relay.internal',
        emailPort: '587',
        emailSecurity: 'starttls',
        emailFrom: 'Andaru <alerts@example.com>',
        emailTo: ['ops@example.com', 'oncall@example.com'],
      },
      undefined,
    );
    expect(unauthWrite).toEqual({
      name: 'SMTP Relay',
      enabled: true,
      kind: 'email',
      filter: { minSeverity: 'info' },
      email: {
        host: 'relay.internal',
        port: 587,
        security: 'starttls',
        from: 'Andaru <alerts@example.com>',
        to: ['ops@example.com', 'oncall@example.com'],
      },
    });

    // Authenticated build preserves leading/trailing whitespace in SMTP passwords and allocates fresh PASSWORD secret.
    const authWrite = buildChannelWrite(
      {
        ...channelDraftOf(),
        name: 'SMTP Auth',
        kind: 'email',
        emailHost: 'smtp.example.com',
        emailPort: '465',
        emailSecurity: 'tls',
        emailUsername: 'alerts@example.com',
        emailPassword: ' hunter2 ',
        emailFrom: 'Andaru <alerts@example.com>',
        emailTo: ['ops@example.com'],
      },
      undefined,
    );
    expect(authWrite.email).toEqual({
      host: 'smtp.example.com',
      port: 465,
      security: 'tls',
      username: 'alerts@example.com',
      password: { secret: 'NOTIFY_SMTP_AUTH_PASSWORD', set: ' hunter2 ', expectedVersion: '0' },
      from: 'Andaru <alerts@example.com>',
      to: ['ops@example.com'],
    });

    const savedEmail: NotificationChannel = {
      id: 'ntf_mail',
      name: 'SMTP Auth',
      enabled: true,
      kind: 'email',
      email: {
        host: 'smtp.example.com',
        port: 465,
        security: 'tls',
        username: 'alerts@example.com',
        password: { secret: 'SMTP_PW', isSet: false, version: '0', unresolved: true },
        from: 'Andaru <alerts@example.com>',
        to: ['ops@example.com'],
      },
    };
    expect(channelSecretsOf(savedEmail)).toEqual([
      { label: 'Password', view: { secret: 'SMTP_PW', isSet: false, version: '0', unresolved: true }, optional: true },
    ]);
    // Clearing username without checking removeEmailPassword flags username with clear guidance.
    const clearedUserOnly = validateChannelDraft({ ...channelDraftOf(savedEmail), emailUsername: '' }, savedEmail);
    expect(clearedUserOnly.get('emailUsername')).toMatch(/Remove password/i);
    // Checking removeEmailPassword while username is still set flags the focusable emailUsername field, not disabled password.
    const removedPwOnly = validateChannelDraft({ ...channelDraftOf(savedEmail), removeEmailPassword: true }, savedEmail);
    expect(removedPwOnly.get('emailUsername')).toMatch(/Clear the username/i);
    expect(removedPwOnly.has('emailPassword')).toBe(false);
    // Keeping existing unresolved password echoes unresolved: true; rotating allocates fresh expectedVersion '0'.
    expect(buildChannelWrite({ ...channelDraftOf(savedEmail), enabled: false }, savedEmail).email?.password).toEqual({
      secret: 'SMTP_PW',
      unresolved: true,
    });
    expect(buildChannelWrite({ ...channelDraftOf(savedEmail), emailPassword: 'new-pw' }, savedEmail).email?.password).toEqual({
      secret: 'NOTIFY_SMTP_AUTH_PASSWORD',
      set: 'new-pw',
      expectedVersion: '0',
    });
    // Switching from email to webhook drops email sub-object.
    const switchedToWebhook = buildChannelWrite({ ...channelDraftOf(savedEmail), kind: 'webhook', webhookUrl: 'https://hooks.example/x' }, savedEmail);
    expect('email' in switchedToWebhook).toBe(false);
    expect(switchedToWebhook.webhook?.url.set).toBe('https://hooks.example/x');
    // Clearing username AND checking removeEmailPassword strips both credentials.
    const strippedAuth = buildChannelWrite(
      { ...channelDraftOf(savedEmail), emailUsername: '', removeEmailPassword: true },
      savedEmail,
    );
    expect(strippedAuth.email?.username).toBeUndefined();
    expect(strippedAuth.email?.password).toBeUndefined();
  });

  it('maps 422 validation pointer paths (direct and collection-prefixed) to form fields', () => {
    const err = new ApiError(422, 'VALIDATION_FAILED', 'Invalid.', [
      { path: '/name', message: 'Name already exists in this collection.' },
      { path: '/notifications/channels/0/filter/sources/0', message: 'Bad source.' },
      { path: '/webhook/url', message: 'This secret belongs to another kind of object.' },
      { path: '/webhook/authorization', message: 'Bad auth secret.' },
      { path: '/notifications/channels/1/ntfy/server', message: 'Bad server.' },
      { path: '/ntfy/topic', message: 'Bad topic.' },
      { path: '/ntfy/token/set', message: 'Bad ntfy token.' },
      { path: '/notifications/channels/2/pushover/token/set', message: 'Bad app token.' },
      { path: '/pushover/user', message: 'Bad user key.' },
      { path: '/telegram/token/secret', message: 'Bad bot token.' },
      { path: '/notifications/channels/3/telegram/chatId', message: 'Bad chat id.' },
      { path: '/email/host', message: 'Bad host.' },
      { path: '/email/port', message: 'Bad port.' },
      { path: '/email/security', message: 'Bad security.' },
      { path: '/email/username', message: 'Bad user.' },
      { path: '/notifications/channels/4/email/password/set', message: 'Bad pass.' },
      { path: '/email/from', message: 'Bad from.' },
      { path: '/notifications/channels/4/email/to/0', message: 'Bad recipient.' },
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
      ['emailHost', 'Bad host.'],
      ['emailPort', 'Bad port.'],
      ['emailSecurity', 'Bad security.'],
      ['emailUsername', 'Bad user.'],
      ['emailPassword', 'Bad pass.'],
      ['emailFrom', 'Bad from.'],
      ['emailTo', 'Bad recipient.'],
    ]));
  });
});
