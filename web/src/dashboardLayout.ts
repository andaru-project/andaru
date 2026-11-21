import { arrayMove } from '@dnd-kit/sortable';
import type { DashboardLayout } from './types';

// The stored document may come from another client version or from a hand, so nothing about its shape is taken
// on trust: a list that is not a list of names counts as empty, and a name counts once.
const names = (value: unknown) => (Array.isArray(value) ? [...new Set(value.filter((item): item is string => typeof item === 'string'))] : []);

// arrange puts the stored layout over the widgets this version has. A stored name the page does not know is
// dropped; a widget the layout does not name, new since it was saved, joins at the end, shown.
export function arrange(known: string[], stored: DashboardLayout | undefined) {
  const order = names(stored?.order), hidden = names(stored?.hidden);
  const ordered = [...order.filter((id) => known.includes(id)), ...known.filter((id) => !order.includes(id))];
  return { order: ordered, hidden: ordered.filter((id) => hidden.includes(id)) };
}

// moved puts one shown widget where another is. Widgets that are not on the page, hidden or not this account's
// to see, keep their places in the order.
export function moved(order: string[], shown: string[], from: string, to: string) {
  if (!shown.includes(from) || !shown.includes(to)) return order;
  const next = arrayMove(shown, shown.indexOf(from), shown.indexOf(to));
  return order.map((id) => (shown.includes(id) ? next.shift()! : id));
}
