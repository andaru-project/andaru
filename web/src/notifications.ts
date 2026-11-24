import type { Notification, NotificationSeverity } from './types';

const severityRank: Record<NotificationSeverity, number> = {
  info: 1,
  warning: 2,
  critical: 3,
};

export const severityColor: Record<NotificationSeverity, string> = {
  info: 'blue',
  warning: 'orange',
  critical: 'red',
};

// highestSeverity returns the most severe level among the active notifications, or undefined when empty.
export function highestSeverity(items: readonly Notification[]): NotificationSeverity | undefined {
  let highest: NotificationSeverity | undefined;
  for (const item of items) {
    if (!highest || severityRank[item.severity] > severityRank[highest]) highest = item.severity;
  }
  return highest;
}

// isSafeAppLink allows only in-app paths that a browser cannot interpret as another origin.
export function isSafeAppLink(link: string | undefined): link is string {
  return typeof link === 'string' && link.startsWith('/') && !link.startsWith('//') && !link.startsWith('/\\');
}
