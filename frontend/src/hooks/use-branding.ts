import { useEffect, useState } from "react";

import type { Settings } from "@/lib/types";

export type Branding = Settings["branding"];

export const DEFAULT_BRANDING: Branding = {
  name: "simple_cdn",
  subtitle: "控制面板",
  logo_data_url: "",
};

const STORAGE_KEY = "simple_cdn:branding:v1";
const CHANGE_EVENT = "cdn:branding-changed";
const MAX_CACHED_LOGO_LENGTH = 180_000;

export function cacheBranding(branding: Branding) {
  const normalized = normalizeBranding(branding);
  if (!normalized) return;
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(normalized));
    window.dispatchEvent(new Event(CHANGE_EVENT));
  } catch {
    // Storage can be unavailable in hardened or private browser contexts.
  }
}

export function useCachedBranding() {
  const [branding, setBranding] = useState<Branding | null>(readCachedBranding);

  useEffect(() => {
    const sync = () => setBranding(readCachedBranding());
    const syncStorage = (event: StorageEvent) => {
      if (event.key === STORAGE_KEY) sync();
    };
    window.addEventListener(CHANGE_EVENT, sync);
    window.addEventListener("storage", syncStorage);
    return () => {
      window.removeEventListener(CHANGE_EVENT, sync);
      window.removeEventListener("storage", syncStorage);
    };
  }, []);

  return branding;
}

function readCachedBranding(): Branding | null {
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY);
    if (!stored) return null;
    return normalizeBranding(JSON.parse(stored));
  } catch {
    return null;
  }
}

function normalizeBranding(value: unknown): Branding | null {
  if (!value || typeof value !== "object") return null;
  const candidate = value as Partial<Branding>;
  if (typeof candidate.name !== "string" || !candidate.name.trim()) return null;
  if (typeof candidate.subtitle !== "string") return null;
  const logoDataURL =
    typeof candidate.logo_data_url === "string" &&
    candidate.logo_data_url.length <= MAX_CACHED_LOGO_LENGTH &&
    (candidate.logo_data_url.startsWith("data:image/png;base64,") ||
      candidate.logo_data_url.startsWith("data:image/jpeg;base64,"))
      ? candidate.logo_data_url
      : "";
  return {
    name: candidate.name.trim(),
    subtitle: candidate.subtitle.trim(),
    logo_data_url: logoDataURL,
  };
}
