import { Loader, MultiSelect, Select, Text, type ComboboxItem, type ComboboxParsedItem, type OptionsFilter } from '@mantine/core';
import { useEffect, useMemo, useRef, useState } from 'react';
import { ApiClient, ApiError } from '../api';
import type { AliasKinds, AliasSearchItem, TextSegment } from '../types';

export interface AliasOption {
  value: string;
  label: string;
}

interface AliasSearchBaseProps {
  api: ApiClient;
  aliasType: keyof AliasKinds;
  kind?: string;
  family?: 'ipv4' | 'ipv6';
  excludeGroups?: boolean;
  exclude?: string;
  initialOptions: AliasOption[];
  label: string;
  description?: string;
  placeholder?: string;
  error?: string;
  clearable?: boolean;
  nothingFoundMessage?: string;
  debounceMs?: number;
  onSessionLost?: () => void;
}

export interface AliasMultiSelectProps extends AliasSearchBaseProps {
  value: string[];
  onChange(value: string[]): void;
}

export interface AliasSelectProps extends AliasSearchBaseProps {
  value: string | null;
  onChange(value: string | null): void;
}

function truncateUtf8(text: string, maxBytes: number): string {
  const encoder = new TextEncoder();
  if (encoder.encode(text).length <= maxBytes) return text;
  let out = '';
  let bytes = 0;
  for (const ch of text) {
    const len = encoder.encode(ch).length;
    if (bytes + len > maxBytes) break;
    out += ch;
    bytes += len;
  }
  return out;
}

function Highlighted({ segments }: { segments: TextSegment[] }) {
  return (
    <>
      {segments.map((seg, i) => (
        seg.match ? <strong key={i}>{seg.text}</strong> : <span key={i}>{seg.text}</span>
      ))}
    </>
  );
}

function useAliasSearch({
  api,
  aliasType,
  kind,
  family,
  excludeGroups,
  exclude,
  initialOptions,
  selected,
  singleSelected,
  nothingFoundMessage,
  debounceMs = 150,
  onSessionLost,
}: {
  api: ApiClient;
  aliasType: keyof AliasKinds;
  kind?: string;
  family?: 'ipv4' | 'ipv6';
  excludeGroups?: boolean;
  exclude?: string;
  initialOptions: AliasOption[];
  selected: string[];
  singleSelected?: string;
  nothingFoundMessage: string;
  debounceMs?: number;
  onSessionLost?: () => void;
}) {
  const scopeKey = `${aliasType}|${kind ?? ''}|${family ?? ''}|${excludeGroups ? '1' : '0'}|${exclude ?? ''}`;
  const [seenScope, setSeenScope] = useState(scopeKey);
  const [knownLabels, setKnownLabels] = useState<Record<string, string>>({});
  const singleSelectedLabel = useMemo(() => {
    if (!singleSelected) return undefined;
    return initialOptions.find((o) => o.value === singleSelected)?.label ?? knownLabels[singleSelected] ?? `${singleSelected} (missing)`;
  }, [singleSelected, initialOptions, knownLabels]);
  const singleKey = `${singleSelected ?? ''}|${singleSelectedLabel ?? ''}`;
  const [seenSingleKey, setSeenSingleKey] = useState(singleKey);
  const [searchValue, setSearchValue] = useState(singleSelectedLabel ?? '');
  const [remote, setRemote] = useState<{ query: string; items: AliasSearchItem[] } | null>(null);
  const [loading, setLoading] = useState(false);
  const [searchError, setSearchError] = useState('');

  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const abortRef = useRef<AbortController | undefined>(undefined);
  const seqRef = useRef(0);
  const mountedRef = useRef(true);

  if (scopeKey !== seenScope) {
    setSeenScope(scopeKey);
    setSeenSingleKey(singleKey);
    setRemote(null);
    setLoading(false);
    setSearchError('');
    setSearchValue(singleSelectedLabel ?? '');
  } else if (singleKey !== seenSingleKey) {
    setSeenSingleKey(singleKey);
    setRemote(null);
    setLoading(false);
    setSearchError('');
    setSearchValue(singleSelectedLabel ?? '');
  }

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      clearTimeout(timerRef.current);
      abortRef.current?.abort();
    };
  }, []);

  // Cancel pending work whenever search scope changes (e.g. switching IPv4/IPv6 family).
  useEffect(() => {
    clearTimeout(timerRef.current);
    abortRef.current?.abort();
    seqRef.current += 1;
  }, [scopeKey]);

  function cancelPending() {
    clearTimeout(timerRef.current);
    abortRef.current?.abort();
    abortRef.current = undefined;
    seqRef.current += 1;
  }

  function handleSearchChange(next: string) {
    setSearchValue(next);
    cancelPending();
    const trimmed = truncateUtf8(next.trim(), 256);
    if (trimmed === '' || (singleSelectedLabel !== undefined && next === singleSelectedLabel)) {
      setRemote(null);
      setLoading(false);
      setSearchError('');
      return;
    }
    setRemote(null);
    setLoading(true);
    setSearchError('');
    const seq = seqRef.current;
    timerRef.current = setTimeout(() => {
      const controller = new AbortController();
      abortRef.current = controller;
      void api.searchAliases(
        {
          type: aliasType,
          q: trimmed,
          kind,
          family,
          excludeGroups,
          exclude,
          limit: 20,
        },
        controller.signal,
      ).then((res) => {
        if (!mountedRef.current || seq !== seqRef.current) return;
        setRemote({ query: trimmed, items: res.items });
        if (res.items.length > 0) {
          setKnownLabels((prev) => {
            let changed = false;
            const nextMap = { ...prev };
            for (const item of res.items) {
              if (nextMap[item.id] !== item.name) {
                nextMap[item.id] = item.name;
                changed = true;
              }
            }
            return changed ? nextMap : prev;
          });
        }
        setLoading(false);
        setSearchError('');
      }).catch((err: unknown) => {
        if (controller.signal.aborted || !mountedRef.current || seq !== seqRef.current) return;
        if (err instanceof ApiError && err.status === 401) {
          onSessionLost?.();
          return;
        }
        setRemote({ query: trimmed, items: [] });
        setLoading(false);
        setSearchError('Could not search aliases.');
      });
    }, debounceMs);
  }

  const remoteById = useMemo(() => {
    const map = new Map<string, AliasSearchItem>();
    if (remote) {
      for (const item of remote.items) map.set(item.id, item);
    }
    return map;
  }, [remote]);

  const data = useMemo<AliasOption[]>(() => {
    const base = remote
      ? remote.items.map((item) => ({ value: item.id, label: item.name }))
      : initialOptions;
    const seen = new Set(base.map((o) => o.value));
    const initialById = new Map(initialOptions.map((o) => [o.value, o.label]));
    const extras: AliasOption[] = [];
    for (const id of new Set(selected)) {
      if (!id || seen.has(id)) continue;
      seen.add(id);
      const label = initialById.get(id) ?? knownLabels[id] ?? `${id} (missing)`;
      extras.push({ value: id, label });
    }
    return extras.length > 0 ? [...base, ...extras] : base;
  }, [remote, initialOptions, selected, knownLabels]);

  const filter: OptionsFilter = ({ options }) => {
    const trimmed = searchValue.trim();
    if (trimmed === '' || (singleSelectedLabel !== undefined && searchValue === singleSelectedLabel)) {
      return options;
    }
    if (remote) {
      return options.filter((o: ComboboxParsedItem) => 'value' in o && remoteById.has(o.value));
    }
    // Waiting on the first debounced backend response: show the searching placeholder instead of stale options.
    if (loading) return [];
    return options;
  };

  function renderOption({ option }: { option: ComboboxItem }) {
    const item = remoteById.get(option.value);
    if (item) {
      return (
        <div>
          <Text size="sm" component="div"><Highlighted segments={item.highlights} /></Text>
          {item.summary && (
            <Text size="xs" c="dimmed" component="div">
              {item.summaryHighlights ? <Highlighted segments={item.summaryHighlights} /> : item.summary}
            </Text>
          )}
        </div>
      );
    }
    if (option.label.endsWith(' (missing)')) {
      return <Text size="sm" c="red">{option.label}</Text>;
    }
    return option.label;
  }

  const emptyMessage = searchError || (loading ? 'Searching aliases…' : nothingFoundMessage);

  return {
    data,
    searchValue,
    loading,
    filter,
    emptyMessage,
    handleSearchChange,
    renderOption,
    resetSearch(next = '') {
      cancelPending();
      setSearchValue(next);
      setRemote(null);
      setLoading(false);
      setSearchError('');
    },
  };
}

export function AliasMultiSelect({
  api,
  aliasType,
  kind,
  family,
  excludeGroups,
  exclude,
  initialOptions,
  value,
  onChange,
  label,
  description,
  placeholder,
  error,
  clearable,
  nothingFoundMessage = 'No alias by that name. Aliases are made under Firewall ▸ Aliases.',
  debounceMs,
  onSessionLost,
}: AliasMultiSelectProps) {
  const {
    data, searchValue, loading, filter, emptyMessage, handleSearchChange, renderOption, resetSearch,
  } = useAliasSearch({
    api,
    aliasType,
    kind,
    family,
    excludeGroups,
    exclude,
    initialOptions,
    selected: value,
    nothingFoundMessage,
    debounceMs,
    onSessionLost,
  });

  return (
    <MultiSelect
      label={label}
      description={description}
      placeholder={placeholder}
      data={data}
      value={value}
      error={error}
      searchable
      clearable={clearable}
      searchValue={searchValue}
      onSearchChange={handleSearchChange}
      onDropdownClose={() => resetSearch('')}
      filter={filter}
      renderOption={renderOption}
      rightSection={loading ? <Loader size="xs" /> : undefined}
      nothingFoundMessage={emptyMessage}
      onChange={onChange}
    />
  );
}

export function AliasSelect({
  api,
  aliasType,
  kind,
  family,
  excludeGroups,
  exclude,
  initialOptions,
  value,
  onChange,
  label,
  description,
  placeholder,
  error,
  clearable,
  nothingFoundMessage = 'No alias by that name. Aliases are made under Firewall ▸ Aliases.',
  debounceMs,
  onSessionLost,
}: AliasSelectProps) {
  const selected = useMemo(() => (value ? [value] : []), [value]);
  const lastPickedRef = useRef<{ value: string | null; label: string } | null>(null);

  const {
    data, searchValue, loading, filter, emptyMessage, handleSearchChange, renderOption, resetSearch,
  } = useAliasSearch({
    api,
    aliasType,
    kind,
    family,
    excludeGroups,
    exclude,
    initialOptions,
    selected,
    singleSelected: value ?? undefined,
    nothingFoundMessage,
    debounceMs,
    onSessionLost,
  });

  return (
    <Select
      label={label}
      description={description}
      placeholder={placeholder}
      data={data}
      value={value}
      error={error}
      searchable
      clearable={clearable}
      searchValue={searchValue}
      onSearchChange={handleSearchChange}
      onDropdownClose={() => {
        const picked = lastPickedRef.current;
        lastPickedRef.current = null;
        const chosen = picked
          ? picked.label
          : (value ? data.find((o) => o.value === value)?.label ?? '' : '');
        resetSearch(chosen);
      }}
      filter={filter}
      renderOption={renderOption}
      rightSection={loading ? <Loader size="xs" /> : undefined}
      nothingFoundMessage={emptyMessage}
      onChange={(next) => {
        const chosen = next ? data.find((o) => o.value === next)?.label ?? '' : '';
        lastPickedRef.current = { value: next, label: chosen };
        onChange(next);
        resetSearch(chosen);
      }}
    />
  );
}
