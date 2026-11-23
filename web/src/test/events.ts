// FakeEventSource stands in for the browser's EventSource, which jsdom lacks. Tests drive the streams it opened.
export class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  static opened: FakeEventSource[] = [];
  readyState = FakeEventSource.CONNECTING;
  onopen: (() => void) | null = null;
  onmessage: (() => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(readonly url: string) {
    FakeEventSource.opened.push(this);
  }

  open() {
    this.readyState = FakeEventSource.OPEN;
    this.onopen?.();
  }

  message() {
    this.onmessage?.();
  }

  // fail is a dropped stream the browser reopens itself, or with closed a refusal it leaves to the page.
  fail(closed: boolean) {
    this.readyState = closed ? FakeEventSource.CLOSED : FakeEventSource.CONNECTING;
    this.onerror?.();
  }

  close() {
    this.readyState = FakeEventSource.CLOSED;
  }
}

// openStreams are the streams a page holds now.
export const openStreams = () => FakeEventSource.opened.filter((source) => source.readyState !== FakeEventSource.CLOSED);
