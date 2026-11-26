import { Loader, MultiSelect, Select, Text, type ComboboxItem, type ComboboxParsedItem, type OptionsFilter } from '@mantine/core';
import { useEffect, useMemo, useRef, useState } from 'react';
import { parseAliasLiterals, type LiteralAliasCandidate } from '../aliasLiteral';
import { ApiClient, ApiError, apiErrorMessage } from '../api';
import type { AliasKinds, AliasSearchItem, TextSegment } from '../types';

const createOptionPrefix = '__create_alias__:';

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
  allowCreateLiteral?: boolean;
  onAliasCreated?: (alias: AliasKinds[keyof AliasKinds]) => void;
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

function truncateCodePoints(text: string, maxChars: number): string {
  const chars = Array.from(text);
  return chars.length <= maxChars ? text : chars.slice(0, maxChars).join('');
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

function defaultNothingFound(aliasType: keyof AliasKinds, allowCreateLiteral: boolean): string {
  if (!allowCreateLiteral) return 'No alias by that name. Aliases are made under Firewall ▸ Aliases.';
  return aliasType === 'addresses'
    ? 'No matching alias. Type an IPv4/IPv6 address, CIDR, or range to create one.'
    : 'No matching alias. Type a port or protocol/port (e.g. 443, tcp/8080) to create one.';
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
  allowCreateLiteral = true,
  onAliasCreated,
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
  allowCreateLiteral?: boolean;
  onAliasCreated?: (alias: AliasKinds[keyof AliasKinds]) => void;
  onSessionLost?: () => void;
}) {
  const scopeKey = `${aliasType}|${kind ?? ''}|${family ?? ''}|${excludeGroups ? '1' : '0'}|${exclude ?? ''}`;
  const [seenScope, setSeenScope] = useState(scopeKey);
  const [knownLabels, setKnownLabels] = useState<Record<string, string>>({});
  const singleSelectedLabel = useMemo(() => {
    if (!singleSelected) return undefined;
    return initialOptions.find((o) => o.value === singleSelected)?.label ?? knownLabels[singleSelected] ?? `${singleSelected} (missing)`;
  }, [singleSelected, initialOptions, knownLabels]);
  const [seenSingleValue, setSeenSingleValue] = useState(singleSelected);
  const [seenSingleLabel, setSeenSingleLabel] = useState(singleSelectedLabel);
  const [searchValue, setSearchValue] = useState(singleSelectedLabel ?? '');
  const [remote, setRemote] = useState<{ query: string; items: AliasSearchItem[] } | null>(null);
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [searchError, setSearchError] = useState('');
  const [createError, setCreateError] = useState('');

  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const abortRef = useRef<AbortController | undefined>(undefined);
  const seqRef = useRef(0);
  const scopeSeqRef = useRef(0);
  const creatingRef = useRef(false);
  const mountedRef = useRef(true);

  if (scopeKey !== seenScope) {
    setSeenScope(scopeKey);
    setSeenSingleValue(singleSelected);
    setSeenSingleLabel(singleSelectedLabel);
    setRemote(null);
    setLoading(false);
    setSearchError('');
    setCreateError('');
    setSearchValue(singleSelectedLabel ?? '');
  } else if (singleSelected !== seenSingleValue) {
    setSeenSingleValue(singleSelected);
    setSeenSingleLabel(singleSelectedLabel);
    setRemote(null);
    setLoading(false);
    setSearchError('');
    setCreateError('');
    setSearchValue(singleSelectedLabel ?? '');
  } else if (singleSelectedLabel !== seenSingleLabel) {
    setSeenSingleLabel(singleSelectedLabel);
    if (searchValue === (seenSingleLabel ?? '')) {
      setSearchValue(singleSelectedLabel ?? '');
    }
  }

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      clearTimeout(timerRef.current);
      abortRef.current?.abort();
    };
  }, []);

  // Cancel pending work whenever search scope or external single selection changes.
  useEffect(() => {
    clearTimeout(timerRef.current);
    abortRef.current?.abort();
    abortRef.current = undefined;
    seqRef.current += 1;
    scopeSeqRef.current += 1;
  }, [scopeKey, singleSelected]);

  function cancelPending() {
    clearTimeout(timerRef.current);
    abortRef.current?.abort();
    abortRef.current = undefined;
    seqRef.current += 1;
  }

  function handleSearchChange(next: string) {
    setSearchValue(next);
    setCreateError('');
    cancelPending();
    const trimmed = truncateCodePoints(next.trim(), 256);
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

  const createByValue = useMemo(() => {
    const map = new Map<string, LiteralAliasCandidate>();
    if (!allowCreateLiteral || loading || searchError) return map;
    const trimmed = truncateCodePoints(searchValue.trim(), 256);
    if (!trimmed || !remote || remote.query !== trimmed) return map;
    const existingNames = new Set<string>();
    for (const item of remote.items) existingNames.add(item.name);
    for (const opt of initialOptions) existingNames.add(opt.label);
    for (const label of Object.values(knownLabels)) existingNames.add(label);
    const candidates = parseAliasLiterals(aliasType, trimmed, { family, kind });
    for (const candidate of candidates) {
      if (existingNames.has(candidate.payload.name)) continue;
      map.set(`${createOptionPrefix}${candidate.key}`, candidate);
    }
    return map;
  }, [allowCreateLiteral, loading, searchError, searchValue, remote, initialOptions, knownLabels, aliasType, family, kind]);

  const data = useMemo<AliasOption[]>(() => {
    const base = remote
      ? remote.items.map((item) => ({ value: item.id, label: item.name }))
      : initialOptions;
    const seen = new Set(base.map((o) => o.value));
    const initialById = new Map(initialOptions.map((o) => [o.value, o.label]));
    const extras: AliasOption[] = [];
    for (const [optValue, candidate] of createByValue) {
      if (!seen.has(optValue)) {
        seen.add(optValue);
        extras.push({ value: optValue, label: candidate.label });
      }
    }
    for (const id of new Set(selected)) {
      if (!id || seen.has(id)) continue;
      seen.add(id);
      const label = initialById.get(id) ?? knownLabels[id] ?? `${id} (missing)`;
      extras.push({ value: id, label });
    }
    return extras.length > 0 ? [...base, ...extras] : base;
  }, [remote, initialOptions, createByValue, selected, knownLabels]);

  const filter: OptionsFilter = ({ options }) => {
    const trimmed = searchValue.trim();
    if (trimmed === '' || (singleSelectedLabel !== undefined && searchValue === singleSelectedLabel)) {
      return options;
    }
    if (remote) {
      return options.filter((o: ComboboxParsedItem) => 'value' in o && (remoteById.has(o.value) || createByValue.has(o.value)));
    }
    // Waiting on the first debounced backend response: show the searching placeholder instead of stale options.
    if (loading) return [];
    return options;
  };

  function renderOption({ option }: { option: ComboboxItem }) {
    const candidate = createByValue.get(option.value);
    if (candidate) {
      return (
        <div>
          <Text size="sm" fw={500} component="div">{candidate.label}</Text>
          <Text size="xs" c="dimmed" component="div">{candidate.hint}</Text>
        </div>
      );
    }
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

  async function resolveLiteral(candidate: LiteralAliasCandidate): Promise<{ id: string; name: string } | null> {
    if (creatingRef.current) return null;
    creatingRef.current = true;
    const startScopeSeq = scopeSeqRef.current;
    setCreating(true);
    setCreateError('');
    try {
      const saved = await api.saveAlias(aliasType, candidate.payload as never);
      onAliasCreated?.(saved.value);
      if (!mountedRef.current || scopeSeqRef.current !== startScopeSeq) return null;
      setKnownLabels((prev) => (prev[saved.value.id] === saved.value.name ? prev : { ...prev, [saved.value.id]: saved.value.name }));
      return { id: saved.value.id, name: saved.value.name };
    } catch (err: unknown) {
      if (!mountedRef.current) return null;
      if (err instanceof ApiError && err.status === 401) {
        onSessionLost?.();
        return null;
      }
      // If an alias with this exact name already exists outside the initial search page, find and reuse it.
      if (err instanceof ApiError && err.status === 422) {
        try {
          const found = await api.searchAliases({
            type: aliasType,
            q: candidate.payload.name,
            kind,
            family,
            excludeGroups,
            exclude,
            limit: 20,
          });
          if (!mountedRef.current || scopeSeqRef.current !== startScopeSeq) return null;
          const exact = found.items.find((item) => item.name === candidate.payload.name);
          if (exact) {
            setKnownLabels((prev) => (prev[exact.id] === exact.name ? prev : { ...prev, [exact.id]: exact.name }));
            return { id: exact.id, name: exact.name };
          }
        } catch (lookupErr: unknown) {
          if (lookupErr instanceof ApiError && lookupErr.status === 401) {
            onSessionLost?.();
            return null;
          }
        }
      }
      if (mountedRef.current && scopeSeqRef.current === startScopeSeq) {
        setCreateError(apiErrorMessage(err));
      }
      return null;
    } finally {
      creatingRef.current = false;
      if (mountedRef.current) setCreating(false);
    }
  }

  const emptyMessage = searchError || (loading ? 'Searching aliases…' : nothingFoundMessage);

  return {
    data,
    searchValue,
    loading: loading || creating,
    creating,
    creatingRef,
    createByValue,
    createError,
    clearCreateError() { setCreateError(''); },
    resolveLiteral,
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
  nothingFoundMessage,
  debounceMs,
  allowCreateLiteral = true,
  onAliasCreated,
  onSessionLost,
}: AliasMultiSelectProps) {
  const latestValueRef = useRef(value);
  useEffect(() => { latestValueRef.current = value; }, [value]);

  const {
    data, searchValue, loading, createByValue, createError, clearCreateError, resolveLiteral, filter, emptyMessage, handleSearchChange, renderOption, resetSearch,
  } = useAliasSearch({
    api,
    aliasType,
    kind,
    family,
    excludeGroups,
    exclude,
    initialOptions,
    selected: value,
    nothingFoundMessage: nothingFoundMessage ?? defaultNothingFound(aliasType, allowCreateLiteral),
    debounceMs,
    allowCreateLiteral,
    onAliasCreated,
    onSessionLost,
  });

  function handleChange(next: string[]) {
    const createKey = next.find((item) => item.startsWith(createOptionPrefix));
    if (!createKey) {
      clearCreateError();
      onChange(next);
      return;
    }
    const candidate = createByValue.get(createKey);
    if (!candidate) return;
    void resolveLiteral(candidate).then((created) => {
      if (!created) return;
      const current = latestValueRef.current;
      onChange(current.includes(created.id) ? current : [...current, created.id]);
      resetSearch('');
    });
  }

  return (
    <MultiSelect
      label={label}
      description={description}
      placeholder={placeholder}
      data={data}
      value={value}
      error={error || createError || undefined}
      searchable
      clearable={clearable}
      searchValue={searchValue}
      onSearchChange={handleSearchChange}
      onDropdownClose={() => resetSearch('')}
      filter={filter}
      renderOption={renderOption}
      rightSection={loading ? <Loader size="xs" /> : undefined}
      nothingFoundMessage={emptyMessage}
      onChange={handleChange}
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
  nothingFoundMessage,
  debounceMs,
  allowCreateLiteral = true,
  onAliasCreated,
  onSessionLost,
}: AliasSelectProps) {
  const selected = useMemo(() => (value ? [value] : []), [value]);
  const lastPickedRef = useRef<{ value: string | null; label: string } | null>(null);

  const {
    data, searchValue, loading, creatingRef, createByValue, createError, clearCreateError, resolveLiteral, filter, emptyMessage, handleSearchChange, renderOption, resetSearch,
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
    nothingFoundMessage: nothingFoundMessage ?? defaultNothingFound(aliasType, allowCreateLiteral),
    debounceMs,
    allowCreateLiteral,
    onAliasCreated,
    onSessionLost,
  });

  function handleChange(next: string | null) {
    if (next && next.startsWith(createOptionPrefix)) {
      const candidate = createByValue.get(next);
      if (!candidate) return;
      void resolveLiteral(candidate).then((created) => {
        if (!created) return;
        lastPickedRef.current = { value: created.id, label: created.name };
        onChange(created.id);
        resetSearch(created.name);
      });
      return;
    }
    clearCreateError();
    const chosen = next ? data.find((o) => o.value === next)?.label ?? '' : '';
    lastPickedRef.current = { value: next, label: chosen };
    onChange(next);
    resetSearch(chosen);
  }

  return (
    <Select
      label={label}
      description={description}
      placeholder={placeholder}
      data={data}
      value={value}
      error={error || createError || undefined}
      searchable
      clearable={clearable}
      searchValue={searchValue}
      onSearchChange={handleSearchChange}
      onDropdownOpen={() => { lastPickedRef.current = null; }}
      onDropdownClose={() => {
        if (creatingRef.current) return;
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
      onChange={handleChange}
    />
  );
}
