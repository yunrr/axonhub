'use client';

import { IconTrash } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { useApiKeysContext } from '../context/apikeys-context';
import { useBulkDeleteApiKeys } from '../data/apikeys';

export function ApiKeysBulkDeleteDialog() {
  const { t } = useTranslation();
  const { isDialogOpen, closeDialog, selectedApiKeys, resetRowSelection, setSelectedApiKeys } = useApiKeysContext();
  const bulkDeleteApiKeys = useBulkDeleteApiKeys();

  if (!selectedApiKeys || selectedApiKeys.length === 0) return null;

  const handleBulkDelete = async () => {
    try {
      const ids = selectedApiKeys.map((apiKey) => apiKey.id);
      await bulkDeleteApiKeys.mutateAsync(ids);
      resetRowSelection();
      setSelectedApiKeys([]);
      closeDialog();
    } catch (error) {
    }
  };

  return (
    <ConfirmDialog
      open={isDialogOpen.bulkDelete}
      onOpenChange={() => closeDialog('bulkDelete')}
      handleConfirm={handleBulkDelete}
      disabled={bulkDeleteApiKeys.isPending}
      title={
        <span className='text-red-600'>
          <IconTrash className='mr-1 inline-block stroke-red-600' size={18} />
          {t('apikeys.dialogs.bulkDelete.title')}
        </span>
      }
      desc={
        <div className='space-y-3'>
          <p>{t('apikeys.dialogs.bulkDelete.description', { count: selectedApiKeys.length })}</p>
          <div className='rounded-md border border-red-200 bg-red-50 p-3 dark:border-red-800 dark:bg-red-900/20'>
            <p className='text-sm text-red-800 dark:text-red-200'>{t('apikeys.dialogs.bulkDelete.warning')}</p>
          </div>
        </div>
      }
      confirmText={t('common.buttons.delete')}
      cancelBtnText={t('common.buttons.cancel')}
      destructive
    />
  );
}
