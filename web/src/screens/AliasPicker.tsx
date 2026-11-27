import { Loader, MultiSelect, Select, Text, type ComboboxItem, type ComboboxParsedItem, type OptionsFilter } from '@mantine/core';
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { isAliasLiteral, parseAliasLiterals, type LiteralOption } from '../aliasLiteral';
import { ApiClient, ApiError } from '../api';
import { isDynamicAddressAlias, type AliasKinds, type AliasSearchItem, type TextSegment } from '../types';

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
    ? 'No matching alias. Enter an IPv4/IPv6 address, CIDR, or range.'
    : 'No matching alias. Enter a port or protocol/port (e.g. 443, tcp/8080, icmp/echo-request).';
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

function labelForValue(
  aliasType: keyof AliasKinds,
  id: string,
  initialById: Map<string, string>,
  knownLabels: Record<string, string>,
): string {
  return initialById.get(id) ?? knownLabels[id] ?? (isAliasLiteral(aliasType, id) ? id : `${id} (missing)`);
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
  onItemsResolved?: (items: AliasSearchItem[]) => void;
  onSessionLost?: () => void;
}) {
  const scopeKey = `${aliasType}|${kind ?? ''}|${family ?? ''}|${excludeGroups ? '1' : '0'}|${excludeDynamic ? '1' : '0'}|${exclude ?? ''}`;
  const resetKey = `${scopeKey}|${singleSelected ?? ''}`;
  const [seenAliasType, setSeenAliasType] = useState(aliasType);
  const [seenResetKey, setSeenResetKey] = useState(resetKey);
  const [knownLabels, setKnownLabels] = useState<Record<string, string>>({});
  const initialById = useMemo(
    () => new Map(initialOptions.map((o) => [o.value, o.label])),
    [initialOptions],
  );
  const singleSelectedLabel = useMemo(() => {
    if (!singleSelected) return undefined;
    return labelForValue(aliasType, singleSelected, initialById, knownLabels);
  }, [aliasType, singleSelected, initialById, knownLabels]);
  const [seenSingleLabel, setSeenSingleLabel] = useState(singleSelectedLabel);
  const [searchValue, setSearchValue] = useState(singleSelectedLabel ?? '');
  const [remote, setRemote] = useState<{ query: string; items: AliasSearchItem[] } | null>(null);
  const [loading, setLoading] = useState(false);
  const [searchError, setSearchError] = useState('');

  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const abortRef = useRef<AbortController | undefined>(undefined);
  const seqRef = useRef(0);
  const mountedRef = useRef(true);
  const onItemsResolvedRef = useRef(onItemsResolved);
  const onSessionLostRef = useRef(onSessionLost);

  useLayoutEffect(() => {
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

  useLayoutEffect(() => {
    cancelPending();
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

  // Literal values are parsed synchronously so typing a literal IP/CIDR/range/port is never blocked by search latency or errors.
  const literalOptions = useMemo<LiteralOption[]>(() => {
    const trimmed = searchValue.trim();
    if (!trimmed || (singleSelectedLabel !== undefined && searchValue === singleSelectedLabel)) {
      return [];
    }
    return parseAliasLiterals(aliasType, trimmed, { family, kind });
  }, [searchValue, singleSelectedLabel, aliasType, family, kind]);

  const literalByValue = useMemo(() => {
    const map = new Map<string, LiteralOption>();
    for (const option of literalOptions) map.set(option.value, option);
    return map;
  }, [literalOptions]);

  const data = useMemo<AliasOption[]>(() => {
    const base = remote
      ? remote.items.map((item) => ({ value: item.id, label: item.name }))
      : initialOptions;
    const seen = new Set(base.map((o) => o.value));
    const extras: AliasOption[] = [];
    for (const option of literalOptions) {
      if (!seen.has(option.value)) {
        seen.add(option.value);
        extras.push({ value: option.value, label: option.label });
      }
    }
    for (const id of new Set(selected)) {
      if (!id || seen.has(id)) continue;
      seen.add(id);
      extras.push({ value: id, label: labelForValue(aliasType, id, initialById, knownLabels) });
    }
    return extras.length > 0 ? [...base, ...extras] : base;
  }, [remote, initialOptions, literalOptions, selected, aliasType, initialById, knownLabels]);

  const filter: OptionsFilter = ({ options }) => {
    const trimmed = searchValue.trim();
    if (trimmed === '' || (singleSelectedLabel !== undefined && searchValue === singleSelectedLabel)) {
      return options;
    }
    if (remote) {
      return options.filter((o: ComboboxParsedItem) => 'value' in o && (remoteById.has(o.value) || literalByValue.has(o.value)));
    }
    if (loading) {
      return options.filter((o: ComboboxParsedItem) => 'value' in o && literalByValue.has(o.value));
    }
    return options;
  };

  function renderOption({ option }: { option: ComboboxItem }) {
    const literal = literalByValue.get(option.value);
    if (literal) {
      return (
        <div>
          <Text size="sm" fw={500} component="div">{literal.optionLabel}</Text>
          <Text size="xs" c="dimmed" component="div">{literal.hint}</Text>
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

  const emptyMessage = searchError || (loading ? 'Searching aliases…' : nothingFoundMessage);

  return {
    data,
    searchValue,
    singleSelectedLabel,
    loading,
    literalOptions,
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
    onItemsResolved, onSessionLost,
  } = props;
  const escapeRef = useRef(false);

  const {
    data, searchValue, loading, literalOptions, filter, emptyMessage, handleSearchChange, renderOption, resetSearch,
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
    onItemsResolved,
    onSessionLost,
  });

  const pendingLiteralRef = useRef<LiteralOption | null>(null);
  useLayoutEffect(() => {
    pendingLiteralRef.current = literalOptions.length === 1 ? literalOptions[0] : null;
  });

  function commitLiteral(literalValue: string) {
    pendingLiteralRef.current = null;
    if (!value.includes(literalValue)) {
      onChange([...value, literalValue]);
    }
    resetSearch('');
  }

  function flushPendingLiteral() {
    const escaped = escapeRef.current;
    escapeRef.current = false;
    const pending = pendingLiteralRef.current;
    pendingLiteralRef.current = null;
    if (!escaped && pending) {
      commitLiteral(pending.value);
      return true;
    }
    return false;
  }

  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Escape') {
      escapeRef.current = true;
      pendingLiteralRef.current = null;
      return;
    }
    if (event.key !== 'Enter') return;
    if (event.currentTarget.getAttribute('aria-activedescendant')) return;
    if (literalOptions.length === 1) {
      event.preventDefault();
      commitLiteral(literalOptions[0].value);
    } else if (literalOptions.length > 1) {
      // Bare port (e.g. "443") offers TCP and UDP; keep the dropdown open instead of submitting the form.
      event.preventDefault();
    }
  }

  return (
    <MultiSelect
      label={label}
      description={description}
      placeholder={placeholder}
      data={data}
      value={value}
      error={error || undefined}
      searchable
      clearable={clearable}
      searchValue={searchValue}
      onSearchChange={(next) => {
        escapeRef.current = false;
        handleSearchChange(next);
      }}
      onKeyDown={handleKeyDown}
      onBlur={() => { flushPendingLiteral(); }}
      onDropdownClose={() => {
        if (!flushPendingLiteral()) resetSearch('');
      }}
      filter={filter}
      renderOption={renderOption}
      rightSection={loading ? <Loader size="xs" /> : undefined}
      nothingFoundMessage={emptyMessage}
      onChange={(next) => {
        pendingLiteralRef.current = null;
        onChange(Array.from(new Set(next)));
      }}
    />
  );
}

export function AliasSelect(props: AliasSelectProps) {
  const {
    api, aliasType, kind, family, excludeGroups, excludeDynamic, exclude, initialOptions, value, onChange,
    label, description, placeholder, error, clearable, nothingFoundMessage, debounceMs,
    onItemsResolved, onSessionLost,
  } = props;
  const selected = useMemo(() => (value ? [value] : []), [value]);
  const lastPickedLabelRef = useRef<string | null>(null);
  const escapeRef = useRef(false);

  const {
    data, searchValue, singleSelectedLabel, loading, literalOptions, filter, emptyMessage, handleSearchChange, renderOption, resetSearch,
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
    onItemsResolved,
    onSessionLost,
  });

  const pendingLiteralRef = useRef<LiteralOption | null>(null);
  useLayoutEffect(() => {
    pendingLiteralRef.current = searchValue !== singleSelectedLabel && literalOptions.length === 1 ? literalOptions[0] : null;
  });

  function handleChange(next: string | null) {
    pendingLiteralRef.current = null;
    const chosen = next ? data.find((o) => o.value === next)?.label ?? next : '';
    lastPickedLabelRef.current = chosen;
    onChange(next);
    resetSearch(chosen);
  }

  function commitSingleLiteral(option: LiteralOption) {
    pendingLiteralRef.current = null;
    lastPickedLabelRef.current = option.label;
    onChange(option.value);
    resetSearch(option.label);
  }

  function flushPendingLiteral() {
    const escaped = escapeRef.current;
    escapeRef.current = false;
    const pending = pendingLiteralRef.current;
    pendingLiteralRef.current = null;
    if (!escaped && lastPickedLabelRef.current === null && pending) {
      commitSingleLiteral(pending);
      return true;
    }
    return false;
  }

  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Escape') {
      escapeRef.current = true;
      pendingLiteralRef.current = null;
      return;
    }
    if (event.key !== 'Enter') return;
    if (event.currentTarget.getAttribute('aria-activedescendant')) return;
    if (literalOptions.length === 1) {
      event.preventDefault();
      commitSingleLiteral(literalOptions[0]);
    } else if (literalOptions.length > 1) {
      event.preventDefault();
    }
  }

  return (
    <Select
      label={label}
      description={description}
      placeholder={placeholder}
      data={data}
      value={value}
      error={error || undefined}
      searchable
      clearable={clearable}
      searchValue={searchValue}
      onSearchChange={(next) => {
        escapeRef.current = false;
        handleSearchChange(next);
      }}
      onKeyDown={handleKeyDown}
      onBlur={() => { flushPendingLiteral(); }}
      onDropdownOpen={() => { lastPickedLabelRef.current = null; }}
      onDropdownClose={() => {
        if (flushPendingLiteral()) return;
        const picked = lastPickedLabelRef.current;
        lastPickedLabelRef.current = null;
        const chosen = picked ?? (value ? data.find((o) => o.value === value)?.label ?? value : '');
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
