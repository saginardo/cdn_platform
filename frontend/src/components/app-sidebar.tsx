import {
  Globe2,
  LayoutDashboard,
  LogOut,
  Mail,
  ScrollText,
  Server,
  Settings,
  ShieldCheck,
  Waypoints,
} from "lucide-react";
import { Link, useLocation } from "react-router-dom";

import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuBadge,
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
  unread,
  onMessages,
  onLogout,
}: {
  unread: number;
  onMessages: () => void;
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
              tooltip="CDN Platform"
              className="h-11 justify-start px-2"
            >
              <span className="grid size-8 shrink-0 place-items-center rounded-md bg-primary text-primary-foreground">
                <Globe2 className="size-4" />
              </span>
              <span className="grid min-w-0 text-left leading-tight">
                <span className="truncate font-semibold">CDN Platform</span>
                <span className="truncate text-xs text-muted-foreground">
                  控制面
                </span>
              </span>
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
              tooltip="消息中心"
              onClick={onMessages}
              className="justify-start px-2"
            >
              <Mail />
              <span>消息中心</span>
            </SidebarMenuButton>
            {unread ? (
              <SidebarMenuBadge>
                {unread > 99 ? "99+" : unread}
              </SidebarMenuBadge>
            ) : null}
          </SidebarMenuItem>
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
