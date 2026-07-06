import { useMemo, useState } from "react";
import type { ReactNode } from "react";

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
};

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
// client-side sorting (click a sortable header to cycle asc/desc) and an
// optional global text filter across each column's filterText.
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
}: DataTableProps<T>) {
  const [sort, setSort] = useState<{ key: string; dir: SortDir } | null>(initialSort ?? null);
  const [filter, setFilter] = useState("");

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
        <table className="tbl">
          <tbody>
            <tr>
              {columns.map((c) => {
                const sortable = c.sortable ?? !!c.sortValue;
                const active = sort?.key === c.key;
                return (
                  <th
                    key={c.key}
                    className={(c.className ?? "") + (sortable ? " sortable" : "")}
                    onClick={sortable ? () => toggleSort(c) : undefined}
                  >
                    {c.header}
                    {sortable ? (
                      <span className={"sort-caret" + (active ? " active" : "")}>
                        {active ? (sort!.dir === "asc" ? "▲" : "▼") : "↕"}
                      </span>
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
      )}
    </>
  );
}
