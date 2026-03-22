import { useState } from 'react';

export function DataTable({ columns, rows, renderRow, emptyMessage, maxRows }) {
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
    <div className="overflow-x-auto">
      {displayRows.length === 0 ? (
        <p className="text-muted text-sm px-4 py-8 text-center">{emptyMessage}</p>
      ) : (
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
      )}
    </div>
  );
}
