import { useMemo, useRef, useState } from "react";
import type { MouseEvent as ReactMouseEvent, ReactNode } from "react";

export type SortDir = "asc" | "desc";

export type Column<T> = {
  key: string;
  header: ReactNode;
  render: (row: T) => ReactNode;
  sortValue?: (row: T) => string | number | boolean | null | undefined;
  filterText?: (row: T) => string;
  sortable?: boolean;
  className?: string;
};

export type DataTableProps<T> = {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T, i: number) => string | number;
  initialSort?: { key: string; dir: SortDir };
  onRowClick?: (row: T) => void;
  rowClassName?: (row: T) => string;
  filterable?: boolean;
  filterPlaceholder?: string;
  empty?: ReactNode;
  // Column resizing is on by default; set false to keep the responsive auto layout.
  resizable?: boolean;
  // When set, resized column widths persist in localStorage under this key.
  storageKey?: string;
};

const MIN_COL_WIDTH = 60;
const WIDTH_STORE_PREFIX = "docent.tableWidths.";

function loadWidths(key?: string): Record<string, number> {
  if (!key || typeof localStorage === "undefined") return {};
  try {
    const raw = localStorage.getItem(WIDTH_STORE_PREFIX + key);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") return {};
    const out: Record<string, number> = {};
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (typeof v === "number" && Number.isFinite(v) && v > 0) out[k] = v;
    }
    return out;
  } catch {
    return {};
  }
}

function saveWidths(key: string, widths: Record<string, number>) {
  if (typeof localStorage === "undefined") return;
  try {
    localStorage.setItem(WIDTH_STORE_PREFIX + key, JSON.stringify(widths));
  } catch {
    // ignore quota / unavailable storage
  }
}

function compareValues(a: string | number | boolean | null | undefined, b: string | number | boolean | null | undefined): number {
  const aNil = a === null || a === undefined || a === "";
  const bNil = b === null || b === undefined || b === "";
  if (aNil && bNil) return 0;
  if (aNil) return 1;
  if (bNil) return -1;
  if (typeof a === "number" && typeof b === "number") return a - b;
  if (typeof a === "boolean" && typeof b === "boolean") return a === b ? 0 : a ? 1 : -1;
  return String(a).localeCompare(String(b));
}

// DataTable renders a `table.tbl` (matching the app's existing styling) with
// client-side sorting (click a sortable header to cycle asc/desc), an optional
// global text filter, and draggable column resizing.
//
// Sizing model: until a column is resized the table keeps the responsive auto
// layout (fills its container, cells wrap). The first time a header edge is
// dragged we measure the current column widths, switch to `table-layout: fixed`
// (so widths are honoured exactly), and pin the table width to the sum of the
// columns. That makes wide columns overflow and scroll horizontally through the
// existing `.table-scroll` wrapper.
export function DataTable<T>({
  columns,
  rows,
  rowKey,
  initialSort,
  onRowClick,
  rowClassName,
  filterable,
  filterPlaceholder,
  empty,
  resizable = true,
  storageKey,
}: DataTableProps<T>) {
  const [sort, setSort] = useState<{ key: string; dir: SortDir } | null>(initialSort ?? null);
  const [filter, setFilter] = useState("");
  const [widths, setWidths] = useState<Record<string, number>>(() => loadWidths(storageKey));
  const [resizingKey, setResizingKey] = useState<string | null>(null);
  const headerEls = useRef<Array<HTMLTableCellElement | null>>([]);

  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return rows;
    return rows.filter((row) =>
      columns.some((c) => {
        const text = c.filterText ? c.filterText(row) : c.sortValue ? String(c.sortValue(row) ?? "") : "";
        return text.toLowerCase().includes(q);
      }),
    );
  }, [rows, columns, filter]);

  const sorted = useMemo(() => {
    if (!sort) return filtered;
    const col = columns.find((c) => c.key === sort.key);
    if (!col || !col.sortValue) return filtered;
    const dir = sort.dir === "asc" ? 1 : -1;
    return [...filtered].sort((a, b) => dir * compareValues(col.sortValue!(a), col.sortValue!(b)));
  }, [filtered, sort, columns]);

  const toggleSort = (col: Column<T>) => {
    if (!(col.sortable ?? !!col.sortValue)) return;
    setSort((prev) => {
      if (!prev || prev.key !== col.key) return { key: col.key, dir: "asc" };
      if (prev.dir === "asc") return { key: col.key, dir: "desc" };
      return null;
    });
  };

  // "sized" once every visible column has a width (from a prior resize or
  // persisted storage); only then do we pin widths and switch to fixed layout.
  const sized = resizable && columns.length > 0 && columns.every((c) => widths[c.key] != null);
  const totalWidth = sized ? columns.reduce((sum, c) => sum + widths[c.key], 0) : undefined;

  // Snapshot the current rendered header widths for every column so that
  // switching to fixed layout is visually seamless.
  const measureWidths = (): Record<string, number> => {
    const next: Record<string, number> = {};
    columns.forEach((c, i) => {
      const el = headerEls.current[i];
      next[c.key] = el ? Math.round(el.getBoundingClientRect().width) : (widths[c.key] ?? 120);
    });
    return next;
  };

  const startResize = (e: ReactMouseEvent, col: Column<T>) => {
    e.preventDefault();
    e.stopPropagation();
    const startX = e.clientX;
    let current = measureWidths();
    const startW = current[col.key];
    setWidths(current);
    setResizingKey(col.key);
    document.body.classList.add("col-resizing");

    const onMove = (ev: MouseEvent) => {
      const w = Math.max(MIN_COL_WIDTH, Math.round(startW + (ev.clientX - startX)));
      current = { ...current, [col.key]: w };
      setWidths(current);
    };
    const onUp = () => {
      document.removeEventListener("mousemove", onMove);
      document.removeEventListener("mouseup", onUp);
      document.body.classList.remove("col-resizing");
      setResizingKey(null);
      if (storageKey) saveWidths(storageKey, current);
    };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  };

  const tableClass =
    "tbl" +
    (resizable ? " tbl--resizable" : "") +
    (sized ? " tbl--fixed" : "") +
    (resizingKey ? " resizing" : "");

  return (
    <>
      {filterable ? (
        <div className="table-toolbar">
          <input
            type="text"
            className="table-search"
            placeholder={filterPlaceholder ?? "Filter…"}
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        </div>
      ) : null}
      {sorted.length === 0 ? (
        <div className="wrap muted">{empty ?? "No rows."}</div>
      ) : (
        <div className="table-scroll">
        <table className={tableClass} style={totalWidth != null ? { width: totalWidth } : undefined}>
          {sized ? (
            <colgroup>
              {columns.map((c) => (
                <col key={c.key} style={{ width: widths[c.key] }} />
              ))}
            </colgroup>
          ) : null}
          <tbody>
            <tr>
              {columns.map((c, i) => {
                const sortable = c.sortable ?? !!c.sortValue;
                const active = sort?.key === c.key;
                return (
                  <th
                    key={c.key}
                    ref={(el) => {
                      headerEls.current[i] = el;
                    }}
                    className={(c.className ?? "") + (sortable ? " sortable" : "")}
                    onClick={sortable ? () => toggleSort(c) : undefined}
                  >
                    {c.header}
                    {sortable ? (
                      <span className={"sort-caret" + (active ? " active" : "")}>
                        {active ? (sort!.dir === "asc" ? "▲" : "▼") : "↕"}
                      </span>
                    ) : null}
                    {resizable ? (
                      <span
                        className={"col-resize" + (resizingKey === c.key ? " active" : "")}
                        title="Drag to resize"
                        onMouseDown={(e) => startResize(e, c)}
                        onClick={(e) => e.stopPropagation()}
                      />
                    ) : null}
                  </th>
                );
              })}
            </tr>
            {sorted.map((row, i) => (
              <tr
                key={rowKey(row, i)}
                className={(onRowClick ? "clickable " : "") + (rowClassName ? rowClassName(row) : "")}
                onClick={onRowClick ? () => onRowClick(row) : undefined}
              >
                {columns.map((c) => (
                  <td key={c.key} className={c.className}>
                    {c.render(row)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
        </div>
      )}
    </>
  );
}
