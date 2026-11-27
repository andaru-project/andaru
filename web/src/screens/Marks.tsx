// A pencil to start arranging, a tick to finish: the button's words say the same.
export function EditMark({ editing }: { editing: boolean }) {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={editing ? 'M3 8.5l3.5 3.5L13 4.5' : 'M2.5 13.5l.7-3L10.5 3.2a1.4 1.4 0 0 1 2 0l.3.3a1.4 1.4 0 0 1 0 2L5.5 12.8z'} />
    </svg>
  );
}

// Dots in two columns: the sign for "drag here".
export function Grip() {
  return (
    <svg width="12" height="18" viewBox="0 0 12 18" fill="currentColor" aria-hidden="true">
      {[3, 9, 15].flatMap((y) => [3, 9].map((x) => <circle key={`${x} ${y}`} cx={x} cy={y} r="1.6" />))}
    </svg>
  );
}

// Bell mark for the header notification count.
export function Bell() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12.5 11H3.5c.8-.9 1.2-2 1.2-3.5a3.3 3.3 0 0 1 6.6 0c0 1.5.4 2.6 1.2 3.5Z" />
      <path d="M6.5 13a1.6 1.6 0 0 0 3 0" />
    </svg>
  );
}

// Person silhouette for the header account menu.
export function UserMark() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="8" cy="5.2" r="2.7" />
      <path d="M2.8 13.5a5.2 5.2 0 0 1 10.4 0" />
    </svg>
  );
}
