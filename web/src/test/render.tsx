import { MantineProvider } from '@mantine/core';
import { render as testingRender } from '@testing-library/react';
import type { ReactElement } from 'react';

export * from '@testing-library/react';

export function render(ui: ReactElement) {
  return testingRender(<MantineProvider>{ui}</MantineProvider>);
}
