import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  CirclePlus,
  Clock,
  Gauge,
  LoaderCircle,
  RefreshCw,
  Server,
  Trash2,
} from "lucide-react";
import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import { toast } from "sonner";

import { ConfirmDialog } from "@/components/confirm-dialog";
import { ListPagination } from "@/components/list-pagination";
import {
  EmptyState,
  PageBody,
  PageError,
  PageHeader,
  PageLoading,
} from "@/components/page";
import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Progress } from "@/components/ui/progress";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useListPagination } from "@/hooks/use-list-pagination";
import { api, errorMessage, jsonBody } from "@/lib/api";
import { formatDateTime, formatNumber } from "@/lib/format";
import type {
  MonitoringNode,
  MonitoringOverview,
  MonitoringTarget,
} from "@/lib/types";

export function MonitoringPage() {
  const queryClient = useQueryClient();
  const [createOpen, setCreateOpen] = useState(false);
  const [removeTarget, setRemoveTarget] = useState<MonitoringTarget | null>(
    null,
  );
  const query = useQuery({
    queryKey: ["monitoring"],
    queryFn: () => api<MonitoringOverview>("/api/monitoring"),
    refetchInterval: 10_000,
  });
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: ["monitoring"] });
  const toggleTarget = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      api<MonitoringTarget>(
        `/api/monitoring/targets/${encodeURIComponent(id)}`,
        { method: "PUT", ...jsonBody({ enabled }) },
      ),
    onSuccess: (target) => {
      void refresh();
      toast.success(target.enabled ? "拨测目标已启用" : "拨测目标已停用");
    },
    onError: (error) => toast.error(errorMessage(error)),
  });
  const remove = useMutation({
    mutationFn: (target: MonitoringTarget) =>
      api<{ ok: boolean }>(
        `/api/monitoring/targets/${encodeURIComponent(target.id)}`,
        { method: "DELETE" },
      ),
    onSuccess: () => {
      setRemoveTarget(null);
      void refresh();
      toast.success("拨测目标已删除");
    },
    onError: (error) => toast.error(errorMessage(error)),
  });
  const data = query.data;
  const nodesPagination = useListPagination(data?.nodes ?? []);
  const results = useMemo(
    () =>
      (data?.nodes ?? []).flatMap((node) =>
        node.results.map((result) => ({ node, result })),
      ),
    [data?.nodes],
  );
  const resultsPagination = useListPagination(results);
  const targetsPagination = useListPagination(data?.targets ?? []);
  const enabledTargets = data?.targets.filter((target) => target.enabled) ?? [];
  const capableNodes = data?.nodes.filter((node) => node.capable) ?? [];
  const healthyNodes = capableNodes.filter(
    (node) =>
      !node.stale &&
      node.score !== undefined &&
      node.score >= (data?.healthy_score ?? 80),
  );
  const autoPaused = data?.nodes.filter(
    (node) => node.monitor_auto_paused,
  ).length;

  return (
    <>
      <PageHeader
        title="监测"
        description="边缘 TCP 可达性、访问时延与调度状态"
        actions={
          <>
            <Button
              variant="outline"
              size="icon"
              aria-label="刷新监测数据"
              onClick={() => void query.refetch()}
            >
              <RefreshCw className={query.isFetching ? "animate-spin" : ""} />
            </Button>
            <Button onClick={() => setCreateOpen(true)}>
              <CirclePlus />
              添加目标
            </Button>
          </>
        }
      />
      <PageBody>
        {query.isLoading ? <PageLoading /> : null}
        {query.error ? <PageError error={query.error} /> : null}
        {data ? (
          <>
            <section className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
              <Summary
                icon={<Activity />}
                label="启用目标"
                value={formatNumber(enabledTargets.length)}
                detail={`${data.interval_seconds} 秒 / 每轮 ${data.attempts_per_round} 次`}
              />
              <Summary
                icon={<Server />}
                label="监测覆盖"
                value={`${capableNodes.length} / ${data.nodes.length}`}
                detail="已支持节点"
              />
              <Summary
                icon={<Gauge />}
                label="当前正常"
                value={`${healthyNodes.length} / ${capableNodes.length}`}
                detail={`健康线 ${data.healthy_score} 分`}
              />
              <Summary
                icon={<Clock />}
                label="自动暂停"
                value={formatNumber(autoPaused)}
                detail={`连续 ${data.auto_pause_after} 轮异常`}
                danger={Boolean(autoPaused)}
              />
            </section>

            {!enabledTargets.length ? (
              <EmptyState
                title="暂无启用的拨测目标"
                description="添加或启用目标后，边缘节点会开始上报 TCP 拨测结果"
              />
            ) : null}

            <Tabs defaultValue="nodes" className="space-y-4">
              <TabsList>
                <TabsTrigger value="nodes">节点评分</TabsTrigger>
                <TabsTrigger value="results">拨测明细</TabsTrigger>
                <TabsTrigger value="targets">目标配置</TabsTrigger>
              </TabsList>
              <TabsContent value="nodes">
                {data.nodes.length ? (
                  <div className="border bg-card">
                    <div className="overflow-x-auto">
                      <Table className="min-w-[940px]">
                        <TableHeader>
                          <TableRow>
                            <TableHead className="pl-5">节点</TableHead>
                            <TableHead>调度</TableHead>
                            <TableHead>监测</TableHead>
                            <TableHead className="w-44">评分</TableHead>
                            <TableHead>成功率</TableHead>
                            <TableHead>平均时延</TableHead>
                            <TableHead>连续异常</TableHead>
                            <TableHead className="pr-5">最后拨测</TableHead>
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {nodesPagination.items.map((node) => (
                            <NodeRow
                              key={node.node_id}
                              node={node}
                              healthyScore={data.healthy_score}
                            />
                          ))}
                        </TableBody>
                      </Table>
                    </div>
                    <ListPagination
                      pagination={nodesPagination}
                      itemLabel="个节点"
                    />
                  </div>
                ) : (
                  <EmptyState title="暂无边缘节点" />
                )}
              </TabsContent>
              <TabsContent value="results">
                {results.length ? (
                  <div className="border bg-card">
                    <div className="overflow-x-auto">
                      <Table className="min-w-[820px]">
                        <TableHeader>
                          <TableRow>
                            <TableHead className="pl-5">节点</TableHead>
                            <TableHead>拨测目标</TableHead>
                            <TableHead>TCP 结果</TableHead>
                            <TableHead>成功次数</TableHead>
                            <TableHead>平均时延</TableHead>
                            <TableHead className="pr-5">拨测时间</TableHead>
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {resultsPagination.items.map(({ node, result }) => {
                            const succeeded =
                              result.successful_attempts === result.attempts;
                            return (
                              <TableRow
                                key={`${node.node_id}:${result.target_id}`}
                              >
                                <TableCell className="pl-5">
                                  <div className="font-medium">{node.name}</div>
                                  <div className="font-mono text-xs text-muted-foreground">
                                    {node.public_ipv4}
                                  </div>
                                </TableCell>
                                <TableCell className="font-mono text-xs">
                                  {result.address}
                                </TableCell>
                                <TableCell>
                                  <StatusBadge
                                    status={succeeded ? "succeeded" : "failed"}
                                    label={succeeded ? "可达" : "异常"}
                                  />
                                  {result.error ? (
                                    <div
                                      className="mt-1 max-w-64 truncate text-xs text-muted-foreground"
                                      title={result.error}
                                    >
                                      {result.error}
                                    </div>
                                  ) : null}
                                </TableCell>
                                <TableCell className="tabular-nums">
                                  {result.successful_attempts} /{" "}
                                  {result.attempts}
                                </TableCell>
                                <TableCell className="tabular-nums">
                                  {result.successful_attempts
                                    ? `${result.average_latency_ms.toFixed(1)} ms`
                                    : "--"}
                                </TableCell>
                                <TableCell className="pr-5 whitespace-nowrap text-xs text-muted-foreground">
                                  {formatDateTime(result.checked_at)}
                                </TableCell>
                              </TableRow>
                            );
                          })}
                        </TableBody>
                      </Table>
                    </div>
                    <ListPagination
                      pagination={resultsPagination}
                      itemLabel="条结果"
                    />
                  </div>
                ) : (
                  <EmptyState title="等待节点上报拨测结果" />
                )}
              </TabsContent>
              <TabsContent value="targets">
                {data.targets.length ? (
                  <div className="border bg-card">
                    <div className="overflow-x-auto">
                      <Table className="min-w-[660px]">
                        <TableHeader>
                          <TableRow>
                            <TableHead className="pl-5">目标地址</TableHead>
                            <TableHead>状态</TableHead>
                            <TableHead>更新时间</TableHead>
                            <TableHead className="w-16 pr-5 text-right">
                              操作
                            </TableHead>
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {targetsPagination.items.map((target) => (
                            <TableRow key={target.id}>
                              <TableCell className="pl-5 font-mono text-xs">
                                {target.address}
                              </TableCell>
                              <TableCell>
                                <div className="flex items-center gap-2">
                                  <Switch
                                    checked={target.enabled}
                                    disabled={toggleTarget.isPending}
                                    aria-label={`${target.enabled ? "停用" : "启用"} ${target.address}`}
                                    onCheckedChange={(enabled) =>
                                      toggleTarget.mutate({
                                        id: target.id,
                                        enabled,
                                      })
                                    }
                                  />
                                  <span className="text-xs text-muted-foreground">
                                    {target.enabled ? "启用" : "停用"}
                                  </span>
                                </div>
                              </TableCell>
                              <TableCell className="text-xs text-muted-foreground">
                                {formatDateTime(target.updated_at)}
                              </TableCell>
                              <TableCell className="pr-5 text-right">
                                <Tooltip>
                                  <TooltipTrigger asChild>
                                    <Button
                                      variant="ghost"
                                      size="icon-sm"
                                      aria-label={`删除 ${target.address}`}
                                      onClick={() => setRemoveTarget(target)}
                                    >
                                      <Trash2 />
                                    </Button>
                                  </TooltipTrigger>
                                  <TooltipContent>删除目标</TooltipContent>
                                </Tooltip>
                              </TableCell>
                            </TableRow>
                          ))}
                        </TableBody>
                      </Table>
                    </div>
                    <ListPagination
                      pagination={targetsPagination}
                      itemLabel="个目标"
                    />
                  </div>
                ) : (
                  <EmptyState title="暂无拨测目标" />
                )}
              </TabsContent>
            </Tabs>
          </>
        ) : null}
      </PageBody>
      <CreateTargetDialog open={createOpen} onOpenChange={setCreateOpen} />
      <ConfirmDialog
        open={Boolean(removeTarget)}
        onOpenChange={(open) => {
          if (!open) setRemoveTarget(null);
        }}
        title="删除拨测目标"
        description={`将删除 ${removeTarget?.address ?? "该目标"} 的当前拨测结果。`}
        confirmLabel="删除"
        destructive
        busy={remove.isPending}
        onConfirm={() => {
          if (removeTarget) remove.mutate(removeTarget);
        }}
      />
    </>
  );
}

function NodeRow({
  node,
  healthyScore,
}: {
  node: MonitoringNode;
  healthyScore: number;
}) {
  const monitoringState = !node.capable
    ? { status: "pending", label: "待升级" }
    : node.score === undefined
      ? { status: "pending", label: "等待上报" }
      : node.stale
        ? { status: "pending", label: "数据过期" }
        : node.score >= healthyScore
          ? { status: "succeeded", label: "正常" }
          : { status: "failed", label: "异常" };
  return (
    <TableRow>
      <TableCell className="pl-5">
        <div className="font-medium">{node.name}</div>
        <div className="font-mono text-xs text-muted-foreground">
          {node.public_ipv4}
        </div>
      </TableCell>
      <TableCell>
        <StatusBadge
          status={node.status}
          label={node.monitor_auto_paused ? "监测暂停" : undefined}
        />
      </TableCell>
      <TableCell>
        <StatusBadge
          status={monitoringState.status}
          label={monitoringState.label}
        />
      </TableCell>
      <TableCell>
        {node.score === undefined ? (
          <span className="text-muted-foreground">--</span>
        ) : (
          <div className="grid w-36 grid-cols-[2rem_1fr] items-center gap-2">
            <span className="font-medium tabular-nums">{node.score}</span>
            <Progress value={node.score} />
          </div>
        )}
      </TableCell>
      <TableCell className="tabular-nums">
        {node.success_rate === undefined
          ? "--"
          : `${node.success_rate.toFixed(1)}%`}
      </TableCell>
      <TableCell className="tabular-nums">
        {node.average_latency_ms === undefined
          ? "--"
          : `${node.average_latency_ms.toFixed(1)} ms`}
      </TableCell>
      <TableCell className="tabular-nums">
        {formatNumber(node.consecutive_abnormal)}
      </TableCell>
      <TableCell className="pr-5 whitespace-nowrap text-xs text-muted-foreground">
        {formatDateTime(node.last_checked_at)}
      </TableCell>
    </TableRow>
  );
}

function Summary({
  icon,
  label,
  value,
  detail,
  danger = false,
}: {
  icon: ReactNode;
  label: string;
  value: string;
  detail: string;
  danger?: boolean;
}) {
  return (
    <Card size="sm">
      <CardContent className="flex items-center gap-3">
        <div
          className={
            danger
              ? "grid size-9 shrink-0 place-items-center rounded-md bg-red-50 text-red-600 dark:bg-red-950 dark:text-red-300 [&_svg]:size-4"
              : "grid size-9 shrink-0 place-items-center rounded-md bg-muted text-muted-foreground [&_svg]:size-4"
          }
        >
          {icon}
        </div>
        <div className="min-w-0">
          <div className="text-xs text-muted-foreground">{label}</div>
          <div className="text-lg font-semibold tabular-nums">{value}</div>
          <div className="truncate text-xs text-muted-foreground">{detail}</div>
        </div>
      </CardContent>
    </Card>
  );
}

function CreateTargetDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const [address, setAddress] = useState("");
  const mutation = useMutation({
    mutationFn: () =>
      api<MonitoringTarget>("/api/monitoring/targets", {
        method: "POST",
        ...jsonBody({ address }),
      }),
    onSuccess: () => {
      setAddress("");
      onOpenChange(false);
      void queryClient.invalidateQueries({ queryKey: ["monitoring"] });
      toast.success("拨测目标已添加");
    },
    onError: (error) => toast.error(errorMessage(error)),
  });
  function submit(event: FormEvent) {
    event.preventDefault();
    mutation.mutate();
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>添加拨测目标</DialogTitle>
            <DialogDescription>配置 TCP 连接目标</DialogDescription>
          </DialogHeader>
          <div className="grid gap-2 py-5">
            <Label htmlFor="monitoring-address">IP:端口 或 域名:端口</Label>
            <Input
              id="monitoring-address"
              value={address}
              onChange={(event) => setAddress(event.target.value)}
              placeholder="probe.example.com:443"
              autoComplete="off"
              spellCheck={false}
              autoFocus
            />
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={mutation.isPending}
              onClick={() => onOpenChange(false)}
            >
              取消
            </Button>
            <Button
              type="submit"
              disabled={!address.trim() || mutation.isPending}
            >
              {mutation.isPending ? (
                <LoaderCircle className="animate-spin" />
              ) : (
                <CirclePlus />
              )}
              添加
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
