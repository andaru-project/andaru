// SPDX-License-Identifier: AGPL-3.0-or-later

export function RoleSvgIcon({ role, size = 18 }: { role?: string; size?: number }) {
  switch (role) {
    case 'firewall':
    case 'gateway':
    case 'router':
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true">
          <path
            d="M12 3l7 3v5c0 4.5-3 8.2-7 10-4-1.8-7-5.5-7-10V6l7-3z"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
            strokeLinejoin="round"
          />
        </svg>
      );
    case 'switch':
    case 'combo':
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true">
          <rect x="3" y="7" width="18" height="10" rx="2" fill="none" stroke="currentColor" strokeWidth="1.8" />
          <path d="M7 12h2M11 12h2M15 12h2" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
        </svg>
      );
    case 'ap':
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true">
          <circle cx="12" cy="14" r="2" fill="currentColor" />
          <path
            d="M8.5 11.5a5 5 0 017 0M5.5 8.5a9.2 9.2 0 0113 0"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
            strokeLinecap="round"
          />
        </svg>
      );
    default:
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true">
          <rect x="4" y="5" width="16" height="11" rx="2" fill="none" stroke="currentColor" strokeWidth="1.8" />
          <path d="M9 19h6" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
        </svg>
      );
  }
}
