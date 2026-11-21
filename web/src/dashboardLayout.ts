import type { DashboardLayout } from './types';

// arrange puts the stored layout over the widgets this version has. A stored name the page does not know is
// dropped; a widget the layout does not name, new since it was saved, joins at the end, shown.
export function arrange(known: string[], { order = [], hidden = [] }: DashboardLayout) {
  const ordered = [...order.filter((id) => known.includes(id)), ...known.filter((id) => !order.includes(id))];
  return { order: ordered, hidden: ordered.filter((id) => hidden.includes(id)) };
}

// moved puts one shown widget where another is. Widgets that are not on the page, hidden or not this account's
// to see, keep their places in the order.
export function moved(order: string[], shown: string[], from: string, to: string) {
  const next = shown.filter((id) => id !== from);
  next.splice(shown.indexOf(to), 0, from);
  return order.map((id) => (shown.includes(id) ? next.shift()! : id));
}
