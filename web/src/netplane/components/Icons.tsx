// SPDX-License-Identifier: AGPL-3.0-or-later

export function AndaruMark({ size = 28 }: { size?: number }) {
  return (
    <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1254 1254" width={size} height={size} aria-hidden="true">
      <path
        fill="#ffb21c"
        transform="translate(0 1254) scale(.1 -.1)"
        d="M6040 11092c0-5 22-56 49-113 126-270 181-517 181-810 0-240-42-466-131-709-132-364-330-672-870-1360-709-903-1017-1494-1113-2136-60-399-9-766 154-1099 97-200 206-349 400-549 194-199 333-314 590-487l95-64-175-3-175-3-156-122c-251-197-491-386-811-640-165-131-328-252-361-268-70-36-168-59-253-59-61 0-62 0-68 31-10 54-7 3451 4 3603 23 314 116 581 287 822 75 105 249 278 833 828 157 148 327 310 378 360l92 91v537c0 307-4 539-9 542-5 3-78-51-162-120-342-280-420-344-550-449-75-60-194-157-265-216-199-162-519-421-859-694-245-196-328-274-435-406-265-329-411-728-450-1223-6-80-10-797-10-1891v-1763l-157-149c-198-186-412-386-663-618-257-238-370-346-370-356 0-4 615-6 1367-4l1368 3 60 22c57 21 84 46 525 489 1091 1093 1600 1608 1600 1618 0 6-33 47-73 91-159 173-343 448-422 632-124 285-173 529-162 799 11 281 75 495 242 817 135 260 269 464 622 947 92 126 186 255 251 345 511 710 680 1172 651 1777-10 212-30 348-79 541-118 463-358 887-675 1192-131 127-295 251-295 224zM7556 9483c-3-3-6-250-5-547l1-541 101-95c326-304 901-853 1017-969 240-242 363-464 442-801l22-95 3-1884 3-1884-97 6c-149 8-231 42-361 150-28 23-105 85-173 137-67 52-170 133-230 180-59 47-154 121-210 165-56 44-206 163-333 265l-231 185-205 5-204 5 119 58c489 238 823 535 981 872 65 140 85 226 91 390 6 164-5 253-49 380-81 241-188 405-509 791-255 306-368 463-449 624-84 166-127 318-138 479-4 57-12 96-18 98-16 5-131-150-182-245-102-192-154-444-133-655 17-171 71-345 155-502 24-45 91-155 149-245 195-307 254-452 264-656 10-195-26-353-128-556-107-214-235-376-574-728-56-58-107-113-115-123-15-20-82 49 1218-1263 877-886 856-867 978-883 48-6 1153-7 2524-2l205 1-85 83c-47 45-211 199-365 342-292 272-561 522-680 634l-70 66-5 1885c-5 1704-7 1894-23 1980-100 570-327 982-712 1292-38 31-110 90-160 130-49 41-193 157-320 259-200 160-327 263-695 564-183 150-337 274-400 325-38 30-140 114-227 186-176 145-178 146-187 137z"
      />
    </svg>
  );
}

const iconProps = {
  width: 16,
  height: 16,
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.8,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
  'aria-hidden': true,
};

export function IconOverview() {
  return (
    <svg {...iconProps}>
      <rect x="3" y="3" width="7" height="9" rx="1" />
      <rect x="14" y="3" width="7" height="5" rx="1" />
      <rect x="14" y="12" width="7" height="9" rx="1" />
      <rect x="3" y="16" width="7" height="5" rx="1" />
    </svg>
  );
}

export function IconTopology() {
  return (
    <svg {...iconProps}>
      <circle cx="12" cy="5" r="2.5" />
      <circle cx="5" cy="19" r="2.5" />
      <circle cx="19" cy="19" r="2.5" />
      <path d="M12 7.5V12m0 0L6.5 17M12 12l5.5 5" />
    </svg>
  );
}

export function IconVlanFlow() {
  return (
    <svg {...iconProps}>
      <path d="M4 6h5c3 0 3 6 6 6h5" />
      <path d="M4 18h5c3 0 3-6 6-6h5" />
      <circle cx="4" cy="6" r="1.5" fill="currentColor" />
      <circle cx="4" cy="18" r="1.5" fill="currentColor" />
      <circle cx="20" cy="12" r="1.5" fill="currentColor" />
    </svg>
  );
}

export function IconDevices() {
  return (
    <svg {...iconProps}>
      <rect x="3" y="5" width="18" height="6" rx="1.5" />
      <rect x="3" y="13" width="18" height="6" rx="1.5" />
      <circle cx="7" cy="8" r="0.8" fill="currentColor" />
      <circle cx="7" cy="16" r="0.8" fill="currentColor" />
    </svg>
  );
}

export function IconVlans() {
  return (
    <svg {...iconProps}>
      <path d="M12 3l8 4.5-8 4.5-8-4.5L12 3z" />
      <path d="M4 12l8 4.5 8-4.5" />
      <path d="M4 16.5L12 21l8-4.5" />
    </svg>
  );
}

export function IconClients() {
  return (
    <svg {...iconProps}>
      <rect x="3" y="4" width="18" height="12" rx="2" />
      <path d="M8 20h8M12 16v4" />
    </svg>
  );
}

export function IconDiagnostics() {
  return (
    <svg {...iconProps}>
      <polyline points="4 17 10 11 4 5" />
      <line x1="12" y1="19" x2="20" y2="19" />
    </svg>
  );
}

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
