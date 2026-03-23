import { useState } from 'react';

export function DataTable({ columns, rows, renderRow, renderCard, emptyMessage, maxRows }) {
  const [sortCol, setSortCol] = useState(null);
  const [sortAsc, setSortAsc] = useState(true);

  const handleSort = (col) => {
    if (sortCol === col) {
      setSortAsc(!sortAsc);
    } else {
      setSortCol(col);
      setSortAsc(true);
    }
  };

  let displayRows = rows;
  if (maxRows && displayRows.length > maxRows) {
    displayRows = displayRows.slice(0, maxRows);
  }

  return (
    <div>
      {displayRows.length === 0 ? (
        <p className="text-muted text-sm px-4 py-8 text-center">{emptyMessage}</p>
      ) : (
        <>
          {/* Desktop: standard table */}
          <div className="hidden md:block overflow-x-auto">
            <table className="data-table">
              <thead>
                <tr>
                  {columns.map((col) => (
                    <th
                      key={col}
                      onClick={() => handleSort(col)}
                      className="cursor-pointer select-none hover:text-gray-300 transition-colors"
                    >
                      {col}
                      {sortCol === col && (
                        <span className="ml-1 text-accent">{sortAsc ? '↑' : '↓'}</span>
                      )}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>{displayRows.map(renderRow)}</tbody>
            </table>
          </div>

          {/* Mobile: card layout */}
          <div className="md:hidden divide-y divide-surface-3">
            {displayRows.map((row, idx) =>
              renderCard ? renderCard(row, idx) : (
                <div key={idx} className="p-4 space-y-2">
                  {columns.slice(0, 6).map((col) => (
                    <div key={col} className="flex justify-between text-sm">
                      <span className="text-muted">{col}</span>
                      <span className="text-white font-mono">—</span>
                    </div>
                  ))}
                </div>
              )
            )}
          </div>
        </>
      )}
    </div>
  );
}
