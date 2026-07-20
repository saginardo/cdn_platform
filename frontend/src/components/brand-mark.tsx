import { Globe2 } from "lucide-react";

import { cn } from "@/lib/utils";

export function BrandMark({
  logoDataURL,
  className,
  iconClassName,
}: {
  logoDataURL: string;
  className?: string;
  iconClassName?: string;
}) {
  return (
    <span
      className={cn(
        "grid shrink-0 place-items-center overflow-hidden rounded-md",
        logoDataURL
          ? "border bg-background p-0.5"
          : "bg-primary text-primary-foreground",
        className,
      )}
      aria-hidden="true"
    >
      {logoDataURL ? (
        <img src={logoDataURL} alt="" className="size-full object-contain" />
      ) : (
        <Globe2 className={cn("size-4", iconClassName)} />
      )}
    </span>
  );
}
