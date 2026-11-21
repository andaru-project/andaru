import { Text } from '@mantine/core';
import { Fragment } from 'react';

// An address or prefix in monospace that breaks, when it must, only after a colon or before its length: never
// in the middle of a group, where a broken line reads as two addresses or is copied wrong.
export function Address({ value, size = 'sm' }: { value: string; size?: 'xs' | 'sm' }) {
  const [address, length] = value.split('/');
  const groups = address.split(':');
  return (
    <Text className="address" ff="monospace" size={size}>
      {groups.map((group, index) => <Fragment key={index}>{group}{index < groups.length - 1 && <>:<wbr /></>}</Fragment>)}
      {length !== undefined && <><wbr />/{length}</>}
    </Text>
  );
}
