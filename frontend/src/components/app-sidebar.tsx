import {
  Activity,
  LayoutDashboard,
  LogOut,
  ScrollText,
  Server,
  Settings,
  ShieldCheck,
  Waypoints,
} from "lucide-react";
import { Link, useLocation } from "react-router-dom";

import { BrandMark } from "@/components/brand-mark";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  SidebarSeparator,
  useSidebar,
} from "@/components/ui/sidebar";

const groups = [
  {
    label: "工作区",
    items: [
      { label: "概览", to: "/overview", icon: LayoutDashboard },
      { label: "日志", to: "/logs", icon: ScrollText },
    ],
  },
  {
    label: "运营",
    items: [
      { label: "安全", to: "/security", icon: ShieldCheck },
      { label: "监测", to: "/monitoring", icon: Activity },
      { label: "节点", to: "/nodes", icon: Server },
      { label: "站点", to: "/sites", icon: Waypoints },
    ],
  },
  {
    label: "系统",
    items: [{ label: "设置", to: "/settings", icon: Settings }],
  },
];

export function AppSidebar({
  brandName,
  brandSubtitle,
  brandLogoDataURL,
  brandPending,
  onLogout,
}: {
  brandName: string;
  brandSubtitle: string;
  brandLogoDataURL: string;
  brandPending?: boolean;
  onLogout: () => void;
}) {
  const location = useLocation();
  const { isMobile, setOpenMobile } = useSidebar();
  return (
    <Sidebar collapsible="icon">
      <SidebarHeader className="px-2 py-3">
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              size="lg"
              tooltip={brandName || "控制面板"}
              className="h-11 justify-start px-2"
            >
              <BrandMark logoDataURL={brandLogoDataURL} className="size-8" />
              {brandPending ? (
                <span
                  className="grid min-w-0 gap-1.5"
                  aria-label="正在加载品牌"
                >
                  <span className="h-3 w-24 bg-sidebar-accent" />
                  <span className="h-2.5 w-16 bg-sidebar-accent" />
                </span>
              ) : (
                <span className="grid min-w-0 text-left leading-tight">
                  <span className="truncate font-semibold">{brandName}</span>
                  {brandSubtitle ? (
                    <span className="truncate text-xs text-muted-foreground">
                      {brandSubtitle}
                    </span>
                  ) : null}
                </span>
              )}
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarSeparator />
      <SidebarContent>
        {groups.map((group) => (
          <SidebarGroup key={group.label} className="px-2 py-2">
            <SidebarGroupLabel className="h-7 justify-start px-2">
              {group.label}
            </SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                {group.items.map((item) => {
                  const active =
                    location.pathname === item.to ||
                    location.pathname.startsWith(`${item.to}/`);
                  return (
                    <SidebarMenuItem key={item.to}>
                      <SidebarMenuButton
                        asChild
                        tooltip={item.label}
                        isActive={active}
                        className="justify-start px-2"
                      >
                        <Link
                          to={item.to}
                          onClick={() => {
                            if (isMobile) setOpenMobile(false);
                          }}
                        >
                          <item.icon />
                          <span>{item.label}</span>
                        </Link>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  );
                })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        ))}
      </SidebarContent>
      <SidebarSeparator />
      <SidebarFooter>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              tooltip="退出登录"
              onClick={onLogout}
              className="justify-start px-2 text-muted-foreground hover:text-foreground"
            >
              <LogOut />
              <span>退出登录</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  );
}
