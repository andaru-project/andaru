import '@mantine/core/styles.css';
import './styles.css';
import { MantineProvider, createTheme } from '@mantine/core';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';

const element = document.getElementById('root');
if (!element) throw new Error('Application root is missing.');

const theme = createTheme({ primaryColor: 'teal', defaultRadius: 'sm', fontFamily: 'system-ui, sans-serif' });
createRoot(element).render(<StrictMode><MantineProvider defaultColorScheme="auto" theme={theme}><App /></MantineProvider></StrictMode>);
