import { Button, Group, Modal, Stack, Text } from '@mantine/core';

interface UninstallAddonModalProps {
  opened: boolean;
  name: string;
  loading: boolean;
  onClose(): void;
  onConfirm(): void;
}

export function UninstallAddonModal({ opened, name, loading, onClose, onConfirm }: UninstallAddonModalProps) {
  return (
    <Modal
      opened={opened}
      onClose={() => { if (!loading) onClose(); }}
      title={name ? `Uninstall ${name}` : 'Uninstall add-on'}
      centered
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close' }}
    >
      <Stack>
        <Text size="sm">
          Uninstalling removes <strong>{name}</strong> and its saved configuration from the appliance declaration.
        </Text>
        <Text size="sm" c="dimmed">
          The running add-on unit is only stopped and removed when you apply changes in <strong>Review and apply</strong>, protected by Andaru&apos;s automatic rollback timer until confirmed.
        </Text>
        <Group justify="flex-end" gap="xs">
          <Button variant="default" disabled={loading} onClick={onClose}>Cancel</Button>
          <Button color="red" loading={loading} onClick={onConfirm}>Uninstall</Button>
        </Group>
      </Stack>
    </Modal>
  );
}
