import { useState } from "react";

export const LIST_PAGE_SIZE = 20;

export interface ListPaginationState<T> {
  items: T[];
  page: number;
  pageSize: number;
  totalItems: number;
  totalPages: number;
  start: number;
  end: number;
  startIndex: number;
  setPage: (page: number) => void;
}

export function useListPagination<T>(
  items: readonly T[],
  pageSize = LIST_PAGE_SIZE,
): ListPaginationState<T> {
  const [requestedPage, setRequestedPage] = useState(1);
  const totalItems = items.length;
  const totalPages = Math.max(1, Math.ceil(totalItems / pageSize));
  const page = Math.min(requestedPage, totalPages);
  const startIndex = (page - 1) * pageSize;
  const endIndex = Math.min(startIndex + pageSize, totalItems);

  return {
    items: items.slice(startIndex, endIndex),
    page,
    pageSize,
    totalItems,
    totalPages,
    start: totalItems ? startIndex + 1 : 0,
    end: endIndex,
    startIndex,
    setPage: (nextPage) =>
      setRequestedPage(Math.max(1, Math.min(nextPage, totalPages))),
  };
}
