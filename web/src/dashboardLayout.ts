import type { DashboardLayout } from './types';

// The stored document may come from another client version or from a hand, so nothing about its shape is taken
// on trust: a list that is not a list of names counts as empty, and a name counts once.
const names = (value: unknown) => (Array.isArray(value) ? [...new Set(value.filter((item): item is string => typeof item === 'string'))] : []);

// arrange puts the stored layout over the widgets this version has. A stored name the page does not know is
// dropped; a widget the layout does not name, new since it was saved, joins at the end, shown.
export function arrange(known: string[], stored: DashboardLayout | undefined): DashboardLayout & { order: string[]; hidden: string[] } {
  const order = names(stored?.order), hidden = names(stored?.hidden);
  const ordered = [...order.filter((id) => known.includes(id)), ...known.filter((id) => !order.includes(id))];
  const arranged = { order: ordered, hidden: ordered.filter((id) => hidden.includes(id)) };
  if (!Array.isArray(stored?.columns)) return arranged;
  // A name counts once across the columns too, in the first column that holds it.
  const seen = new Set<string>();
  const columns = stored.columns.map((column) => names(column).filter((id) => known.includes(id) && !seen.has(id) && seen.add(id)));
  return { ...arranged, columns };
}

// columnsFor lays the shown widgets out in count columns: as they were arranged by hand at that count, and the
// rest (all of them, at any other count) each to the first of the shortest columns, which deals them in turn.
export function columnsFor(shown: string[], count: number, arranged?: string[][]) {
  const columns: string[][] = Array.from({ length: count }, (_, index) => arranged?.length === count ? arranged[index].filter((id) => shown.includes(id)) : []);
  for (const id of shown.filter((item) => !columns.some((column) => column.includes(item)))) {
    columns.reduce((shortest, column) => (column.length < shortest.length ? column : shortest)).push(id);
  }
  return columns;
}

// readAcross is the columns read row by row: the order another column count deals them in, so it starts from
// much the same arrangement.
export function readAcross(columns: string[][]) {
  return Array.from({ length: Math.max(0, ...columns.map((column) => column.length)) }, (_, row) => columns.flatMap((column) => column[row] ?? [])).flat();
}

// dropAt moves one widget to the end of a column, or to where another widget is: after it when moving down its
// own column, before it otherwise, as a sortable list does.
export function dropAt(columns: string[][], id: string, column: number, over?: string) {
  const from = columns[column]?.indexOf(id) ?? -1;
  const to = over === undefined ? -1 : columns[column]?.indexOf(over) ?? -1;
  const next = columns.map((items) => items.filter((item) => item !== id));
  if (!next[column]) return columns;
  const at = to < 0 ? next[column].length : from >= 0 && from < to ? to : next[column].indexOf(over!);
  next[column].splice(at, 0, id);
  return next;
}

// withShown puts the shown widgets in a new order. Widgets that are not on the page, hidden or not this account's
// to see, keep their places in the order.
export function withShown(order: string[], shown: string[], next: string[]) {
  const queue = [...next];
  return order.map((id) => (shown.includes(id) ? queue.shift()! : id));
}
