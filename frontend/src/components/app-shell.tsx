import { useQuery } from "@tanstack/react-query";
import { Bell, ChevronRight } from "lucide-react";
import { Suspense, useState } from "react";
import { Link, Outlet, useLocation } from "react-router-dom";

import { AppSidebar } from "@/components/app-sidebar";
import { MessageCenter, useMessages } from "@/components/message-center";
import { PageBody, PageLoading } from "@/components/page";
import { ThemeToggle } from "@/components/theme-toggle";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import {
  SidebarInset,
  SidebarProvider,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useAuth } from "@/features/auth/auth-provider";
import { api } from "@/lib/api";
import type { Settings } from "@/lib/types";

const pageNames: Record<string, string> = {
  overview: "概览",
  logs: "日志",
  security: "安全",
  nodes: "节点",
  sites: "站点",
  settings: "设置",
};

export function AppShell() {
  const [messagesOpen, setMessagesOpen] = useState(false);
  const { logout } = useAuth();
  const messageQuery = useMessages();
  const settingsQuery = useQuery({
    queryKey: ["settings"],
    queryFn: () => api<Settings>("/api/settings"),
  });
  const location = useLocation();
  const segments = location.pathname.split("/").filter(Boolean);
  const section = segments[0] || "overview";
  const detail = segments.length > 1;

  return (
    <SidebarProvider
      style={{ "--sidebar-width": "13.5rem" } as React.CSSProperties}
    >
      <AppSidebar
        brandName={settingsQuery.data?.branding?.name || "CDN Platform"}
        brandSubtitle={settingsQuery.data?.branding?.subtitle ?? "控制面板"}
        onLogout={() => void logout()}
      />
      <SidebarInset className="min-w-0">
        <header className="sticky top-0 z-20 flex h-12 shrink-0 items-center gap-2 border-b bg-background/95 px-3 backdrop-blur supports-[backdrop-filter]:bg-background/80 sm:px-5">
          <SidebarTrigger aria-label="切换侧边栏" />
          <Separator orientation="vertical" className="mx-1 h-4" />
          <nav
            className="flex min-w-0 items-center gap-1 text-sm"
            aria-label="面包屑"
          >
            <Link
              to={`/${section}`}
              className="truncate text-muted-foreground hover:text-foreground"
            >
              {pageNames[section] ?? "概览"}
            </Link>
            {detail ? (
              <>
                <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />
                <span className="truncate">详情</span>
              </>
            ) : null}
          </nav>
          <div className="ml-auto flex items-center gap-1">
            <ThemeToggle />
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="relative"
                  aria-label="打开消息中心"
                  onClick={() => setMessagesOpen(true)}
                >
                  <Bell />
                  {messageQuery.data?.unread_count ? (
                    <span className="absolute -right-0.5 -top-0.5 size-2 rounded-full bg-red-500" />
                  ) : null}
                </Button>
              </TooltipTrigger>
              <TooltipContent>消息中心</TooltipContent>
            </Tooltip>
          </div>
        </header>
        <Suspense
          fallback={
            <PageBody>
              <PageLoading />
            </PageBody>
          }
        >
          <Outlet />
        </Suspense>
      </SidebarInset>
      <MessageCenter
        open={messagesOpen}
        onOpenChange={setMessagesOpen}
        page={messageQuery.data}
      />
    </SidebarProvider>
  );
}
