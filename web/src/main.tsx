import '@mantine/core/styles.css';
import './theme.css';
import './styles.css';
import { MantineProvider } from '@mantine/core';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { theme } from './theme';

const element = document.getElementById('root');
if (!element) throw new Error('Application root is missing.');

createRoot(element).render(<StrictMode><MantineProvider defaultColorScheme="auto" theme={theme} withCssVariables={false}><App /></MantineProvider></StrictMode>);
