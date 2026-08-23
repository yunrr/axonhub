import { useMemo, useState } from 'react';
import { IconLoader } from '@tabler/icons-react';
import { Check, ChevronDown } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from '@/components/ui/command';
import { FormControl } from '@/components/ui/form';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';

interface SelectDropdownProps {
  onValueChange?: (value: string) => void;
  defaultValue: string | undefined;
  placeholder?: string;
  isPending?: boolean;
  items: { label: string; value: string; disabled?: boolean }[] | undefined;
  disabled?: boolean;
  className?: string;
  isControlled?: boolean;
  'data-testid'?: string;
}

export function SelectDropdown({
  defaultValue,
  onValueChange,
  isPending,
  items,
  placeholder,
  disabled,
  className = '',
  isControlled = false,
  'data-testid': dataTestId,
}: SelectDropdownProps) {
  const defaultState = isControlled ? { value: defaultValue, onValueChange } : { defaultValue, onValueChange };
  return (
    <Select {...defaultState}>
      <FormControl>
        <SelectTrigger disabled={disabled} className={cn(className)} data-testid={dataTestId}>
          <SelectValue placeholder={placeholder ?? 'Select'} />
        </SelectTrigger>
      </FormControl>
      <SelectContent>
        {isPending ? (
          <SelectItem disabled value='loading' className='h-14'>
            <div className='flex items-center justify-center gap-2'>
              <IconLoader className='h-5 w-5 animate-spin' />
              {'  '}
              Loading...
            </div>
          </SelectItem>
        ) : (
          items?.map(({ label, value, disabled }) => (
            <SelectItem key={value} value={value} disabled={disabled}>
              {label}
            </SelectItem>
          ))
        )}
      </SelectContent>
    </Select>
  );
}

interface SearchableSelectDropdownProps extends SelectDropdownProps {
  searchPlaceholder?: string;
  emptyMessage?: string;
}

export function SearchableSelectDropdown({
  defaultValue,
  onValueChange,
  isPending,
  items,
  placeholder,
  searchPlaceholder = 'Search...',
  emptyMessage = 'No items found.',
  disabled,
  className = '',
  isControlled = false,
  'data-testid': dataTestId,
}: SearchableSelectDropdownProps) {
  const [open, setOpen] = useState(false);
  const [searchValue, setSearchValue] = useState('');
  const [uncontrolledValue, setUncontrolledValue] = useState(defaultValue);

  const selectedValue = isControlled ? defaultValue : uncontrolledValue;
  const selectedLabel = items?.find((item) => item.value === selectedValue)?.label;
  const filteredItems = useMemo(() => {
    const query = searchValue.trim().toLowerCase();
    if (!query) return items ?? [];
    return (items ?? []).filter(({ label, value }) => `${label} ${value}`.toLowerCase().includes(query));
  }, [items, searchValue]);

  const handleOpenChange = (nextOpen: boolean) => {
    setOpen(nextOpen);
    if (!nextOpen) setSearchValue('');
  };

  const handleValueChange = (value: string) => {
    if (!isControlled) setUncontrolledValue(value);
    onValueChange?.(value);
    setOpen(false);
    setSearchValue('');
  };

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger asChild>
        <FormControl>
          <button
            type='button'
            role='combobox'
            aria-expanded={open}
            disabled={disabled}
            data-testid={dataTestId}
            className={cn(
              'border-input focus-visible:border-ring focus-visible:ring-ring/50 flex h-9 w-full items-center justify-between gap-2 rounded-md border bg-transparent px-3 py-2 text-left text-sm shadow-xs outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-50',
              !selectedLabel && 'text-muted-foreground',
              className
            )}
          >
            <span className='min-w-0 truncate'>{selectedLabel || placeholder || 'Select'}</span>
            <ChevronDown className='text-muted-foreground h-4 w-4 shrink-0 opacity-50' />
          </button>
        </FormControl>
      </PopoverTrigger>
      <PopoverContent align='start' className='w-[var(--radix-popover-trigger-width)] p-0'>
        <Command shouldFilter={false}>
          <CommandInput autoFocus placeholder={searchPlaceholder} value={searchValue} onValueChange={setSearchValue} />
          <CommandList>
            {isPending ? (
              <CommandItem disabled value='loading' className='justify-center'>
                <IconLoader className='h-4 w-4 animate-spin' />
                Loading...
              </CommandItem>
            ) : (
              <>
                {filteredItems.length > 0 && (
                  <CommandGroup>
                    {filteredItems.map(({ label, value, disabled: itemDisabled }) => (
                      <CommandItem key={value} value={value} disabled={itemDisabled} onSelect={() => handleValueChange(value)}>
                        <Check className={cn('mr-2 h-4 w-4', selectedValue === value ? 'opacity-100' : 'opacity-0')} />
                        <span className='min-w-0 truncate'>{label}</span>
                      </CommandItem>
                    ))}
                  </CommandGroup>
                )}
                <CommandEmpty>{emptyMessage}</CommandEmpty>
              </>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
