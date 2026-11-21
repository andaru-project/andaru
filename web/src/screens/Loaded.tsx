import { Loader, Stack, Text } from '@mantine/core';
import type { ReactNode } from 'react';
import type { Resource } from '../useResource';

// Loaded shows one widget's own loading and failure, so a widget that fails never blanks the others.
export function Loaded<T>({ resource, what, children }: { resource: Resource<T>; what: string; children(data: T): ReactNode }) {
  return (
    <Stack className="widget-body" gap="xs">
      {resource.error && <Text className="error-text" size="sm" role="alert">{resource.error}</Text>}
      {resource.data !== undefined ? children(resource.data) : !resource.error && <Loader size="sm" aria-label={`Loading ${what}`} />}
    </Stack>
  );
}
