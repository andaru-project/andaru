import { MantineProvider } from '@mantine/core';
import { render as testingRender } from '@testing-library/react';
import type { ReactElement } from 'react';

export * from '@testing-library/react';

// The provider is a wrapper, so rerender keeps it.
export function render(ui: ReactElement) {
  return testingRender(ui, { wrapper: MantineProvider });
}
