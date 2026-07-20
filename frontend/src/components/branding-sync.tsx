import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";

import {
  cacheBranding,
  DEFAULT_BRANDING,
  type Branding,
  useCachedBranding,
} from "@/hooks/use-branding";
import { api } from "@/lib/api";

export function BrandingSync() {
  const cachedBranding = useCachedBranding();
  const query = useQuery({
    queryKey: ["public-branding"],
    queryFn: () => api<Branding>("/api/branding"),
    staleTime: 60_000,
  });

  useEffect(() => {
    if (query.data) cacheBranding(query.data);
  }, [query.data]);

  const branding = cachedBranding ?? query.data ?? DEFAULT_BRANDING;
  useEffect(() => {
    document.title = branding.subtitle
      ? `${branding.name} · ${branding.subtitle}`
      : branding.name;

    let favicon = document.querySelector<HTMLLinkElement>(
      'link[rel="icon"][data-branding-icon]',
    );
    if (!branding.logo_data_url) {
      favicon?.remove();
      return;
    }
    if (!favicon) {
      favicon = document.createElement("link");
      favicon.rel = "icon";
      favicon.dataset.brandingIcon = "";
      document.head.append(favicon);
    }
    favicon.type = branding.logo_data_url.startsWith("data:image/png")
      ? "image/png"
      : "image/jpeg";
    favicon.href = branding.logo_data_url;
  }, [branding]);

  return null;
}
