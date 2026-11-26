import { MantineProvider } from '@mantine/core';
import { render as testingRender } from '@testing-library/react';
import type { ReactElement, ReactNode } from 'react';

export * from '@testing-library/react';

// Mantine's test environment: no open and close transitions, so a menu or panel is there or not, never part-way,
// however loaded the machine running the tests. The provider is a wrapper, so rerender keeps it.
function TestProvider({ children }: { children: ReactNode }) {
  return <MantineProvider env="test">{children}</MantineProvider>;
}

export function render(ui: ReactElement) {
  return testingRender(ui, { wrapper: TestProvider });
}
