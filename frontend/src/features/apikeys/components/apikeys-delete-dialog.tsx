'use client';

import { IconTrash } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { useApiKeysContext } from '../context/apikeys-context';
import { useDeleteApiKey } from '../data/apikeys';

export function ApiKeysDeleteDialog() {
  const { t } = useTranslation();
  const { isDialogOpen, closeDialog, selectedApiKey, resetRowSelection } = useApiKeysContext();
  const deleteApiKey = useDeleteApiKey();

  if (!selectedApiKey) return null;

  const handleDelete = async () => {
    try {
      await deleteApiKey.mutateAsync(selectedApiKey.id);
      closeDialog('delete');
      resetRowSelection();
    } catch (_error) {
      // Error will be handled by the mutation's error state
    }
  };

  return (
    <ConfirmDialog
      open={isDialogOpen.delete}
      onOpenChange={() => closeDialog('delete')}
      handleConfirm={handleDelete}
      disabled={deleteApiKey.isPending}
      title={
        <span className='text-red-600'>
          <IconTrash className='mr-1 inline-block stroke-red-600' size={18} />
          {t('apikeys.dialogs.delete.title')}
        </span>
      }
      desc={
        <div className='space-y-3'>
          <p>{t('apikeys.dialogs.delete.description', { name: selectedApiKey.name })}</p>
          <div className='rounded-md border border-red-200 bg-red-50 p-3 dark:border-red-800 dark:bg-red-900/20'>
            <p className='text-sm text-red-800 dark:text-red-200'>{t('apikeys.dialogs.delete.warning')}</p>
          </div>
        </div>
      }
      confirmText={t('common.buttons.delete')}
      cancelBtnText={t('common.buttons.cancel')}
      destructive
    />
  );
}
