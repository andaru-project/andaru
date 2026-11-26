import { Loader, MultiSelect, Select, Text, type ComboboxItem, type ComboboxParsedItem, type OptionsFilter } from '@mantine/core';
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { parseAliasLiterals, type LiteralAliasCandidate } from '../aliasLiteral';
import { ApiClient, ApiError, apiErrorMessage } from '../api';
import { isDynamicAddressAlias, type AliasKinds, type AliasSearchItem, type TextSegment } from '../types';

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
  excludeDynamic?: boolean;
  exclude?: string;
  initialOptions: AliasOption[];
  label: string;
  description?: string;
  placeholder?: string;
  error?: string;
  clearable?: boolean;
  nothingFoundMessage?: string;
  debounceMs?: number;
  onAliasCreated?: (alias: AliasKinds[keyof AliasKinds]) => void;
  onCreatingChange?: (creating: boolean) => void;
  onItemsResolved?: (items: AliasSearchItem[]) => void;
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

function defaultNothingFound(aliasType: keyof AliasKinds): string {
  return aliasType === 'addresses'
    ? 'No matching alias. Type an IPv4/IPv6 address, CIDR, or range to create one.'
    : 'No matching alias. Type a port or protocol/port (e.g. 443, tcp/8080) to create one.';
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
  excludeDynamic,
  exclude,
  initialOptions,
  selected,
  singleSelected,
  nothingFoundMessage,
  debounceMs = 150,
  onAliasCreated,
  onCreatingChange,
  onItemsResolved,
  onSessionLost,
}: {
  api: ApiClient;
  aliasType: keyof AliasKinds;
  kind?: string;
  family?: 'ipv4' | 'ipv6';
  excludeGroups?: boolean;
  excludeDynamic?: boolean;
  exclude?: string;
  initialOptions: AliasOption[];
  selected: string[];
  singleSelected?: string;
  nothingFoundMessage: string;
  debounceMs?: number;
  onAliasCreated?: (alias: AliasKinds[keyof AliasKinds]) => void;
  onCreatingChange?: (creating: boolean) => void;
  onItemsResolved?: (items: AliasSearchItem[]) => void;
  onSessionLost?: () => void;
}) {
  const scopeKey = `${aliasType}|${kind ?? ''}|${family ?? ''}|${excludeGroups ? '1' : '0'}|${excludeDynamic ? '1' : '0'}|${exclude ?? ''}`;
  const resetKey = `${scopeKey}|${singleSelected ?? ''}`;
  const [seenAliasType, setSeenAliasType] = useState(aliasType);
  const [seenResetKey, setSeenResetKey] = useState(resetKey);
  const [knownLabels, setKnownLabels] = useState<Record<string, string>>({});
  const singleSelectedLabel = useMemo(() => {
    if (!singleSelected) return undefined;
    return initialOptions.find((o) => o.value === singleSelected)?.label ?? knownLabels[singleSelected] ?? `${singleSelected} (missing)`;
  }, [singleSelected, initialOptions, knownLabels]);
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
  const searchSeqRef = useRef(0);
  const scopeSeqRef = useRef(0);
  const creatingRef = useRef(false);
  const mountedRef = useRef(true);
  const onAliasCreatedRef = useRef(onAliasCreated);
  const onCreatingChangeRef = useRef(onCreatingChange);
  const onItemsResolvedRef = useRef(onItemsResolved);
  const onSessionLostRef = useRef(onSessionLost);

  useLayoutEffect(() => {
    onAliasCreatedRef.current = onAliasCreated;
    onCreatingChangeRef.current = onCreatingChange;
    onItemsResolvedRef.current = onItemsResolved;
    onSessionLostRef.current = onSessionLost;
  });

  if (aliasType !== seenAliasType) {
    setSeenAliasType(aliasType);
    setKnownLabels({});
  }
  if (resetKey !== seenResetKey) {
    setSeenResetKey(resetKey);
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

  function cancelPending() {
    clearTimeout(timerRef.current);
    abortRef.current?.abort();
    abortRef.current = undefined;
    seqRef.current += 1;
  }

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      cancelPending();
    };
  }, []);

  // Synchronously advance scopeSeq on commit so an in-flight literal create cannot land in a changed scope.
  useLayoutEffect(() => {
    cancelPending();
    scopeSeqRef.current += 1;
  }, [resetKey]);

  function search(q: string, signal?: AbortSignal) {
    return api.searchAliases({ type: aliasType, q, kind, family, excludeGroups, exclude, limit: 20 }, signal);
  }

  function rememberLabels(items: Array<{ id: string; name: string }>) {
    if (items.length === 0) return;
    setKnownLabels((prev) => {
      let changed = false;
      const next = { ...prev };
      for (const item of items) {
        if (next[item.id] !== item.name) {
          next[item.id] = item.name;
          changed = true;
        }
      }
      return changed ? next : prev;
    });
  }

  function resetSearch(next = '') {
    cancelPending();
    if (!mountedRef.current) return;
    setSearchValue(next);
    setRemote(null);
    setLoading(false);
    setSearchError('');
  }

  function handleSearchChange(next: string) {
    if (creatingRef.current) return;
    searchSeqRef.current += 1;
    setCreateError('');
    const trimmed = truncateCodePoints(next.trim(), 256);
    if (trimmed === '' || (singleSelectedLabel !== undefined && next === singleSelectedLabel)) {
      resetSearch(next);
      return;
    }
    cancelPending();
    setSearchValue(next);
    setRemote(null);
    setLoading(true);
    setSearchError('');
    const seq = seqRef.current;
    timerRef.current = setTimeout(() => {
      const controller = new AbortController();
      abortRef.current = controller;
      void search(trimmed, controller.signal).then((res) => {
        if (!mountedRef.current || seq !== seqRef.current) return;
        const items = aliasType === 'addresses' && excludeDynamic
          ? res.items.filter((item) => !isDynamicAddressAlias(item))
          : res.items;
        setRemote({ query: trimmed, items });
        rememberLabels(items);
        onItemsResolvedRef.current?.(res.items);
        setLoading(false);
        setSearchError('');
      }).catch((err: unknown) => {
        if (controller.signal.aborted || !mountedRef.current || seq !== seqRef.current) return;
        if (err instanceof ApiError && err.status === 401) {
          onSessionLostRef.current?.();
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
    if (loading || searchError) return map;
    const trimmed = searchValue.trim();
    if (!trimmed || trimmed.length > 128 || !remote || remote.query !== trimmed) return map;
    // Suppress only aliases visible in remote.items so non-canonical or stale initialOptions cannot dead-end the dropdown.
    const visibleNames = new Set(remote.items.map((item) => item.name));
    for (const candidate of parseAliasLiterals(aliasType, trimmed, { family, kind })) {
      if (visibleNames.has(candidate.payload.name)) continue;
      map.set(`${createOptionPrefix}${candidate.key}`, candidate);
    }
    return map;
  }, [loading, searchError, searchValue, remote, aliasType, family, kind]);

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
      return <Text size="sm" className="error-text">{option.label}</Text>;
    }
    return option.label;
  }

  async function resolveLiteral(candidate: LiteralAliasCandidate): Promise<{ id: string; name: string } | null> {
    if (creatingRef.current) return null;
    creatingRef.current = true;
    cancelPending();
    const startScopeSeq = scopeSeqRef.current;
    setCreating(true);
    setLoading(false);
    setCreateError('');
    onCreatingChangeRef.current?.(true);
    try {
      const saved = await api.saveAlias(aliasType, candidate.payload as never);
      onAliasCreatedRef.current?.(saved.value);
      if (!mountedRef.current || scopeSeqRef.current !== startScopeSeq) return null;
      rememberLabels([saved.value]);
      return { id: saved.value.id, name: saved.value.name };
    } catch (err: unknown) {
      if (!mountedRef.current) return null;
      if (err instanceof ApiError && err.status === 401) {
        onSessionLostRef.current?.();
        return null;
      }
      // On 422 name collision, reuse an existing alias only when both its kind/protocol and summary match the literal.
      if (err instanceof ApiError && err.status === 422) {
        try {
          const found = await search(candidate.payload.name);
          if (!mountedRef.current || scopeSeqRef.current !== startScopeSeq) return null;
          const exactName = found.items.find((item) => item.name === candidate.payload.name);
          if (exactName) {
            const exactKind = exactName.kind ?? exactName.protocol;
            if (exactKind === candidate.expectedKind && exactName.summary === candidate.expectedSummary) {
              rememberLabels([exactName]);
              return { id: exactName.id, name: exactName.name };
            }
            setCreateError(`An alias named "${candidate.payload.name}" already exists with different values (${exactName.summary || exactKind || 'unknown'}).`);
            return null;
          }
        } catch (lookupErr: unknown) {
          if (lookupErr instanceof ApiError && lookupErr.status === 401) {
            onSessionLostRef.current?.();
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
      onCreatingChangeRef.current?.(false);
      if (mountedRef.current) setCreating(false);
    }
  }

  const emptyMessage = searchError || (loading ? 'Searching aliases…' : nothingFoundMessage);

  return {
    data,
    searchValue,
    loading: loading || creating,
    creatingRef,
    searchSeqRef,
    createByValue,
    createError,
    clearCreateError() { setCreateError(''); },
    resolveLiteral,
    filter,
    emptyMessage,
    handleSearchChange,
    renderOption,
    resetSearch,
  };
}

export function AliasMultiSelect(props: AliasMultiSelectProps) {
  const {
    api, aliasType, kind, family, excludeGroups, excludeDynamic, exclude, initialOptions, value, onChange,
    label, description, placeholder, error, clearable, nothingFoundMessage, debounceMs,
    onAliasCreated, onCreatingChange, onItemsResolved, onSessionLost,
  } = props;
  const latestValueRef = useRef(value);
  const onChangeRef = useRef(onChange);
  useLayoutEffect(() => {
    latestValueRef.current = value;
    onChangeRef.current = onChange;
  });

  const {
    data, searchValue, loading, searchSeqRef, createByValue, createError, clearCreateError, resolveLiteral, filter, emptyMessage, handleSearchChange, renderOption, resetSearch,
  } = useAliasSearch({
    api,
    aliasType,
    kind,
    family,
    excludeGroups,
    excludeDynamic,
    exclude,
    initialOptions,
    selected: value,
    nothingFoundMessage: nothingFoundMessage ?? defaultNothingFound(aliasType),
    debounceMs,
    onAliasCreated,
    onCreatingChange,
    onItemsResolved,
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
    const startSearchSeq = searchSeqRef.current;
    void resolveLiteral(candidate).then((created) => {
      if (!created) return;
      const current = latestValueRef.current;
      onChangeRef.current(current.includes(created.id) ? current : [...current, created.id]);
      if (searchSeqRef.current === startSearchSeq) resetSearch('');
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

export function AliasSelect(props: AliasSelectProps) {
  const {
    api, aliasType, kind, family, excludeGroups, excludeDynamic, exclude, initialOptions, value, onChange,
    label, description, placeholder, error, clearable, nothingFoundMessage, debounceMs,
    onAliasCreated, onCreatingChange, onItemsResolved, onSessionLost,
  } = props;
  const selected = useMemo(() => (value ? [value] : []), [value]);
  const lastPickedLabelRef = useRef<string | null>(null);
  const onChangeRef = useRef(onChange);
  const latestValueRef = useRef(value);
  useLayoutEffect(() => {
    onChangeRef.current = onChange;
    latestValueRef.current = value;
  });

  const {
    data, searchValue, loading, creatingRef, createByValue, createError, clearCreateError, resolveLiteral, filter, emptyMessage, handleSearchChange, renderOption, resetSearch,
  } = useAliasSearch({
    api,
    aliasType,
    kind,
    family,
    excludeGroups,
    excludeDynamic,
    exclude,
    initialOptions,
    selected,
    singleSelected: value ?? undefined,
    nothingFoundMessage: nothingFoundMessage ?? defaultNothingFound(aliasType),
    debounceMs,
    onAliasCreated,
    onCreatingChange,
    onItemsResolved,
    onSessionLost,
  });

  const dataRef = useRef(data);
  useLayoutEffect(() => { dataRef.current = data; });

  function handleChange(next: string | null) {
    if (next && next.startsWith(createOptionPrefix)) {
      if (creatingRef.current) return;
      const candidate = createByValue.get(next);
      if (!candidate) return;
      void resolveLiteral(candidate).then((created) => {
        if (!created) {
          const cur = latestValueRef.current;
          resetSearch(cur ? dataRef.current.find((o) => o.value === cur)?.label ?? '' : '');
          return;
        }
        lastPickedLabelRef.current = created.name;
        onChangeRef.current(created.id);
        resetSearch(created.name);
      });
      return;
    }
    clearCreateError();
    const chosen = next ? data.find((o) => o.value === next)?.label ?? '' : '';
    lastPickedLabelRef.current = chosen;
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
      onDropdownOpen={() => { lastPickedLabelRef.current = null; }}
      onDropdownClose={() => {
        if (creatingRef.current) return;
        const picked = lastPickedLabelRef.current;
        lastPickedLabelRef.current = null;
        const chosen = picked ?? (value ? data.find((o) => o.value === value)?.label ?? '' : '');
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
