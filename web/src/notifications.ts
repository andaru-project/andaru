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

// isSafeAppLink allows only in-app paths that a browser cannot read as another site's. The page's own origin must
// come back from the URL parser itself: it drops tabs and new lines and reads a backslash as a slash, so `/\t/host`
// and `/\host` are `//host`. One it cannot parse is no link.
export function isSafeAppLink(link: string | undefined): link is string {
  if (typeof link !== 'string' || !link.startsWith('/')) return false;
  try {
    return new URL(link, window.location.origin).origin === window.location.origin;
  } catch {
    return false;
  }
}
