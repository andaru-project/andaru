// SPDX-License-Identifier: AGPL-3.0-or-later
import '@mantine/core/styles.css';
import './theme.css';
import './styles.css';
import './netplane/styles.css';
import { MantineProvider } from '@mantine/core';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router';
import { App } from './netplane/App';
import { pinAppRoot } from './netplane/routing';
import { theme } from './theme';

const rootEl = document.getElementById('root');
if (!rootEl) throw new Error('Application root is missing.');

createRoot(rootEl).render(
  <StrictMode>
    <MantineProvider defaultColorScheme="auto" theme={theme} withCssVariables={false}>
      <BrowserRouter basename={pinAppRoot()}>
        <App />
      </BrowserRouter>
    </MantineProvider>
  </StrictMode>,
);
