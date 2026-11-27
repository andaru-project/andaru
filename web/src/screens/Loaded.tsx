import { Loader, Stack, Text } from '@mantine/core';
import type { ReactNode } from 'react';
import { useConnected, type Resource } from '../useResource';

// Loaded shows one widget's own loading and failure, so a widget that fails never blanks the others.
// When the shell is already disconnected and last-known data is on screen, the header indicator owns
// the offline state instead of repeating the same network error on every section.
export function Loaded<T>({ resource, what, children }: { resource: Resource<T>; what: string; children(data: T): ReactNode }) {
  const connected = useConnected();
  const showError = Boolean(resource.error) && (connected || resource.data === undefined);
  return (
    <Stack gap="xs">
      {showError && <Text className="error-text" size="sm" role="alert">{resource.error}</Text>}
      {resource.data !== undefined ? children(resource.data) : !showError && <Loader size="sm" aria-label={`Loading ${what}`} />}
    </Stack>
  );
}
