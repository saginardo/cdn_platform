import { useQuery } from "@tanstack/react-query";
import { ArrowRight, CirclePlus, RefreshCw } from "lucide-react";
import { Link } from "react-router-dom";

import {
  EmptyState,
  PageBody,
  PageError,
  PageHeader,
  PageLoading,
} from "@/components/page";
import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { api } from "@/lib/api";
import { formatDateTime, formatNumber } from "@/lib/format";
import type { DeploymentTask, PublishStatus, Site } from "@/lib/types";

interface TLSStatus {
  certificate_task: DeploymentTask | null;
  published_after_certificate: boolean;
}

export function SitesPage() {
  const query = useQuery({
    queryKey: ["sites"],
    queryFn: () => api<Site[]>("/api/sites"),
    refetchInterval: 20_000,
  });
  return (
    <>
      <PageHeader
        title="站点"
        description="域名、源站、边缘节点与发布状态"
        actions={
          <Button asChild>
            <Link to="/sites/new">
              <CirclePlus />
              添加站点
            </Link>
          </Button>
        }
      />
      <PageBody>
        {query.isLoading ? <PageLoading /> : null}
        {query.error ? <PageError error={query.error} /> : null}
        {query.data ? (
          query.data.length ? (
            <div className="border bg-card">
              <div className="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="pl-5">站点</TableHead>
                      <TableHead>协议</TableHead>
                      <TableHead>节点</TableHead>
                      <TableHead>配置</TableHead>
                      <TableHead>状态</TableHead>
                      <TableHead>更新时间</TableHead>
                      <TableHead className="w-12 pr-5">
                        <span className="sr-only">管理</span>
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {query.data.map((site) => (
                      <TableRow key={site.id}>
                        <TableCell className="pl-5">
                          <div className="font-medium">{site.name}</div>
                          <div className="max-w-sm truncate text-xs text-muted-foreground">
                            {site.domains.join(", ") || "无 HTTP 域名"}
                          </div>
                        </TableCell>
                        <TableCell className="text-sm">
                          {siteProtocol(site)}
                        </TableCell>
                        <TableCell className="tabular-nums">
                          {formatNumber(site.node_ids.length)}
                        </TableCell>
                        <TableCell className="text-xs">
                          <div>草稿 v{formatNumber(site.config_version)}</div>
                          <div className="text-muted-foreground">
                            缓存代 {formatNumber(site.cache_generation)}
                          </div>
                        </TableCell>
                        <TableCell>
                          <SiteStatus site={site} />
                        </TableCell>
                        <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                          {formatDateTime(site.updated_at)}
                        </TableCell>
                        <TableCell className="pr-5">
                          <Button asChild variant="ghost" size="icon-sm">
                            <Link
                              to={`/sites/${encodeURIComponent(site.id)}`}
                              aria-label={`管理 ${site.name}`}
                            >
                              <ArrowRight />
                            </Link>
                          </Button>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
              <div className="flex items-center justify-between border-t px-5 py-3 text-xs text-muted-foreground">
                <span>{query.data.length} 个站点</span>
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label="刷新站点"
                  onClick={() => void query.refetch()}
                >
                  <RefreshCw
                    className={query.isFetching ? "animate-spin" : undefined}
                  />
                </Button>
              </div>
            </div>
          ) : (
            <EmptyState
              title="暂无站点"
              description="创建站点后配置域名、源站与边缘节点"
            />
          )
        ) : null}
      </PageBody>
    </>
  );
}

function SiteStatus({ site }: { site: Site }) {
  const encodedID = encodeURIComponent(site.id);
  const publish = useQuery({
    queryKey: ["site-publish", site.id],
    queryFn: () => api<PublishStatus>(`/api/sites/${encodedID}/publish-status`),
    enabled: !site.deleting,
    refetchInterval: (query) =>
      activeTask(query.state.data?.task) ? 2_000 : 20_000,
  });
  const tls = useQuery({
    queryKey: ["site-tls", site.id],
    queryFn: () => api<TLSStatus>(`/api/sites/${encodedID}/tls-status`),
    enabled: !site.deleting && siteNeedsTLS(site),
    refetchInterval: (query) =>
      activeTask(query.state.data?.certificate_task) ? 2_000 : 20_000,
  });

  if (site.deleting) return <StatusBadge status="applying" label="删除中" />;
  if (!site.enabled) return <StatusBadge status="pending" label="已停用" />;
  const publishTask = publish.data?.task;
  const certificateTask = tls.data?.certificate_task;
  return (
    <div className="flex min-w-24 flex-col items-start gap-1">
      <StatusBadge
        status={
          publishTask?.status ?? (site.published ? "succeeded" : "pending")
        }
        label={publishTask ? undefined : site.published ? "已发布" : "待发布"}
      />
      {siteNeedsTLS(site) ? (
        <StatusBadge
          status={certificateTask?.status ?? "pending"}
          label={certificateTask ? undefined : "TLS 未签发"}
        />
      ) : null}
    </div>
  );
}

function activeTask(task?: DeploymentTask | null) {
  return Boolean(
    task && ["queued", "dispatching", "applying"].includes(task.status),
  );
}

function siteNeedsTLS(site: Site) {
  return (
    site.domains.length > 0 ||
    site.tcp_forwards.some((forward) => forward.listen_tls)
  );
}

function siteProtocol(site: Site) {
  if (site.tcp_only) return "TCP / TLS";
  if (site.tcp_forwards.length) return "HTTP + TCP";
  const scheme = site.primary_origin.url.split(":", 1)[0]?.toLowerCase();
  if (scheme === "grpc" || scheme === "grpcs") return "gRPC";
  if (scheme === "ws" || scheme === "wss") return "WebSocket";
  return "HTTP / WS / SSE";
}
